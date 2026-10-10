package main

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

// reactionsFetchFunc returns the total reactions of each given message of chat.
type reactionsFetchFunc func(ctx context.Context, chat Chat, msgIDs []int) (map[int]int, error)

// maxMessagesPerRequest is the most messages Telegram returns per getMessages call.
const maxMessagesPerRequest = 100

// getChatMessages fetches messages of a chat by ID.
func getChatMessages(ctx context.Context, chat Chat, msgIDs []int) ([]tg.MessageClass, error) {
	ids := make([]tg.InputMessageClass, len(msgIDs))
	for i, id := range msgIDs {
		ids[i] = &tg.InputMessageID{ID: id}
	}
	var res tg.MessagesMessagesClass
	err := withFloodRetry(ctx, "fetching messages", func() (err error) {
		if chat.Type == ChatTypeChannel {
			res, err = telegramAPI.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: &tg.InputChannel{ChannelID: chat.ID, AccessHash: chat.Hash},
				ID:      ids,
			})
		} else {
			res, err = telegramAPI.MessagesGetMessages(ctx, ids)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	switch m := res.(type) {
	case *tg.MessagesMessages:
		return m.Messages, nil
	case *tg.MessagesMessagesSlice:
		return m.Messages, nil
	case *tg.MessagesChannelMessages:
		return m.Messages, nil
	default:
		return nil, nil
	}
}

func messageReactionCount(msg *tg.Message) int {
	total := 0
	for _, r := range msg.Reactions.Results {
		total += r.Count
	}
	return total
}

// fetchReactions returns the total reactions of each given message. Messages
// that no longer exist are left out. On error, the counts fetched so far are
// returned along with it.
func fetchReactions(ctx context.Context, chat Chat, msgIDs []int) (map[int]int, error) {
	res := make(map[int]int, len(msgIDs))
	for start := 0; start < len(msgIDs); start += maxMessagesPerRequest {
		end := min(start+maxMessagesPerRequest, len(msgIDs))
		msgs, err := getChatMessages(ctx, chat, msgIDs[start:end])
		if err != nil {
			return res, err
		}
		for _, mc := range msgs {
			if m, ok := mc.(*tg.Message); ok {
				res[m.ID] = messageReactionCount(m)
			}
		}
	}
	return res, nil
}

// addReactions sets each video's reactions to those on the user's original
// post plus those on the bot's video message. If fetching fails, the digest
// is ranked on whatever was fetched (likes only, at worst).
func addReactions(ctx context.Context, chat Chat, videos []RankedVideo, fetch reactionsFetchFunc) {
	var ids []int
	for _, v := range videos {
		ids = append(ids, v.MsgID)
		if v.OrigMsgID != 0 {
			ids = append(ids, v.OrigMsgID)
		}
	}
	if len(ids) == 0 {
		return
	}
	counts, err := fetch(ctx, chat, ids)
	if err != nil {
		fmt.Println("  best-of: error fetching reactions for chat", chat.ID, "(ranking on what was fetched):", err)
	}
	for i := range videos {
		videos[i].Reactions = counts[videos[i].MsgID]
		if videos[i].OrigMsgID != 0 {
			videos[i].Reactions += counts[videos[i].OrigMsgID]
		}
	}
}
