package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// errVideoGone means the original message or its media no longer exists.
var errVideoGone = errors.New("original video is gone")

type bestOfSendFunc func(ctx context.Context, chat Chat, v RankedVideo, caption string) error

func bestOfCaption(interval Interval, rank int, v RankedVideo) string {
	link := ""
	if v.ChatType == ChatTypeChannel {
		link = fmt.Sprintf("https://t.me/c/%d/%d", v.ChatID, v.MsgID)
	}
	return T("best_of_caption", map[string]any{
		"Interval": T("interval_"+string(interval), nil),
		"Rank":     rank,
		"Likes":    v.Likes,
		"User":     v.UserName,
		"Title":    v.Title,
		"Link":     link,
	})
}

// postDigest reposts the top videos of [sched.Prev(occ), occ) in chat. The
// digest is recorded as done even when sending fails, so a broken chat
// (e.g. bot removed) doesn't retry forever.
func postDigest(ctx context.Context, store *Store, sched Schedule, count int, chat Chat, occ time.Time, send bestOfSendFunc) error {
	top, err := store.TopVideos(chat.ID, sched.Prev(occ), occ, count)
	if err != nil {
		return fmt.Errorf("loading top videos: %w", err)
	}

	var sendErr error
	rank := 0
	for _, v := range top {
		err := send(ctx, chat, v, bestOfCaption(sched.Interval, rank+1, v))
		if errors.Is(err, errVideoGone) {
			fmt.Println("  best-of: skipping deleted video", v.MsgID, "in chat", chat.ID)
			continue
		}
		if err != nil {
			sendErr = err
			break
		}
		rank++
	}

	if err := store.SetLastDigest(chat.ID, occ); err != nil {
		return errors.Join(sendErr, fmt.Errorf("recording digest: %w", err))
	}
	return sendErr
}

// maxCatchUpDigests caps how many missed digests are posted per chat after a
// long downtime, so the chat isn't flooded with reposts.
const maxCatchUpDigests = 3

// dueOccurrences returns the occurrences after last and up to now, keeping only
// the latest max ones.
func dueOccurrences(sched Schedule, last, now time.Time, max int) []time.Time {
	var res []time.Time
	for occ := sched.Next(last); !occ.After(now); occ = sched.Next(occ) {
		res = append(res, occ)
		if len(res) > max {
			res = res[1:]
		}
	}
	return res
}

// initDigests marks the latest occurrence as done for chats without any digest
// yet, so enabling the feature doesn't trigger an instant digest.
func initDigests(store *Store, sched Schedule, now time.Time) {
	chats, err := store.Chats()
	if err != nil {
		fmt.Println("best-of: error listing chats:", err)
		return
	}
	for _, chat := range chats {
		if _, ok, err := store.LastDigest(chat.ID); err != nil {
			fmt.Println("best-of: error reading last digest:", err)
		} else if !ok {
			if err := store.SetLastDigest(chat.ID, sched.Prev(now)); err != nil {
				fmt.Println("best-of: error initializing digest:", err)
			}
		}
	}
}

// runDueDigests posts, for every chat, the digests that became due since its
// last one, oldest first.
func runDueDigests(ctx context.Context, store *Store, sched Schedule, count int, now time.Time, send bestOfSendFunc) {
	chats, err := store.Chats()
	if err != nil {
		fmt.Println("best-of: error listing chats:", err)
		return
	}
	for _, chat := range chats {
		last, ok, err := store.LastDigest(chat.ID)
		if err != nil {
			fmt.Println("best-of: error reading last digest:", err)
			continue
		}
		if !ok {
			// The chat got its first video after startup: only its latest
			// digest is due.
			last = sched.Prev(sched.Prev(now))
		}
		for _, occ := range dueOccurrences(sched, last, now, maxCatchUpDigests) {
			fmt.Println("best-of: posting digest of", occ, "for chat", chat.ID)
			if err := postDigest(ctx, store, sched, count, chat, occ, send); err != nil {
				fmt.Println("  best-of error:", err)
				sendTextToAdmins(ctx, T("best_of_failed", map[string]any{
					"Interval": T("interval_"+string(sched.Interval), nil),
					"Chat":     chat.ID,
					"Err":      err,
				}))
			}
		}
	}
}

// runBestOf posts missed digests, then each scheduled digest until ctx is done.
func runBestOf(ctx context.Context, store *Store, sched Schedule, count int) {
	initDigests(store, sched, time.Now())
	for {
		// The next wake-up is based on when this run started, so an occurrence
		// passing while digests are being sent is picked up right away.
		now := time.Now()
		runDueDigests(ctx, store, sched, count, now, sendBestOfVideo)
		next := sched.Next(now)
		fmt.Println("best-of: next digest at", next)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// sendBestOfVideo resends a stored video with the given caption, refreshing
// the file reference once if Telegram reports it expired.
func sendBestOfVideo(ctx context.Context, chat Chat, v RankedVideo, caption string) error {
	fileRef := v.FileRef
	for attempt := 0; ; attempt++ {
		err := withFloodRetry(ctx, "best-of send", func() error {
			id, err := ephemeralRandomID()
			if err != nil {
				return err
			}
			_, err = telegramAPI.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
				Peer: chat.InputPeer(),
				Media: &tg.InputMediaDocument{ID: &tg.InputDocument{
					ID:            v.DocID,
					AccessHash:    v.DocHash,
					FileReference: fileRef,
				}},
				Message:  caption,
				RandomID: id,
			})
			return err
		})
		if err == nil {
			return nil
		}
		if attempt > 0 || !tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID") {
			return err
		}
		fileRef, err = refreshFileRef(ctx, chat, v.Video)
		if err != nil {
			return err
		}
	}
}

// refreshFileRef fetches the original message again to get a fresh file
// reference and stores it.
func refreshFileRef(ctx context.Context, chat Chat, v Video) ([]byte, error) {
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: v.MsgID}}
	var res tg.MessagesMessagesClass
	var err error
	if chat.Type == ChatTypeChannel {
		res, err = telegramAPI.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: chat.ID, AccessHash: chat.Hash},
			ID:      ids,
		})
	} else {
		res, err = telegramAPI.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, fmt.Errorf("refetching message: %w", err)
	}

	var msgs []tg.MessageClass
	switch m := res.(type) {
	case *tg.MessagesMessages:
		msgs = m.Messages
	case *tg.MessagesMessagesSlice:
		msgs = m.Messages
	case *tg.MessagesChannelMessages:
		msgs = m.Messages
	}
	for _, mc := range msgs {
		msg, ok := mc.(*tg.Message)
		if !ok || msg.ID != v.MsgID {
			continue
		}
		doc := extractDocument(msg)
		if doc == nil || doc.ID != v.DocID {
			return nil, errVideoGone
		}
		if err := likeStore.UpdateFileRef(v.ChatID, v.MsgID, doc.FileReference); err != nil {
			fmt.Println("  best-of: error storing refreshed file reference:", err)
		}
		return doc.FileReference, nil
	}
	return nil, errVideoGone
}
