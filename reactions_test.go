package main

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

func TestMessageReactionCount(t *testing.T) {
	msg := &tg.Message{Reactions: tg.MessageReactions{Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 3},
		{Reaction: &tg.ReactionEmoji{Emoticon: "😂"}, Count: 2},
	}}}
	if got := messageReactionCount(msg); got != 5 {
		t.Fatalf("got %d", got)
	}
	if got := messageReactionCount(&tg.Message{}); got != 0 {
		t.Fatalf("no reactions: got %d", got)
	}
}

func TestAddReactions(t *testing.T) {
	videos := []RankedVideo{
		{Video: Video{MsgID: 10, OrigMsgID: 9}, Likes: 1},
		{Video: Video{MsgID: 20}}, // recorded before original IDs were stored
	}
	var asked []int
	fetch := func(ctx context.Context, chat Chat, ids []int) (map[int]int, error) {
		asked = ids
		return map[int]int{9: 4, 10: 1, 20: 2}, nil
	}
	addReactions(context.Background(), Chat{ID: 1}, videos, fetch)
	if len(asked) != 3 {
		t.Fatalf("asked for %v", asked)
	}
	if videos[0].Reactions != 5 || videos[1].Reactions != 2 {
		t.Fatalf("reactions = %d, %d", videos[0].Reactions, videos[1].Reactions)
	}
}

func TestAddReactionsFetchError(t *testing.T) {
	videos := []RankedVideo{{Video: Video{MsgID: 10, OrigMsgID: 9}, Likes: 1}}
	fetch := func(ctx context.Context, chat Chat, ids []int) (map[int]int, error) {
		return map[int]int{9: 3}, errors.New("CHANNEL_PRIVATE")
	}
	addReactions(context.Background(), Chat{ID: 1}, videos, fetch)
	// Counts fetched before the error are still used.
	if videos[0].Reactions != 3 {
		t.Fatalf("reactions = %d", videos[0].Reactions)
	}
}
