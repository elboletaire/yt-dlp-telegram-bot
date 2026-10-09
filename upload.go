package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/flytam/filenamify"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type Uploader struct{}

var dlUploader Uploader

func (p Uploader) Chunk(ctx context.Context, state uploader.ProgressState) error {
	dlQueue.HandleProgressPercentUpdate(T("uploading", nil), int(state.Uploaded*100/state.Total))
	return nil
}

func (p *Uploader) UploadFile(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage, f io.ReadCloser, format, title string) error {
	// Reading to a buffer first, because we don't know the file size.
	var buf bytes.Buffer
	for {
		b := make([]byte, 1024)
		n, err := f.Read(b)
		if err != nil && err != io.EOF {
			return fmt.Errorf("reading to buffer error: %w", err)
		}
		if n == 0 {
			break
		}
		if params.MaxSize > 0 && buf.Len() > int(params.MaxSize) {
			return fmt.Errorf("file is too big, max. allowed size is %s", humanize.BigBytes(big.NewInt(int64(params.MaxSize))))
		}
		buf.Write(b[:n])
	}

	fmt.Println("  got", buf.Len(), "bytes, uploading...")
	dlQueue.currentlyDownloadedEntry.progressInfo = fmt.Sprint(" (", humanize.BigBytes(big.NewInt(int64(buf.Len()))), ")")

	upload, err := telegramUploader.FromBytes(ctx, "yt-dlp", buf.Bytes())
	if err != nil {
		return fmt.Errorf("uploading %w", err)
	}

	// Now we have uploaded file handle, sending it as styled message. First, preparing message.
	var document message.MediaOption
	filename, _ := filenamify.Filenamify(title+"."+format, filenamify.Options{Replacement: " "})
	if format == "mp3" {
		document = message.UploadedDocument(upload).Filename(filename).Audio().Title(title)
	} else {
		document = message.UploadedDocument(upload).Filename(filename).Video()
	}

	// Sending message with media.
	if _, err := telegramSender.Answer(entities, u).Media(ctx, document); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	return nil
}

func (p *Uploader) UploadFileFromEntry(ctx context.Context, qEntry *DownloadQueueEntry, f io.ReadCloser, format, title string, videoMetadata *VideoMetadata) error {
	// Reading to a buffer first, because we don't know the file size.
	var buf bytes.Buffer
	for {
		b := make([]byte, 1024)
		n, err := f.Read(b)
		if err != nil && err != io.EOF {
			return fmt.Errorf("reading to buffer error: %w", err)
		}
		if n == 0 {
			break
		}
		if params.MaxSize > 0 && buf.Len() > int(params.MaxSize) {
			return fmt.Errorf("file is too big, max. allowed size is %s", humanize.BigBytes(big.NewInt(int64(params.MaxSize))))
		}
		buf.Write(b[:n])
	}

	fmt.Println("  got", buf.Len(), "bytes, uploading...")
	dlQueue.currentlyDownloadedEntry.progressInfo = fmt.Sprint(" (", humanize.BigBytes(big.NewInt(int64(buf.Len()))), ")")

	upload, err := telegramUploader.FromBytes(ctx, "yt-dlp", buf.Bytes())
	if err != nil {
		return fmt.Errorf("uploading %w", err)
	}

	// Now we have uploaded file handle, sending it as styled message. First, preparing message.
	var document message.MediaOption
	var editDocument message.MediaOption
	filename, _ := filenamify.Filenamify(title+"."+format, filenamify.Options{Replacement: " "})
	if format == "mp3" {
		document = message.UploadedDocument(upload).Filename(filename).Audio().Title(title)
		editDocument = message.UploadedDocument(upload, styling.Plain(" ")).Filename(filename).Audio().Title(title)
	} else {
		// Create video document with metadata if available
		uploadedDoc := message.UploadedDocument(upload).Filename(filename)
		videoDoc := uploadedDoc.Video()

		// Add video metadata if available
		if videoMetadata != nil {
			fmt.Printf("  adding video metadata: %dx%d, duration: %ds\n", videoMetadata.Width, videoMetadata.Height, videoMetadata.Duration)
			videoDoc = videoDoc.Resolution(videoMetadata.Width, videoMetadata.Height)
			if videoMetadata.Duration > 0 {
				videoDoc = videoDoc.Duration(time.Duration(videoMetadata.Duration) * time.Second)
			}
		}

		document = videoDoc
		uploadedDocForEdit := message.UploadedDocument(upload, styling.Plain(" ")).Filename(filename)
		editVideoDoc := uploadedDocForEdit.Video()
		if videoMetadata != nil {
			editVideoDoc = editVideoDoc.Resolution(videoMetadata.Width, videoMetadata.Height)
			if videoMetadata.Duration > 0 {
				editVideoDoc = editVideoDoc.Duration(time.Duration(videoMetadata.Duration) * time.Second)
			}
		}
		editDocument = editVideoDoc
	}

	dlQueue.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
	dlQueue.currentlyDownloadedEntry.disableProgressPercentUpdate = true
	dlQueue.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()

	var markup tg.ReplyMarkupClass
	if likeStore != nil && qEntry.ChatPeer != nil {
		markup = likeMarkup(0)
	}

	// With an ephemeral progress message there is nothing public to edit into
	// the final media; the video is always sent as a normal reply instead.
	if qEntry.Ephemeral == nil {
		if sent, err := p.editMediaWithRetry(ctx, qEntry, editDocument, markup); err == nil {
			recordUpload(qEntry, sent, format, title)
			return nil
		} else {
			fmt.Println("  edit failed, falling back to sending media:", err)
		}
	}

	sent, err := p.sendMediaWithRetry(ctx, qEntry, document, markup)
	if err != nil {
		return err
	}
	recordUpload(qEntry, sent, format, title)

	// Best-effort cleanup of the old progress message to avoid duplicates.
	qEntry.deleteProgressMessage(ctx)
	return nil
}

