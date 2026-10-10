package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeSender struct {
	sent    []string // captions
	failFor map[int]error
}

func (f *fakeSender) send(ctx context.Context, chat Chat, v RankedVideo, caption string) error {
	if err := f.failFor[v.MsgID]; err != nil {
		return err
	}
	f.sent = append(f.sent, caption)
	return nil
}

func TestBestOfCaption(t *testing.T) {
	v := RankedVideo{Video: Video{ChatID: 123, ChatType: ChatTypeChannel, MsgID: 9, UserName: "@poster", Title: "Cat"}, Likes: 4}
	got := bestOfCaption(IntervalWeek, 1, v)
	want := "🏆 Best of the week #1 · 4 ❤️ · @poster\nhttps://t.me/c/123/9"
	if got != want {
		t.Fatalf("caption = %q, want %q", got, want)
	}
	v.ChatType = ChatTypeChat
	if got := bestOfCaption(IntervalMonth, 2, v); got != "🏆 Best of the month #2 · 4 ❤️ · @poster" {
		t.Fatalf("basic group caption = %q", got)
	}
}

func TestPostDigest(t *testing.T) {
	s := newTestStore(t)
	sched := mustSchedule(t, "week", "sun 20:00")
	occ := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	in := occ.Add(-24 * time.Hour)
	addVideo(t, s, 100, 1, 1, in)
	addVideo(t, s, 100, 2, 1, in)
	addVideo(t, s, 100, 3, 1, in)
	addVideo(t, s, 100, 4, 1, occ.AddDate(0, 0, -8)) // previous week
	for _, l := range [][2]int64{{1, 2}, {2, 2}, {2, 3}, {3, 2}, {4, 2}, {4, 3}, {4, 4}} {
		s.ToggleLike(100, int(l[0]), l[1])
	}

	f := &fakeSender{failFor: map[int]error{1: errVideoGone}}
	err := postDigest(context.Background(), s, sched, 2, Chat{ID: 100, Type: ChatTypeChannel}, occ, noReactions, f.send)
	if err != nil {
		t.Fatal(err)
	}
	// Top 2 are msg 2 (2 likes) and msg 1 (1 like, earlier than 3); msg 1 is gone and is skipped, not replaced.
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "#1 · 2 ❤️") {
		t.Fatalf("sent = %q", f.sent)
	}
	at, ok, _ := s.LastDigest(100)
	if !ok || !at.Equal(occ) {
		t.Fatalf("last digest = %v %v", at, ok)
	}
}

func TestPostDigestErrorStillRecords(t *testing.T) {
	s := newTestStore(t)
	sched := mustSchedule(t, "day", "20:00")
	occ := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	addVideo(t, s, 100, 1, 1, occ.Add(-time.Hour))
	s.ToggleLike(100, 1, 2)

	f := &fakeSender{failFor: map[int]error{1: errors.New("CHAT_WRITE_FORBIDDEN")}}
	err := postDigest(context.Background(), s, sched, 3, Chat{ID: 100, Type: ChatTypeChannel}, occ, noReactions, f.send)
	if err == nil {
		t.Fatal("expected error")
	}
	if at, ok, _ := s.LastDigest(100); !ok || !at.Equal(occ) {
		t.Fatalf("digest must be recorded even on failure: %v %v", at, ok)
	}
}

func TestPostDigestNothingLiked(t *testing.T) {
	s := newTestStore(t)
	sched := mustSchedule(t, "day", "20:00")
	occ := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	addVideo(t, s, 100, 1, 1, occ.Add(-time.Hour))
	f := &fakeSender{}
	if err := postDigest(context.Background(), s, sched, 3, Chat{ID: 100}, occ, noReactions, f.send); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent = %q", f.sent)
	}
}

func TestDueOccurrences(t *testing.T) {
	sched := mustSchedule(t, "day", "20:00")
	last := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 20, 0, 0, 0, time.UTC) }

	if got := dueOccurrences(sched, last, day(1).Add(time.Hour), 3); len(got) != 0 {
		t.Fatalf("nothing due yet, got %v", got)
	}
	if got := dueOccurrences(sched, last, day(2), 3); len(got) != 1 || !got[0].Equal(day(2)) {
		t.Fatalf("occurrence at now must be due, got %v", got)
	}
	got := dueOccurrences(sched, last, day(10).Add(time.Hour), 3)
	if len(got) != 3 || !got[0].Equal(day(8)) || !got[2].Equal(day(10)) {
		t.Fatalf("expected the 3 latest missed occurrences, got %v", got)
	}
}

