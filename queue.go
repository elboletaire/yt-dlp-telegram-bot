package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
)

const maxProgressPercentUpdateInterval = time.Second
const maxProgressPercentUpdateIntervalGroup = 5 * time.Second // Slower updates for groups/channels
const progressBarLength = 10
const minProgressPercentChange = 10 // Only update every 10% for groups/channels

type DownloadQueueEntry struct {
	URL    string
	Format string

	OrigEntities         tg.Entities
	OrigMsgUpdate        *tg.UpdateNewMessage
	OrigChannelMsgUpdate *tg.UpdateNewChannelMessage
	OrigMsg              *tg.Message
	FromUser             *tg.PeerUser
	FromGroup            *tg.PeerChat

	// Chat the request came from, set only for groups/supergroups. Used to
	// delete progress messages and to record uploads for likes and best-of.
	ChatPeer     tg.InputPeerClass
	FromUserName string

	Reply         *message.Builder
	ReplyMsg      *tg.UpdateShortSentMessage
	ProgressMsgID int // ID of our progress message (to be deleted later)

	// Ephemeral progress message shown only to the requester in group chats.
	// When set, all progress/error texts go through it instead of a public
	// reply, and expiration is handled by Telegram.
	Ephemeral *EphemeralProgress

	Ctx       context.Context
	CtxCancel context.CancelFunc
	Canceled  bool
}

// func (e *DownloadQueueEntry) getTypingActionDst() tg.InputPeerClass {
// 	if e.FromGroup != nil {
// 		return &tg.InputPeerChat{
// 			ChatID: e.FromGroup.ChatID,
// 		}
// 	}
// 	return &tg.InputPeerUser{
// 		UserID: e.FromUser.UserID,
// 	}
// }

func (e *DownloadQueueEntry) sendTypingAction(ctx context.Context) {
	// _ = telegramSender.To(e.getTypingActionDst()).TypingAction().Typing(ctx)
}

func (e *DownloadQueueEntry) sendTypingCancelAction(ctx context.Context) {
	// _ = telegramSender.To(e.getTypingActionDst()).TypingAction().Cancel(ctx)
}

func (e *DownloadQueueEntry) editReply(ctx context.Context, s string) {
	if e.Ephemeral != nil {
		if err := e.Ephemeral.Edit(ctx, s); err != nil {
			fmt.Println("  error editing ephemeral progress message:", err)
		}
		return
	}
	_, _ = e.Reply.Edit(e.ReplyMsg.ID).Text(ctx, s)
	e.sendTypingAction(ctx)
}

func (e *DownloadQueueEntry) editReplyWithAutoDelete(ctx context.Context, qEntry *DownloadQueueEntry, s string, deleteAfter time.Duration) {
	e.editReply(ctx, s)
	if e.Ephemeral != nil {
		// Only the requester sees the message and Telegram expires it on its
		// own, so keep the text visible instead of deleting it.
		return
	}
	if deleteAfter > 0 {
		go func() {
			time.Sleep(deleteAfter)
			// Try to delete the message after the specified duration
			e.editReply(ctx, T("message_deleted", nil))
			time.Sleep(1 * time.Second)
			_, err := telegramSender.Self().Revoke().Messages(ctx, qEntry.ProgressMsgID)
			if err != nil {
				fmt.Println("  error deleting progress message:", err)
			} else {
				fmt.Println("  progress message deleted successfully!")
			}

		}()
	}
}

func (e *DownloadQueueEntry) deleteProgressMessage(ctx context.Context) {
	if e.Ephemeral != nil {
		if err := e.Ephemeral.Delete(ctx); err != nil {
			fmt.Println("  error deleting ephemeral progress message:", err)
		}
		return
	}

	if e.ProgressMsgID == 0 {
		return
	}

	var err error
	if e.ChatPeer != nil {
		_, err = telegramSender.To(e.ChatPeer).Revoke().Messages(ctx, e.ProgressMsgID)
	} else if e.FromGroup != nil {
		peer := &tg.InputPeerChat{ChatID: e.FromGroup.ChatID}
		_, err = telegramSender.To(peer).Revoke().Messages(ctx, e.ProgressMsgID)
	} else {
		_, err = telegramSender.Self().Revoke().Messages(ctx, e.ProgressMsgID)
	}

	if err != nil {
		fmt.Println("  error deleting progress message:", err)
	} else {
		fmt.Println("  progress message deleted successfully!")
	}
}

type currentlyDownloadedEntryType struct {
	disableProgressPercentUpdate bool
	progressPercentUpdateMutex   sync.Mutex
	lastProgressPercentUpdateAt  time.Time
	lastProgressPercent          int
	lastDisplayedProgressPercent int
	progressUpdateTimer          *time.Timer

	sourceCodecInfo string
	progressInfo    string
}