// withFloodRetry runs fn, waiting and retrying up to 3 times on FLOOD_WAIT.
func withFloodRetry(ctx context.Context, what string, fn func() error) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		waitDuration, isFloodWait := tgerr.AsFloodWait(err)
		if !isFloodWait {
			return fmt.Errorf("%s: %w", what, err)
		}
		fmt.Printf("  FLOOD_WAIT detected (%s), waiting %v seconds (attempt %d/%d)\n", what, waitDuration.Seconds(), attempt+1, maxRetries)
		timer := time.NewTimer(waitDuration + time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s failed after %d attempts due to rate limiting", what, maxRetries)
}

// editMediaWithRetry edits the progress message into a media message with FLOOD_WAIT retry handling.
func (p *Uploader) editMediaWithRetry(ctx context.Context, qEntry *DownloadQueueEntry, document message.MediaOption, markup tg.ReplyMarkupClass) (tg.UpdatesClass, error) {
	if qEntry.ProgressMsgID == 0 {
		return nil, fmt.Errorf("no progress message ID to edit")
	}
	var res tg.UpdatesClass
	err := withFloodRetry(ctx, "edit", func() error {
		// A fresh builder, so the markup doesn't stick to qEntry.Reply, which is
		// still used for error and cancel texts afterwards.
		reply, err := origReply(qEntry)
		if err != nil {
			return err
		}
		res, err = reply.Markup(markup).Edit(qEntry.ProgressMsgID).Media(ctx, document)
		return err
	})
	return res, err
}

// sendMediaWithRetry sends media as a reply to the original message with FLOOD_WAIT retry handling.
func (p *Uploader) sendMediaWithRetry(ctx context.Context, qEntry *DownloadQueueEntry, document message.MediaOption, markup tg.ReplyMarkupClass) (tg.UpdatesClass, error) {
	var res tg.UpdatesClass
	err := withFloodRetry(ctx, "send", func() error {
		reply, err := origReply(qEntry)
		if err != nil {
			return err
		}
		res, err = reply.Markup(markup).Media(ctx, document)
		return err
	})
	return res, err
}

// origReply returns a new builder replying to the request's original message.
func origReply(qEntry *DownloadQueueEntry) (*message.Builder, error) {
	switch {
	case qEntry.OrigMsgUpdate != nil:
		return telegramSender.Reply(qEntry.OrigEntities, qEntry.OrigMsgUpdate), nil
	case qEntry.OrigChannelMsgUpdate != nil:
		return telegramSender.Reply(qEntry.OrigEntities, qEntry.OrigChannelMsgUpdate), nil
	default:
		return nil, fmt.Errorf("no valid update found in queue entry")
	}
}
