package main

import (
	"database/sql"
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
		DocID: int64(msgID) * 10, DocHash: 7, FileRef: []byte{1, 2}, PostedAt: posted, OrigMsgID: msgID + 1000,
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
	if v.OrigMsgID != 1005 || v.UserID != 1 || v.DocID != 50 || string(v.FileRef) != "\x01\x02" || !v.PostedAt.Equal(base) || v.ChatType != ChatTypeChannel {
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

func TestVideosInWindow(t *testing.T) {
	s := newTestStore(t)
	addVideo(t, s, 100, 1, 1, base)                  // 1 like
	addVideo(t, s, 100, 2, 1, base.Add(time.Hour))   // 2 likes
	addVideo(t, s, 100, 3, 1, base.Add(-time.Hour))  // before window
	addVideo(t, s, 100, 4, 1, base.Add(2*time.Hour)) // 0 likes, still returned
	addVideo(t, s, 200, 5, 1, base)                  // other chat
	for _, l := range [][2]int64{{1, 2}, {2, 2}, {2, 3}, {3, 2}} {
		if _, _, err := s.ToggleLike(100, int(l[0]), l[1]); err != nil {
			t.Fatal(err)
		}
	}

	videos, err := s.VideosInWindow(100, base, base.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Upper bound is exclusive, so msg 4 (posted exactly at the bound) is left out.
	if len(videos) != 2 || videos[0].MsgID != 1 || videos[1].MsgID != 2 {
		t.Fatalf("videos = %+v", videos)
	}
	if videos[0].Likes != 1 || videos[1].Likes != 2 || videos[0].OrigMsgID != 1001 {
		t.Fatalf("likes/orig = %+v", videos)
	}

	all, _ := s.VideosInWindow(100, base, base.Add(24*time.Hour))
	if len(all) != 3 || all[2].MsgID != 4 || all[2].Likes != 0 {
		t.Fatalf("videos without likes must be included: %+v", all)
	}
}

func TestRankedVideoScore(t *testing.T) {
	if got := (RankedVideo{Likes: 2, Reactions: 5}).Score(); got != 7 {
		t.Fatalf("score = %d", got)
	}
}

func TestStoreMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")

	// A database created before the migration existed.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(storeSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO videos (chat_id, chat_type, chat_hash, msg_id, user_id, user_name, title, url, is_audio, doc_id, doc_hash, doc_file_ref, posted_at)
		VALUES (100, 'channel', 1, 5, 1, '@x', 't', 'u', 0, 1, 1, x'01', 0)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	for i := 0; i < 2; i++ { // reopening must be idempotent
		s, err := OpenStore(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		v, err := s.GetVideo(100, 5)
		if err != nil || v == nil || v.OrigMsgID != 0 {
			t.Fatalf("old row after migration: %+v %v", v, err)
		}
		var version int
		s.db.QueryRow("PRAGMA user_version").Scan(&version)
		if version != len(storeMigrations) {
			t.Fatalf("user_version = %d", version)
		}
		s.Close()
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