type DownloadQueue struct {
	ctx context.Context

	mutex          sync.Mutex
	entries        []DownloadQueueEntry
	processReqChan chan bool

	currentlyDownloadedEntry currentlyDownloadedEntryType
}

func (e *DownloadQueue) getQueuePositionString(pos int) string {
	return T("queued", map[string]any{"Position": pos})
}

func (q *DownloadQueue) Add(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage, url, format string) {
	q.mutex.Lock()

	var replyStr string
	if len(q.entries) == 0 {
		replyStr = T("getting_info", nil)
	} else {
		fmt.Println("  queueing request at position #", len(q.entries))
		replyStr = q.getQueuePositionString(len(q.entries))
	}

	newEntry := DownloadQueueEntry{
		URL:           url,
		Format:        format,
		OrigEntities:  entities,
		OrigMsgUpdate: u,
		OrigMsg:       u.Message.(*tg.Message),
	}

	newEntry.Reply = telegramSender.Reply(entities, u)
	replyText, _ := newEntry.Reply.Text(ctx, replyStr)
	newEntry.ReplyMsg = replyText.(*tg.UpdateShortSentMessage)

	// Store the ID of our progress message for later deletion
	newEntry.ProgressMsgID = newEntry.ReplyMsg.ID

	newEntry.FromUser, newEntry.FromGroup = resolveMsgSrc(newEntry.OrigMsg)

	q.entries = append(q.entries, newEntry)
	q.mutex.Unlock()

	select {
	case q.processReqChan <- true:
	default:
	}
}

func (q *DownloadQueue) AddFromContext(ctx context.Context, msgCtx *MessageContext, url, format string) {
	q.mutex.Lock()

	var replyStr string
	if len(q.entries) == 0 {
		replyStr = T("getting_info", nil)
	} else {
		fmt.Println("  queueing request at position #", len(q.entries))
		replyStr = q.getQueuePositionString(len(q.entries))
	}

	// Create a mock message for compatibility with existing queue structure
	mockMsg := &tg.Message{
		Message: msgCtx.MessageText,
		Out:     msgCtx.IsOutgoing,
	}

	newEntry := DownloadQueueEntry{
		URL:                  url,
		Format:               format,
		OrigEntities:         msgCtx.Entities,
		OrigMsgUpdate:        msgCtx.OrigMsgUpdate,
		OrigChannelMsgUpdate: msgCtx.OrigChannelMsgUpdate,
		OrigMsg:              mockMsg,
	}

	// Set up user/group info
	newEntry.FromUser = &tg.PeerUser{UserID: msgCtx.FromUserID}
	if msgCtx.FromGroupID != nil {
		newEntry.FromGroup = &tg.PeerChat{ChatID: -*msgCtx.FromGroupID}
	}
	if peer, ok := msgCtx.chatInputPeer(); ok {
		newEntry.ChatPeer = peer
	}
	newEntry.FromUserName = displayUserName(msgCtx.Entities, msgCtx.FromUserID)

	// In groups/supergroups, show progress only to the requester using an
	// ephemeral message so the chat stays clean for everyone else.
	if peer, receiver, ok := msgCtx.ephemeralTarget(); ok {
		eph, err := sendEphemeral(ctx, peer, receiver, replyStr)
		if err != nil {
			fmt.Println("  ephemeral progress message failed, falling back to public reply:", err)
		} else {
			fmt.Println("  ephemeral progress message sent, id:", eph.msgID)
			newEntry.Ephemeral = eph
		}
	} else if msgCtx.FromGroupID != nil {
		fmt.Println("  ephemeral target not resolvable for this chat, using public reply")
	}
	if newEntry.Ephemeral != nil {
		newEntry.Reply = msgCtx.ReplyBuilder()
		newEntry.ReplyMsg = &tg.UpdateShortSentMessage{}

		q.entries = append(q.entries, newEntry)
		q.mutex.Unlock()

		select {
		case q.processReqChan <- true:
		default:
		}
		return
	}

	newEntry.Reply = msgCtx.ReplyBuilder()
	replyText, _ := newEntry.Reply.Text(ctx, replyStr)

	// Handle different response types for regular vs channel messages
	switch reply := replyText.(type) {
	case *tg.UpdateShortSentMessage:
		// Regular message response
		newEntry.ReplyMsg = reply
		newEntry.ProgressMsgID = reply.ID
	case *tg.Updates:
		// Channel message response - extract the message from updates
		for _, update := range reply.Updates {
			if msgUpdate, ok := update.(*tg.UpdateNewChannelMessage); ok {
				if sentMsg, ok := msgUpdate.Message.(*tg.Message); ok {
					// Create a compatible UpdateShortSentMessage structure
					newEntry.ReplyMsg = &tg.UpdateShortSentMessage{
						ID:   sentMsg.ID,
						Date: sentMsg.Date,
					}
					newEntry.ProgressMsgID = sentMsg.ID
					break
				}
			}
		}
		// Fallback if we couldn't find the message in updates
		if newEntry.ReplyMsg == nil {
			newEntry.ReplyMsg = &tg.UpdateShortSentMessage{ID: 0, Date: 0}
			newEntry.ProgressMsgID = 0
		}
	default:
		// Fallback for any other response type
		newEntry.ReplyMsg = &tg.UpdateShortSentMessage{ID: 0, Date: 0}
		newEntry.ProgressMsgID = 0
	}

	q.entries = append(q.entries, newEntry)
	q.mutex.Unlock()

	select {
	case q.processReqChan <- true:
	default:
	}
}

