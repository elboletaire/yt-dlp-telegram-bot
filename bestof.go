package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
		"Interval":  T("interval_"+string(interval), nil),
		"Rank":      rank,
		"Likes":     v.Likes,
		"Reactions": v.Reactions,
		"Score":     v.Score(),
		"User":      v.UserName,
		"Title":     v.Title,
		"Link":      link,
	})
}

// rankVideos orders videos by score, then by post time, leaving out the ones
// nobody liked or reacted to, and keeps the first limit.
func rankVideos(videos []RankedVideo, limit int) []RankedVideo {
	var res []RankedVideo
	for _, v := range videos {
		if v.Score() > 0 {
			res = append(res, v)
		}
	}
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].Score() != res[j].Score() {
			return res[i].Score() > res[j].Score()
		}
		if !res[i].PostedAt.Equal(res[j].PostedAt) {
			return res[i].PostedAt.Before(res[j].PostedAt)
		}
		return res[i].MsgID < res[j].MsgID
	})
	if len(res) > limit {
		res = res[:limit]
	}
	return res
}

// postDigest reposts the highest scored videos of [sched.Prev(occ), occ) in
// chat. The digest is recorded as done even when sending fails, so a broken
// chat (e.g. bot removed) doesn't retry forever.
func postDigest(ctx context.Context, store *Store, sched Schedule, count int, chat Chat, occ time.Time, fetch reactionsFetchFunc, send bestOfSendFunc) error {
	videos, err := store.VideosInWindow(chat.ID, sched.Prev(occ), occ)
	if err != nil {
		return fmt.Errorf("loading videos: %w", err)
	}
	addReactions(ctx, chat, videos, fetch)
	top := rankVideos(videos, count)

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
func runDueDigests(ctx context.Context, store *Store, sched Schedule, count int, now time.Time, fetch reactionsFetchFunc, send bestOfSendFunc) {
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
			if err := postDigest(ctx, store, sched, count, chat, occ, fetch, send); err != nil {
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
		runDueDigests(ctx, store, sched, count, now, fetchReactions, sendBestOfVideo)
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
	msgs, err := getChatMessages(ctx, chat, []int{v.MsgID})
	if err != nil {
		return nil, fmt.Errorf("refetching message: %w", err)
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
