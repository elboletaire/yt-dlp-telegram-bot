package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// EphemeralProgress tracks a per-user ephemeral status message in a group or
// supergroup chat. Ephemeral messages (Bot API 10.2 / layer 228) are visible
// only to the receiver, expire on their own and never clutter the chat for
// other members, so no manual cleanup dance is needed.
type EphemeralProgress struct {
	Peer     tg.InputPeerClass
	Receiver tg.InputUserClass

	mutex sync.Mutex
	msgID int
}

// ephemeralEditMessageRequest is a hand-written request for
// ephemeral.editMessage#13f250ee, which is present in the current Telegram
// schema but was added after the layer 228 snapshot bundled with gotd
// v0.161.0. Remove once gotd generates tg.Client.EphemeralEditMessage.
//
// ephemeral.editMessage#13f250ee flags:# peer:InputPeer receiver_id:InputUser
// id:int message:flags.0?string media:flags.3?InputMedia
// entities:flags.1?Vector<MessageEntity> reply_markup:flags.2?ReplyMarkup
// = Updates;
type ephemeralEditMessageRequest struct {
	Peer       tg.InputPeerClass
	ReceiverID tg.InputUserClass
	ID         int
	Message    string
	Entities   []tg.MessageEntityClass
}

const ephemeralEditMessageRequestTypeID = 0x13f250ee

func (r *ephemeralEditMessageRequest) Encode(b *bin.Buffer) error {
	b.PutID(ephemeralEditMessageRequestTypeID)
	var flags bin.Fields
	flags.Set(0) // message
	if r.Entities != nil {
		flags.Set(1)
	}
	if err := flags.Encode(b); err != nil {
		return err
	}
	if r.Peer == nil {
		return fmt.Errorf("ephemeral.editMessage: nil peer")
	}
	if err := r.Peer.Encode(b); err != nil {
		return err
	}
	if r.ReceiverID == nil {
		return fmt.Errorf("ephemeral.editMessage: nil receiver")
	}
	if err := r.ReceiverID.Encode(b); err != nil {
		return err
	}
	b.PutInt(r.ID)
	b.PutString(r.Message)
	if r.Entities != nil {
		b.PutVectorHeader(len(r.Entities))
		for _, e := range r.Entities {
			if err := e.Encode(b); err != nil {
				return err
			}
		}
	}
	return nil
}

func ephemeralRandomID() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

// extractEphemeralMsgID digs the ephemeral message ID out of the Updates
// returned by ephemeral.sendMessage.
func extractEphemeralMsgID(u tg.UpdatesClass) int {
	var updates []tg.UpdateClass
	switch v := u.(type) {
	case *tg.Updates:
		updates = v.Updates
	case *tg.UpdatesCombined:
		updates = v.Updates
	default:
		return 0
	}
	for _, upd := range updates {
		if e, ok := upd.(*tg.UpdateNewEphemeralMessage); ok {
			return e.Message.ID
		}
	}
	return 0
}

// sendEphemeral sends an ephemeral message to receiver in peer and returns a
// handle for later edits/deletion. The bot must be an admin of the chat to
// send ephemeral messages outside the 15 s callback window.
func sendEphemeral(ctx context.Context, peer tg.InputPeerClass, receiver tg.InputUserClass, text string) (*EphemeralProgress, error) {
	randomID, err := ephemeralRandomID()
	if err != nil {
		return nil, err
	}
	updates, err := telegramAPI.EphemeralSendMessage(ctx, &tg.EphemeralSendMessageRequest{
		Peer:       peer,
		ReceiverID: receiver,
		Message:    text,
		RandomID:   randomID,
	})
	if err != nil {
		return nil, err
	}
	msgID := extractEphemeralMsgID(updates)
	if msgID == 0 {
		return nil, fmt.Errorf("ephemeral message ID not found in response")
	}
	return &EphemeralProgress{Peer: peer, Receiver: receiver, msgID: msgID}, nil
}

func (p *EphemeralProgress) Edit(ctx context.Context, text string) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	req := &ephemeralEditMessageRequest{
		Peer:       p.Peer,
		ReceiverID: p.Receiver,
		ID:         p.msgID,
		Message:    text,
	}
	var res tg.UpdatesBox
	err := telegramClient.Invoke(ctx, req, &res)
	if err == nil {
		return nil
	}

	// The ephemeral message may have expired in the meantime (expiry is
	// controlled by Telegram); send a fresh one and keep updating that.
	fmt.Println("  ephemeral edit failed, sending a fresh ephemeral message:", err)
	fresh, sendErr := sendEphemeral(ctx, p.Peer, p.Receiver, text)
	if sendErr != nil {
		return err
	}
	p.msgID = fresh.msgID
	return nil
}

func (p *EphemeralProgress) Delete(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	_, err := telegramAPI.EphemeralDeleteMessage(ctx, &tg.EphemeralDeleteMessageRequest{
		Peer:       p.Peer,
		ReceiverID: p.Receiver,
		ID:         p.msgID,
	})
	return err
}

// replyEphemeralOrPublic sends text as an ephemeral message to the requester
// when possible, falling back to a public reply otherwise.
func replyEphemeralOrPublic(ctx context.Context, msgCtx *MessageContext, text string) {
	if peer, receiver, ok := msgCtx.ephemeralTarget(); ok {
		if _, err := sendEphemeral(ctx, peer, receiver, text); err == nil {
			return
		} else {
			fmt.Println("  ephemeral reply failed, falling back to public reply:", err)
		}
	}
	_, _ = msgCtx.ReplyBuilder().Text(ctx, text)
}

// ephemeralTarget resolves the chat peer and receiver user needed to send an
// ephemeral message for this context. Returns ok=false for direct messages
// (ephemeral messages only exist in groups/supergroups) or when the required
// access hashes are not present in the update entities.
func (msgCtx *MessageContext) ephemeralTarget() (peer tg.InputPeerClass, receiver tg.InputUserClass, ok bool) {
	var msg tg.MessageClass
	if msgCtx.OrigMsgUpdate != nil {
		msg = msgCtx.OrigMsgUpdate.Message
	} else if msgCtx.OrigChannelMsgUpdate != nil {
		msg = msgCtx.OrigChannelMsgUpdate.Message
	} else {
		return nil, nil, false
	}
	m, isMsg := msg.(*tg.Message)
	if !isMsg {
		return nil, nil, false
	}

	switch p := m.PeerID.(type) {
	case *tg.PeerChat:
		peer = &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		channel, found := msgCtx.Entities.Channels[p.ChannelID]
		if !found {
			return nil, nil, false
		}
		peer = &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	default:
		// Direct message: everything is private already, keep normal replies.
		return nil, nil, false
	}

	user, found := msgCtx.Entities.Users[msgCtx.FromUserID]
	if !found {
		return nil, nil, false
	}
	receiver = &tg.InputUser{UserID: user.ID, AccessHash: user.AccessHash}
	return peer, receiver, true
}