func (q *DownloadQueue) CancelCurrentEntry(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage, url string) {
	q.mutex.Lock()
	if len(q.entries) > 0 {
		q.entries[0].Canceled = true
		q.entries[0].CtxCancel()
	} else {
		fmt.Println("  no active request to cancel")
		_, _ = telegramSender.Reply(entities, u).Text(ctx, T("no_active_request", nil))
	}
	q.mutex.Unlock()
}

func (q *DownloadQueue) CancelCurrentEntryFromContext(ctx context.Context, msgCtx *MessageContext, url string) {
	q.mutex.Lock()
	if len(q.entries) > 0 {
		q.entries[0].Canceled = true
		q.entries[0].CtxCancel()
	} else {
		fmt.Println("  no active request to cancel")
		replyEphemeralOrPublic(ctx, msgCtx, T("no_active_request", nil))
	}
	q.mutex.Unlock()
}

func (q *DownloadQueue) updateProgress(ctx context.Context, qEntry *DownloadQueueEntry, progressStr string, progressPercent int) {
	if progressPercent < 0 {
		qEntry.editReply(ctx, T("progress_unknown", map[string]any{"Label": progressStr})+"\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
		return
	}
	if progressPercent == 0 {
		qEntry.editReply(ctx, T("progress_starting", map[string]any{"Label": progressStr, "Info": q.currentlyDownloadedEntry.progressInfo})+"\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
		return
	}
	fmt.Print("  progress: ", progressPercent, "%\n")
	qEntry.editReply(ctx, T("progress_bar", map[string]any{
		"Label": progressStr,
		"Bar":   getProgressbar(progressPercent, progressBarLength),
		"Info":  q.currentlyDownloadedEntry.progressInfo,
	})+"\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
	q.currentlyDownloadedEntry.lastDisplayedProgressPercent = progressPercent
}

func (q *DownloadQueue) HandleProgressPercentUpdate(progressStr string, progressPercent int) {
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
	defer q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()

	if q.currentlyDownloadedEntry.disableProgressPercentUpdate || q.currentlyDownloadedEntry.lastProgressPercent == progressPercent {
		return
	}

	// Critical updates (100% progress) bypass all rate limiting
	isCriticalUpdate := progressPercent == 100
	if isCriticalUpdate {
		q.updateProgress(q.ctx, &q.entries[0], progressStr, progressPercent)
		return
	}

	// Check if this is a group/channel message to apply different rate limiting
	isGroupMessage := len(q.entries) > 0 && q.entries[0].FromGroup != nil

	// For groups/channels, only update on significant progress changes
	if isGroupMessage {
		progressDiff := progressPercent - q.currentlyDownloadedEntry.lastDisplayedProgressPercent
		if progressDiff < minProgressPercentChange && progressPercent < 100 && progressPercent > 0 {
			// Skip this update - not enough progress change for group messages
			q.currentlyDownloadedEntry.lastProgressPercent = progressPercent
			return
		}
	}

	q.currentlyDownloadedEntry.lastProgressPercent = progressPercent
	if progressPercent < 0 {
		q.currentlyDownloadedEntry.disableProgressPercentUpdate = true
		q.updateProgress(q.ctx, &q.entries[0], progressStr, progressPercent)
		return
	}

	if q.currentlyDownloadedEntry.progressUpdateTimer != nil {
		q.currentlyDownloadedEntry.progressUpdateTimer.Stop()
		select {
		case <-q.currentlyDownloadedEntry.progressUpdateTimer.C:
		default:
		}
	}

	// Use different update intervals for groups vs direct messages
	updateInterval := maxProgressPercentUpdateInterval
	if isGroupMessage {
		updateInterval = maxProgressPercentUpdateIntervalGroup
	}

	timeElapsedSinceLastUpdate := time.Since(q.currentlyDownloadedEntry.lastProgressPercentUpdateAt)
	if timeElapsedSinceLastUpdate < updateInterval {
		q.currentlyDownloadedEntry.progressUpdateTimer = time.AfterFunc(updateInterval-timeElapsedSinceLastUpdate, func() {
			q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
			if !q.currentlyDownloadedEntry.disableProgressPercentUpdate {
				q.updateProgress(q.ctx, &q.entries[0], progressStr, progressPercent)
				q.currentlyDownloadedEntry.lastProgressPercentUpdateAt = time.Now()
			}
			q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()
		})
		return
	}
	q.updateProgress(q.ctx, &q.entries[0], progressStr, progressPercent)
	q.currentlyDownloadedEntry.lastProgressPercentUpdateAt = time.Now()
}

func (q *DownloadQueue) processQueueEntry(ctx context.Context, qEntry *DownloadQueueEntry) {
	fromUsername := getFromUsername(qEntry.OrigEntities, qEntry.FromUser.UserID)
	fmt.Print("processing request by")
	if fromUsername != "" {
		fmt.Print(" from ", fromUsername, "#", qEntry.FromUser.UserID)
	}
	fmt.Println(":", qEntry.URL)

	qEntry.editReply(ctx, T("getting_info", nil))

	downloader := Downloader{
		ConvertStartFunc: func(ctx context.Context, videoCodecs, audioCodecs, convertActionsNeeded string) {
			q.currentlyDownloadedEntry.sourceCodecInfo = T("source_codecs", map[string]any{
				"Video":   videoCodecs,
				"Audio":   audioCodecs,
				"Convert": convertActionsNeeded,
			})
			qEntry.editReply(ctx, T("preparing_download", nil)+"\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
		},
		UpdateProgressPercentFunc: q.HandleProgressPercentUpdate,
	}

	r, outputFormat, title, videoMetadata, err := downloader.DownloadAndConvertURL(qEntry.Ctx, qEntry.URL, qEntry.Format)
	if err != nil {
		fmt.Println("  error downloading:", err)
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
		q.currentlyDownloadedEntry.disableProgressPercentUpdate = true
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()
		// Show error and auto-delete after 10 seconds
		qEntry.editReplyWithAutoDelete(ctx, qEntry, T("error", map[string]any{"Err": err}), 10*time.Second)
		return
	}

	// Feeding the returned io.ReadCloser to the uploader.
	fmt.Println("  processing...")
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
	q.updateProgress(ctx, qEntry, T("processing", nil), q.currentlyDownloadedEntry.lastProgressPercent)
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()

	err = dlUploader.UploadFileFromEntry(qEntry.Ctx, qEntry, r, outputFormat, title, videoMetadata)
	if err != nil {
		fmt.Println("  error processing:", err)
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
		q.currentlyDownloadedEntry.disableProgressPercentUpdate = true
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()
		r.Close()
		// Show error and auto-delete after 10 seconds
		qEntry.editReplyWithAutoDelete(ctx, qEntry, T("error", map[string]any{"Err": err}), 10*time.Second)
		return
	}
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
	q.currentlyDownloadedEntry.disableProgressPercentUpdate = true
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()
	r.Close()

	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
	if qEntry.Canceled {
		fmt.Print("  canceled\n")
		// Show canceled message and auto-delete after 5 seconds
		qEntry.editReplyWithAutoDelete(ctx, qEntry, T("canceled", nil), 5*time.Second)
	} else {
		fmt.Print("  success!\n")
	}
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()
	qEntry.sendTypingCancelAction(ctx)
}

func (q *DownloadQueue) processor() {
	for {
		q.mutex.Lock()
		if (len(q.entries)) == 0 {
			q.mutex.Unlock()
			<-q.processReqChan
			continue
		}

		// Updating queue positions for all waiting entries.
		for i := 1; i < len(q.entries); i++ {
			q.entries[i].editReply(q.ctx, q.getQueuePositionString(i))
			q.entries[i].sendTypingCancelAction(q.ctx)
		}

		q.entries[0].Ctx, q.entries[0].CtxCancel = context.WithTimeout(q.ctx, downloadAndConvertTimeout)

		qEntry := &q.entries[0]
		q.mutex.Unlock()

		q.currentlyDownloadedEntry = currentlyDownloadedEntryType{}

		q.processQueueEntry(q.ctx, qEntry)

		q.mutex.Lock()
		q.entries[0].CtxCancel()
		q.entries = q.entries[1:]
		if len(q.entries) == 0 {
			fmt.Print("finished queue processing\n")
		}
		q.mutex.Unlock()
	}
}

func (q *DownloadQueue) Init(ctx context.Context) {
	q.ctx = ctx
	q.processReqChan = make(chan bool)
	go q.processor()
}
