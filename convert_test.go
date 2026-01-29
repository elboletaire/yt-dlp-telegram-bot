package main

import (
	"bytes"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type namedReadCloser struct {
	io.ReadCloser
	name string
}

func (n *namedReadCloser) Name() string {
	return n.name
}

func TestConverterProbeUsesFileWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "sample.mp4")

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", "color=c=black:s=16x16:d=1",
		"-f", "lavfi",
		"-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
		"-shortest",
		"-c:v", "libx264",
		"-c:a", "aac",
		videoPath,
	)
	var ffmpegErr bytes.Buffer
	cmd.Stderr = &ffmpegErr
	if err := cmd.Run(); err != nil {
		t.Fatalf("creating sample video: %v: %s", err, ffmpegErr.String())
	}

	badReader := io.NopCloser(strings.NewReader("not a video"))
	rr := NewReReadCloser(&namedReadCloser{ReadCloser: badReader, name: videoPath})

	conv := Converter{}
	if err := conv.Probe(rr); err != nil {
		t.Fatalf("expected probe to succeed using file path, got error: %v", err)
	}
}
