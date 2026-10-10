package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"golang.org/x/exp/slices"
)

const likeCallbackData = "like"

// likeStore is nil when likes are disabled.
var likeStore *Store

func likeMarkup(count int) tg.ReplyMarkupClass {
	return &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonCallback{
			Text: T("like_button", map[string]any{"Count": count}),
			Data: []byte(likeCallbackData),
		},
	}}}}
}

// displayUserName returns "@username", or the first name when the user has no
// username, or "" when the user is unknown.
func displayUserName(entities tg.Entities, userID int64) string {
	user, ok := entities.Users[userID]
	if !ok {
		return ""
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	return user.FirstName
}

// extractSentMessage digs our new or edited message out of the Updates
// returned by a send/edit request.
func extractSentMessage(u tg.UpdatesClass) *tg.Message {
	var updates []tg.UpdateClass
	switch v := u.(type) {
	case *tg.Updates:
		updates = v.Updates
	case *tg.UpdatesCombined:
		updates = v.Updates
	default:
		return nil
	}
	for _, upd := range updates {
		var msg tg.MessageClass
		switch v := upd.(type) {
		case *tg.UpdateNewMessage:
			msg = v.Message
		case *tg.UpdateNewChannelMessage:
			msg = v.Message
		case *tg.UpdateEditMessage:
			msg = v.Message
		case *tg.UpdateEditChannelMessage:
			msg = v.Message
		default:
			continue
		}
		if m, ok := msg.(*tg.Message); ok {
			return m
		}
	}
	return nil
}

// origMsgID returns the ID of the user's message with the link. qEntry.OrigMsg
// can't be used: for context-based requests it's a stand-in without an ID.
func origMsgID(qEntry *DownloadQueueEntry) int {
	var msg tg.MessageClass
	switch {
	case qEntry.OrigMsgUpdate != nil:
		msg = qEntry.OrigMsgUpdate.Message
	case qEntry.OrigChannelMsgUpdate != nil:
		msg = qEntry.OrigChannelMsgUpdate.Message
	}
	if m, ok := msg.(*tg.Message); ok {
		return m.ID
	}
	return 0
}

func extractDocument(msg *tg.Message) *tg.Document {
	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return nil
	}
	return doc
}

// videoFromUpload builds the store record for an uploaded message. Returns
// ok=false when the upload was not made in a group or has no document.
func videoFromUpload(qEntry *DownloadQueueEntry, msg *tg.Message, format, title string) (Video, bool) {
	doc := extractDocument(msg)
	if doc == nil {
		return Video{}, false
	}
	v := Video{
		MsgID:     msg.ID,
		OrigMsgID: origMsgID(qEntry),
		UserID:    qEntry.FromUser.UserID,
		UserName:  qEntry.FromUserName,
		Title:     title,
		URL:       qEntry.URL,
		IsAudio:   format == "mp3",
		DocID:     doc.ID,
		DocHash:   doc.AccessHash,
		FileRef:   doc.FileReference,
		PostedAt:  time.Unix(int64(msg.Date), 0),
	}
	switch p := qEntry.ChatPeer.(type) {
	case *tg.InputPeerChat:
		v.ChatID, v.ChatType = p.ChatID, ChatTypeChat
	case *tg.InputPeerChannel:
		v.ChatID, v.ChatType, v.ChatHash = p.ChannelID, ChatTypeChannel, p.AccessHash
	default:
		return Video{}, false
	}
	return v, true
}

// recordUpload stores an uploaded video so it can be liked and ranked.
func recordUpload(qEntry *DownloadQueueEntry, sent tg.UpdatesClass, format, title string) {
	if likeStore == nil || qEntry.ChatPeer == nil {
		return
	}
	msg := extractSentMessage(sent)
	if msg == nil {
		fmt.Println("  likes: uploaded message not found in response, not recording")
		return
	}
	v, ok := videoFromUpload(qEntry, msg, format, title)
	if !ok {
		fmt.Println("  likes: uploaded message has no document, not recording")
		return
	}
	if err := likeStore.AddVideo(v); err != nil {
		fmt.Println("  likes: error storing video:", err)
	}
}

// likeLocks is a fixed set of locks shared by messages through hashing, which
// keeps memory bounded without tracking every liked message.
var likeLocks [64]sync.Mutex

func likeLock(chatID int64, msgID int) *sync.Mutex {
	h := uint64(chatID)*31 + uint64(msgID)
	return &likeLocks[h%uint64(len(likeLocks))]
}

func callbackChatID(peer tg.PeerClass) (int64, bool) {
	switch p := peer.(type) {
	case *tg.PeerChat:
		return p.ChatID, true
	case *tg.PeerChannel:
		return p.ChannelID, true
	default:
		return 0, false
	}
}

func handleBotCallbackQuery(ctx context.Context, entities tg.Entities, u *tg.UpdateBotCallbackQuery) error {
	answer := func(text string) {
		_, err := telegramAPI.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
			QueryID: u.QueryID,
			Message: text,
		})
		if err != nil {
			fmt.Println("  likes: error answering callback:", err)
		}
	}

	chatID, ok := callbackChatID(u.Peer)
	if likeStore == nil || string(u.Data) != likeCallbackData || !ok || !slices.Contains(params.AllowedGroupIDs, -chatID) {
		answer("")
		return nil
	}

	v, err := likeStore.GetVideo(chatID, u.MsgID)
	if err != nil {
		fmt.Println("  likes: error loading video:", err)
		answer("")
		return nil
	}
	if v == nil {
		answer("")
		return nil
	}
	if v.UserID == u.UserID {
		answer(T("like_self", nil))
		return nil
	}

	// Taps on the same message are handled one at a time, so button edits
	// can't land out of order and show an older count.
	lock := likeLock(chatID, u.MsgID)
	lock.Lock()
	defer lock.Unlock()

	liked, count, err := likeStore.ToggleLike(chatID, u.MsgID, u.UserID)
	if err != nil {
		fmt.Println("  likes: error toggling like:", err)
		answer("")
		return nil
	}
	fmt.Printf("  likes: user #%d %s video %d in chat %d (%d likes)\n", u.UserID, map[bool]string{true: "liked", false: "unliked"}[liked], u.MsgID, chatID, count)

	if liked {
		answer(T("liked", nil))
	} else {
		answer(T("unliked", nil))
	}

	chat := Chat{ID: v.ChatID, Type: v.ChatType, Hash: v.ChatHash}
	_, err = telegramAPI.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
		Peer:        chat.InputPeer(),
		ID:          u.MsgID,
		ReplyMarkup: likeMarkup(count),
	})
	if err != nil {
		// The stored count is authoritative; the next tap fixes the label.
		fmt.Println("  likes: error updating like button:", err)
	}
	return nil
}
