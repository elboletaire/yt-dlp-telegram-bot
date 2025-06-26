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

func (p *Uploader) UploadFileFromEntry(ctx context.Context, qEntry *DownloadQueueEntry, f io.ReadCloser, format, title string) error {
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

	// Sending message with media - handle both regular and channel messages with FLOOD_WAIT retry
	return p.sendMediaWithRetry(ctx, qEntry, document)
}

// sendMediaWithRetry sends media with automatic FLOOD_WAIT retry handling
func (p *Uploader) sendMediaWithRetry(ctx context.Context, qEntry *DownloadQueueEntry, document message.MediaOption) error {
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		var err error

		// Try to send the media based on message type
		if qEntry.OrigMsgUpdate != nil {
			// Regular message
			_, err = telegramSender.Answer(qEntry.OrigEntities, qEntry.OrigMsgUpdate).Media(ctx, document)
		} else if qEntry.OrigChannelMsgUpdate != nil {
			// Channel message
			_, err = telegramSender.Answer(qEntry.OrigEntities, qEntry.OrigChannelMsgUpdate).Media(ctx, document)
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
