package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wader/goutubedl"
)

const downloadAndConvertTimeout = 5 * time.Minute

type ConvertStartCallbackFunc func(ctx context.Context, videoCodecs, audioCodecs, convertActionsNeeded string)
type UpdateProgressPercentCallbackFunc func(progressStr string, progressPercent int)

type Downloader struct {
	ConvertStartFunc          ConvertStartCallbackFunc
	UpdateProgressPercentFunc UpdateProgressPercentCallbackFunc
}

type goYouTubeDLLogger struct{}

func (l goYouTubeDLLogger) Print(v ...interface{}) {
	fmt.Print("  yt-dlp dbg:")
	fmt.Println(v...)
}

func (d *Downloader) downloadURL(dlCtx context.Context, url string) (rr *ReReadCloser, title, extractor string, err error) {
	opts := goutubedl.Options{
		Type:     goutubedl.TypeSingle,
		DebugLog: goYouTubeDLLogger{},
		// StderrFn:          func(cmd *exec.Cmd) io.Writer { return io.Writer(os.Stdout) },
		MergeOutputFormat: "mkv",     // This handles VP9 properly. yt-dlp uses mp4 by default, which doesn't.
		SortingFormat:     "res:720", // Prefer videos no larger than 720p to keep their size small.
	}

	result, err := goutubedl.New(dlCtx, url, opts)
	dlOpts := goutubedl.DownloadOptions{}
	if errors.Is(err, goutubedl.ErrNotASingleEntry) {
		// Some links resolve to a playlist, e.g. a tweet with a video quoting
		// another video. Its first entry is the video of the linked post
		// itself, so retry accepting playlists and download only that one.
		fmt.Println("  link resolves to a playlist, using its first entry")
		opts.Type = goutubedl.TypeAny
		result, err = goutubedl.New(dlCtx, url, opts)
		dlOpts.PlaylistIndex = 1
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("preparing download %q: %w", url, err)
	}

	title = result.Info.Title
	extractor = result.Info.Extractor
	if dlOpts.PlaylistIndex > 0 {
		if len(result.Info.Entries) == 0 {
			return nil, "", "", fmt.Errorf("preparing download %q: playlist contains no downloadable entries", url)
		}
		first := result.Info.Entries[0]
		if first.Title != "" {
			title = first.Title
		}
		if first.Extractor != "" {
			extractor = first.Extractor
		}
	}

	dlResult, err := result.DownloadWithOptions(dlCtx, dlOpts)
	if err != nil {
		return nil, "", "", fmt.Errorf("downloading %q: %w", url, err)
	}

	tmpFile, err := os.CreateTemp("", "yt-dlp-telegram-bot-*")
	if err != nil {
		_ = dlResult.Close()
		return nil, "", "", fmt.Errorf("creating temp file for %q: %w", url, err)
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, dlResult); err != nil {
		_ = dlResult.Close()
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, "", "", fmt.Errorf("writing temp file for %q: %w", url, err)
	}
	if err := dlResult.Close(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, "", "", fmt.Errorf("closing downloader for %q: %w", url, err)
	}
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, "", "", fmt.Errorf("rewinding temp file for %q: %w", url, err)
	}

	return NewReReadCloser(&tempFileReadCloser{File: tmpFile, path: tmpPath}), title, extractor, nil
}

func (d *Downloader) DownloadAndConvertURL(ctx context.Context, url, format string) (r io.ReadCloser, outputFormat, title string, videoMetadata *VideoMetadata, err error) {
	rr, title, extractor, err := d.downloadURL(ctx, url)
	if err != nil {
		return nil, "", "", nil, err
	}

	conv := Converter{
		Format:                        format,
		UpdateProgressPercentCallback: d.UpdateProgressPercentFunc,
	}
	if isYouTubeExtractor(extractor) {
		conv.ForceReencode = true
	}

	if err := conv.Probe(rr); err != nil {
		_ = rr.Close()
		return nil, "", "", nil, err
	}

	if d.ConvertStartFunc != nil {
		d.ConvertStartFunc(ctx, conv.VideoCodecs, conv.AudioCodecs, conv.GetActionsNeeded())
	}

	r, outputFormat, err = conv.ConvertIfNeeded(ctx, rr)
	if err != nil {
		_ = rr.Close()
		return nil, "", "", nil, err
	}

	return r, outputFormat, title, conv.VideoMetadata, nil
}
