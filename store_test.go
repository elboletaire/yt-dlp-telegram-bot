package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var base = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func addVideo(t *testing.T, s *Store, chatID int64, msgID int, userID int64, posted time.Time) {
	t.Helper()
	err := s.AddVideo(Video{
		ChatID: chatID, ChatType: ChatTypeChannel, ChatHash: 42, MsgID: msgID,
		UserID: userID, UserName: "@poster", Title: "t", URL: "https://x",
		DocID: int64(msgID) * 10, DocHash: 7, FileRef: []byte{1, 2}, PostedAt: posted,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAddAndGetVideo(t *testing.T) {
	s := newTestStore(t)
	addVideo(t, s, 100, 5, 1, base)
	v, err := s.GetVideo(100, 5)
	if err != nil || v == nil {
		t.Fatalf("GetVideo: %v %v", v, err)
	}
	if v.UserID != 1 || v.DocID != 50 || string(v.FileRef) != "\x01\x02" || !v.PostedAt.Equal(base) || v.ChatType != ChatTypeChannel {
		t.Fatalf("unexpected video: %+v", v)
	}
	missing, err := s.GetVideo(100, 6)
	if err != nil || missing != nil {
		t.Fatalf("expected nil,nil for missing video, got %v %v", missing, err)
	}
}

func TestToggleLike(t *testing.T) {
	s := newTestStore(t)
	addVideo(t, s, 100, 5, 1, base)
	liked, count, err := s.ToggleLike(100, 5, 2)
	if err != nil || !liked || count != 1 {
		t.Fatalf("first like: %v %d %v", liked, count, err)
	}
	liked, count, _ = s.ToggleLike(100, 5, 3)
	if !liked || count != 2 {
		t.Fatalf("second user: %v %d", liked, count)
	}
	liked, count, _ = s.ToggleLike(100, 5, 2)
	if liked || count != 1 {
		t.Fatalf("unlike: %v %d", liked, count)
	}
}

func TestTopVideos(t *testing.T) {
	s := newTestStore(t)
	addVideo(t, s, 100, 1, 1, base)                  // 1 like
	addVideo(t, s, 100, 2, 1, base.Add(time.Hour))   // 2 likes
	addVideo(t, s, 100, 3, 1, base.Add(2*time.Hour)) // 1 like, later than #1
	addVideo(t, s, 100, 4, 1, base.Add(3*time.Hour)) // 0 likes → excluded
	addVideo(t, s, 100, 5, 1, base.Add(-time.Hour))  // before window
	addVideo(t, s, 200, 6, 1, base)                  // other chat
	for _, l := range []struct {
		msg  int
		user int64
	}{{1, 2}, {2, 2}, {2, 3}, {3, 2}, {5, 2}, {5, 3}, {5, 4}, {6, 2}} {
		chat := int64(100)
		if l.msg == 6 {
			chat = 200
		}
		if _, _, err := s.ToggleLike(chat, l.msg, l.user); err != nil {
			t.Fatal(err)
		}
	}

	top, err := s.TopVideos(100, base, base.Add(24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, v := range top {
		ids = append(ids, v.MsgID)
	}
	want := []int{2, 1, 3}
	if len(ids) != len(want) || ids[0] != 2 || ids[1] != 1 || ids[2] != 3 {
		t.Fatalf("order = %v, want %v", ids, want)
	}
	if top[0].Likes != 2 || top[1].Likes != 1 {
		t.Fatalf("likes = %d, %d", top[0].Likes, top[1].Likes)
	}

	limited, _ := s.TopVideos(100, base, base.Add(24*time.Hour), 1)
	if len(limited) != 1 || limited[0].MsgID != 2 {
		t.Fatalf("limit: %+v", limited)
	}

	// Upper bound is exclusive.
	exclusive, _ := s.TopVideos(100, base, base.Add(time.Hour), 10)
	if len(exclusive) != 1 || exclusive[0].MsgID != 1 {
		t.Fatalf("exclusive upper bound: %+v", exclusive)
	}
}

func TestChatsAndDigests(t *testing.T) {
	s := newTestStore(t)
	addVideo(t, s, 100, 1, 1, base)
	addVideo(t, s, 100, 2, 1, base)
	addVideo(t, s, 200, 3, 1, base)
	chats, err := s.Chats()
	if err != nil || len(chats) != 2 {
		t.Fatalf("chats: %+v %v", chats, err)
	}

	if _, ok, err := s.LastDigest(100); ok || err != nil {
		t.Fatalf("expected no digest yet: %v %v", ok, err)
	}
	if err := s.SetLastDigest(100, base); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastDigest(100, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	at, ok, err := s.LastDigest(100)
	if !ok || err != nil || !at.Equal(base.Add(time.Hour)) {
		t.Fatalf("LastDigest = %v %v %v", at, ok, err)
	}
}

func TestUpdateFileRef(t *testing.T) {
	s := newTestStore(t)
	addVideo(t, s, 100, 1, 1, base)
	if err := s.UpdateFileRef(100, 1, []byte{9}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.GetVideo(100, 1)
	if string(v.FileRef) != "\x09" {
		t.Fatalf("file ref = %v", v.FileRef)
	}
}

func TestChatInputPeer(t *testing.T) {
	if p, ok := (Chat{ID: 5, Type: ChatTypeChat}).InputPeer().(*tg.InputPeerChat); !ok || p.ChatID != 5 {
		t.Fatalf("chat peer: %#v", p)
	}
	p, ok := (Chat{ID: 6, Type: ChatTypeChannel, Hash: 9}).InputPeer().(*tg.InputPeerChannel)
	if !ok || p.ChannelID != 6 || p.AccessHash != 9 {
		t.Fatalf("channel peer: %#v", p)
	}
}

func TestOpenStorePragmas(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mode string
	var timeout int
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("busy_timeout = %d, %v", timeout, err)
	}
}
