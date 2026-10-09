package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultMessagesRender(t *testing.T) {
	if got := T("queued", map[string]any{"Position": 3}); got != "👨‍👦‍👦 Request queued at position #3" {
		t.Fatalf("queued = %q", got)
	}
	if got := T("error", map[string]any{"Err": "boom"}); got != "❌ Error: boom" {
		t.Fatalf("error = %q", got)
	}
	if got := T("canceled", nil); got != "❌ Canceled by user" {
		t.Fatalf("canceled = %q", got)
	}
}

func TestSourceCodecsTemplate(t *testing.T) {
	cases := []struct {
		video, audio, convert, want string
	}{
		{"h264", "aac", "", "🎬 Source: h264 / aac (no conversion needed)"},
		{"h264", "", "video", "🎬 Source: h264, no audio (converting: video)"},
		{"", "mp3", "", "🎬 Source: mp3 (no conversion needed)"},
	}
	for _, c := range cases {
		got := T("source_codecs", map[string]any{"Video": c.video, "Audio": c.audio, "Convert": c.convert})
		if got != c.want {
			t.Errorf("source_codecs(%q,%q,%q) = %q, want %q", c.video, c.audio, c.convert, got, c.want)
		}
	}
}

func TestMissingKey(t *testing.T) {
	if got := T("does_not_exist", nil); got != "!(does_not_exist)" {
		t.Fatalf("got %q", got)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "messages.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMessagesFileOverrides(t *testing.T) {
	t.Cleanup(resetMessages)
	if err := LoadMessagesFile(writeTemp(t, "canceled: \"❌ Cancel·lat\"\n")); err != nil {
		t.Fatal(err)
	}
	if got := T("canceled", nil); got != "❌ Cancel·lat" {
		t.Fatalf("override not applied: %q", got)
	}
	if got := T("message_deleted", nil); got != "🗑️ Message deleted" {
		t.Fatalf("non-overridden key changed: %q", got)
	}
}

func TestLoadMessagesFileJSON(t *testing.T) {
	t.Cleanup(resetMessages)
	if err := LoadMessagesFile(writeTemp(t, `{"liked": "👍"}`)); err != nil {
		t.Fatal(err)
	}
	if got := T("liked", nil); got != "👍" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadMessagesFileUnknownKey(t *testing.T) {
	t.Cleanup(resetMessages)
	if err := LoadMessagesFile(writeTemp(t, "cancelled: typo\n")); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestLoadMessagesFileBadTemplate(t *testing.T) {
	t.Cleanup(resetMessages)
	if err := LoadMessagesFile(writeTemp(t, "queued: \"{{.Position\"\n")); err == nil {
		t.Fatal("expected error for bad template")
	}
	// A failed load must not leave a partial override behind.
	if got := T("queued", map[string]any{"Position": 1}); got != "👨‍👦‍👦 Request queued at position #1" {
		t.Fatalf("catalog changed after failed load: %q", got)
	}
}
