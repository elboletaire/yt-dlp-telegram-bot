package main

import (
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestLikeMarkup(t *testing.T) {
	m, ok := likeMarkup(5).(*tg.ReplyInlineMarkup)
	if !ok || len(m.Rows) != 1 || len(m.Rows[0].Buttons) != 1 {
		t.Fatalf("unexpected markup: %#v", m)
	}
	b, ok := m.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok || b.Text != "❤️ 5" || string(b.Data) != likeCallbackData {
		t.Fatalf("unexpected button: %#v", b)
	}
}

func TestExtractSentMessage(t *testing.T) {
	doc := &tg.Document{ID: 1, AccessHash: 2, FileReference: []byte{3}}
	msg := &tg.Message{ID: 77, Media: &tg.MessageMediaDocument{Document: doc}}
	cases := []tg.UpdatesClass{
		&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: 77}, &tg.UpdateNewChannelMessage{Message: msg}}},
		&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: msg}}},
		&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateEditChannelMessage{Message: msg}}},
		&tg.UpdatesCombined{Updates: []tg.UpdateClass{&tg.UpdateEditMessage{Message: msg}}},
	}
	for i, c := range cases {
		got := extractSentMessage(c)
		if got == nil || got.ID != 77 {
			t.Fatalf("case %d: got %#v", i, got)
		}
		d := extractDocument(got)
		if d == nil || d.ID != 1 || d.AccessHash != 2 {
			t.Fatalf("case %d: document %#v", i, d)
		}
	}
	if extractSentMessage(&tg.UpdateShortSentMessage{ID: 1}) != nil {
		t.Fatal("expected nil for short sent message")
	}
	if extractDocument(&tg.Message{ID: 1}) != nil {
		t.Fatal("expected nil document for message without media")
	}
}

func TestVideoFromUpload(t *testing.T) {
	doc := &tg.Document{ID: 1, AccessHash: 2, FileReference: []byte{3}}
	msg := &tg.Message{ID: 77, Date: 1760000000, Media: &tg.MessageMediaDocument{Document: doc}}
	entry := &DownloadQueueEntry{
		URL:                  "https://example.com/v",
		FromUser:             &tg.PeerUser{UserID: 10},
		FromUserName:         "@poster",
		OrigChannelMsgUpdate: &tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 70}},
		ChatPeer:             &tg.InputPeerChannel{ChannelID: 500, AccessHash: 600},
	}
	v, ok := videoFromUpload(entry, msg, "mp4", "Title")
	if !ok {
		t.Fatal("expected ok")
	}
	want := Video{
		ChatID: 500, ChatType: ChatTypeChannel, ChatHash: 600, MsgID: 77, UserID: 10, UserName: "@poster",
		Title: "Title", URL: "https://example.com/v", IsAudio: false, DocID: 1, DocHash: 2, FileRef: []byte{3},
		PostedAt: time.Unix(1760000000, 0),
	}
	if v.OrigMsgID != 70 {
		t.Fatalf("orig msg id = %d", v.OrigMsgID)
	}
	if v.ChatID != want.ChatID || v.ChatType != want.ChatType || v.ChatHash != want.ChatHash || v.MsgID != want.MsgID ||
		v.UserID != want.UserID || v.UserName != want.UserName || v.Title != want.Title || v.URL != want.URL ||
		v.IsAudio != want.IsAudio || v.DocID != want.DocID || v.DocHash != want.DocHash || !v.PostedAt.Equal(want.PostedAt) {
		t.Fatalf("got %+v", v)
	}

	entry.ChatPeer = &tg.InputPeerChat{ChatID: 42}
	v, ok = videoFromUpload(entry, msg, "mp3", "Song")
	if !ok || v.ChatType != ChatTypeChat || v.ChatID != 42 || v.ChatHash != 0 || !v.IsAudio {
		t.Fatalf("basic group: %+v %v", v, ok)
	}

	entry.ChatPeer = nil
	if _, ok := videoFromUpload(entry, msg, "mp4", "x"); ok {
		t.Fatal("expected !ok without chat peer")
	}
}

func TestCallbackChatID(t *testing.T) {
	if id, ok := callbackChatID(&tg.PeerChat{ChatID: 5}); !ok || id != 5 {
		t.Fatalf("chat: %d %v", id, ok)
	}
	if id, ok := callbackChatID(&tg.PeerChannel{ChannelID: 6}); !ok || id != 6 {
		t.Fatalf("channel: %d %v", id, ok)
	}
	if _, ok := callbackChatID(&tg.PeerUser{UserID: 1}); ok {
		t.Fatal("user peer must not be ok")
	}
}

func TestDisplayUserName(t *testing.T) {
	e := tg.Entities{Users: map[int64]*tg.User{
		1: {ID: 1, Username: "nick", FirstName: "Nick"},
		2: {ID: 2, FirstName: "Anna"},
	}}
	if got := displayUserName(e, 1); got != "@nick" {
		t.Errorf("got %q", got)
	}
	if got := displayUserName(e, 2); got != "Anna" {
		t.Errorf("got %q", got)
	}
	if got := displayUserName(e, 3); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestLikeLock(t *testing.T) {
	if likeLock(100, 5) != likeLock(100, 5) {
		t.Fatal("same message must map to the same lock")
	}
	if likeLock(100, 5) == likeLock(100, 6) {
		t.Fatal("neighbouring messages should not share a lock")
	}
}

func TestOrigMsgID(t *testing.T) {
	if got := origMsgID(&DownloadQueueEntry{OrigMsgUpdate: &tg.UpdateNewMessage{Message: &tg.Message{ID: 5}}}); got != 5 {
		t.Fatalf("got %d", got)
	}
	if got := origMsgID(&DownloadQueueEntry{OrigChannelMsgUpdate: &tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 6}}}); got != 6 {
		t.Fatalf("got %d", got)
	}
	if got := origMsgID(&DownloadQueueEntry{}); got != 0 {
		t.Fatalf("got %d", got)
	}
}
