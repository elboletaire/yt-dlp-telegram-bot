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
	dlQueue.HandleProgressPercentUpdate(uploadStr, int(state.Uploaded*100/state.Total))
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

	if err := p.editMediaWithRetry(ctx, qEntry, editDocument); err == nil {
		return nil
	} else {
		fmt.Println("  edit failed, falling back to sending media:", err)
	}

	if err := p.sendMediaWithRetry(ctx, qEntry, document); err != nil {
		return err
	}

	// Best-effort cleanup of the old progress message to avoid duplicates.
	qEntry.deleteProgressMessage(ctx)
	return nil
}

// editMediaWithRetry edits the progress message into a media message with FLOOD_WAIT retry handling.
func (p *Uploader) editMediaWithRetry(ctx context.Context, qEntry *DownloadQueueEntry, document message.MediaOption) error {
	if qEntry.ProgressMsgID == 0 {
		return fmt.Errorf("no progress message ID to edit")
	}

	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		_, err := qEntry.Reply.Edit(qEntry.ProgressMsgID).Media(ctx, document)
		if err == nil {
			return nil
		}

		if waitDuration, isFloodWait := tgerr.AsFloodWait(err); isFloodWait {
			fmt.Printf("  FLOOD_WAIT detected while editing, waiting %v seconds (attempt %d/%d)\n", waitDuration.Seconds(), attempt+1, maxRetries)

			timer := time.NewTimer(waitDuration + time.Second)
			select {
			case <-timer.C:
				continue
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		} else {
			return fmt.Errorf("edit: %w", err)
		}
	}

	return fmt.Errorf("failed to edit media after %d attempts due to rate limiting", maxRetries)
}

// sendMediaWithRetry sends media with automatic FLOOD_WAIT retry handling
func (p *Uploader) sendMediaWithRetry(ctx context.Context, qEntry *DownloadQueueEntry, document message.MediaOption) error {
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		var err error

		// Try to send the media as reply to original message
		if qEntry.OrigMsgUpdate != nil {
			// Regular message - send as reply
			_, err = telegramSender.Reply(qEntry.OrigEntities, qEntry.OrigMsgUpdate).Media(ctx, document)
		} else if qEntry.OrigChannelMsgUpdate != nil {
			// Channel message - send as reply
			_, err = telegramSender.Reply(qEntry.OrigEntities, qEntry.OrigChannelMsgUpdate).Media(ctx, document)
		} else {
			return fmt.Errorf("no valid update found in queue entry")
		}

		if err == nil {
			// Success!
			return nil
		}

		// Check if it's a FLOOD_WAIT error
		if waitDuration, isFloodWait := tgerr.AsFloodWait(err); isFloodWait {
			fmt.Printf("  FLOOD_WAIT detected, waiting %v seconds (attempt %d/%d)\n", waitDuration.Seconds(), attempt+1, maxRetries)

			// Wait for the required duration plus a small buffer
			timer := time.NewTimer(waitDuration + time.Second)
			select {
			case <-timer.C:
				// Continue to retry
				continue
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		} else {
			// Not a FLOOD_WAIT error, return immediately
			return fmt.Errorf("send: %w", err)
		}
	}

	return fmt.Errorf("failed to send media after %d attempts due to rate limiting", maxRetries)
}