func TestInitDigests(t *testing.T) {
	s := newTestStore(t)
	sched := mustSchedule(t, "day", "20:00")
	now := time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	addVideo(t, s, 100, 1, 1, now.Add(-2*time.Hour))
	addVideo(t, s, 200, 2, 1, now.Add(-2*time.Hour))
	old := now.AddDate(0, 0, -5)
	s.SetLastDigest(200, old)

	initDigests(s, sched, now)

	if at, ok, _ := s.LastDigest(100); !ok || !at.Equal(sched.Prev(now)) {
		t.Fatalf("new chat must be initialized to the latest occurrence, got %v %v", at, ok)
	}
	if at, _, _ := s.LastDigest(200); !at.Equal(old) {
		t.Fatalf("existing digest must be kept, got %v", at)
	}
}

func TestRunDueDigests(t *testing.T) {
	s := newTestStore(t)
	sched := mustSchedule(t, "day", "20:00")
	day := func(d int) time.Time { return time.Date(2026, 10, d, 20, 0, 0, 0, time.UTC) }
	now := day(11).Add(time.Second)

	// Chat 100 missed the digests of the 10th and the 11th.
	addVideo(t, s, 100, 1, 1, day(10).Add(-time.Hour))
	addVideo(t, s, 100, 2, 1, day(11).Add(-time.Hour))
	s.SetLastDigest(100, day(9))
	// Chat 200 is up to date.
	addVideo(t, s, 200, 3, 1, day(11).Add(-time.Hour))
	s.SetLastDigest(200, day(11))
	// Chat 300 got its first video after startup, so it has no digest row.
	addVideo(t, s, 300, 4, 1, day(11).Add(-time.Hour))
	for _, l := range [][2]int64{{100, 1}, {100, 2}, {200, 3}, {300, 4}} {
		s.ToggleLike(l[0], int(l[1]), 2)
	}

	f := &fakeSender{}
	runDueDigests(context.Background(), s, sched, 3, now, noReactions, f.send)

	if len(f.sent) != 3 {
		t.Fatalf("expected 2 catch-up digests for chat 100 and 1 for chat 300, sent = %q", f.sent)
	}
	for _, chat := range []int64{100, 200, 300} {
		if at, ok, _ := s.LastDigest(chat); !ok || !at.Equal(day(11)) {
			t.Fatalf("chat %d last digest = %v %v, want %v", chat, at, ok, day(11))
		}
	}
}

func noReactions(ctx context.Context, chat Chat, ids []int) (map[int]int, error) {
	return nil, nil
}

func TestRankVideos(t *testing.T) {
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	videos := []RankedVideo{
		{Video: Video{MsgID: 1, PostedAt: at(0)}, Likes: 1},               // 1
		{Video: Video{MsgID: 2, PostedAt: at(1)}, Likes: 0, Reactions: 4}, // 4
		{Video: Video{MsgID: 3, PostedAt: at(2)}},                         // 0 → left out
		{Video: Video{MsgID: 4, PostedAt: at(3)}, Likes: 2, Reactions: 2}, // 4, later than #2
		{Video: Video{MsgID: 5, PostedAt: at(4)}, Reactions: 1},           // 1, later than #1
	}
	got := rankVideos(videos, 10)
	var ids []int
	for _, v := range got {
		ids = append(ids, v.MsgID)
	}
	want := []int{2, 4, 1, 5}
	if len(ids) != len(want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
	if got := rankVideos(videos, 2); len(got) != 2 || got[1].MsgID != 4 {
		t.Fatalf("limit: %+v", got)
	}
}

func TestBestOfCaptionWithReactions(t *testing.T) {
	v := RankedVideo{Video: Video{ChatType: ChatTypeChat, UserName: "@poster"}, Likes: 4, Reactions: 7}
	if got, want := bestOfCaption(IntervalWeek, 1, v), "🏆 Best of the week #1 · 4 ❤️ · 7 reactions · @poster"; got != want {
		t.Fatalf("caption = %q, want %q", got, want)
	}
	v.Likes, v.Reactions = 0, 1
	if got, want := bestOfCaption(IntervalWeek, 1, v), "🏆 Best of the week #1 · 1 reaction · @poster"; got != want {
		t.Fatalf("caption = %q, want %q", got, want)
	}
}

func TestPostDigestUsesReactions(t *testing.T) {
	s := newTestStore(t)
	sched := mustSchedule(t, "day", "20:00")
	occ := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	addVideo(t, s, 100, 1, 1, occ.Add(-2*time.Hour)) // 1 like
	addVideo(t, s, 100, 2, 1, occ.Add(-time.Hour))   // no likes, 3 reactions on the original post
	s.ToggleLike(100, 1, 2)
	fetch := func(ctx context.Context, chat Chat, ids []int) (map[int]int, error) {
		return map[int]int{1002: 3}, nil // addVideo sets OrigMsgID = msgID + 1000
	}
	f := &fakeSender{}
	if err := postDigest(context.Background(), s, sched, 3, Chat{ID: 100}, occ, fetch, f.send); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || !strings.Contains(f.sent[0], "#1 · 3 reactions") || !strings.Contains(f.sent[1], "#2 · 1 ❤️") {
		t.Fatalf("sent = %q", f.sent)
	}
}
