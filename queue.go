package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
)

const processStartStr = "🔍 Getting information..."
const processStr = "🔨 Processing"
const uploadStr = "☁️ Uploading"
const uploadDoneStr = "🏁 Uploading"
const errorStr = "❌ Error"
const canceledStr = "❌ Canceled"

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

	Reply         *message.Builder
	ReplyMsg      *tg.UpdateShortSentMessage
	ProgressMsgID int // ID of our progress message (to be deleted later)

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
	_, _ = e.Reply.Edit(e.ReplyMsg.ID).Text(ctx, s)
	e.sendTypingAction(ctx)
}

func (e *DownloadQueueEntry) editReplyWithAutoDelete(ctx context.Context, s string, deleteAfter time.Duration) {
	e.editReply(ctx, s)
	if deleteAfter > 0 {
		go func() {
			time.Sleep(deleteAfter)
			// Try to delete the message after the specified duration
			e.editReply(ctx, "🗑️ Message deleted")
			time.Sleep(1 * time.Second)
			e.editReply(ctx, "")
		}()
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
	return "👨‍👦‍👦 Request queued at position #" + fmt.Sprint(pos)
}

func (q *DownloadQueue) Add(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage, url, format string) {
	q.mutex.Lock()

	var replyStr string
	if len(q.entries) == 0 {
		replyStr = processStartStr
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
		replyStr = processStartStr
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
		_, _ = telegramSender.Reply(entities, u).Text(ctx, errorStr+": no active request to cancel")
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
		_, _ = msgCtx.ReplyBuilder().Text(ctx, errorStr+": no active request to cancel")
	}
	q.mutex.Unlock()
}

func (q *DownloadQueue) updateProgress(ctx context.Context, qEntry *DownloadQueueEntry, progressStr string, progressPercent int) {
	if progressPercent < 0 {
		qEntry.editReply(ctx, progressStr+"... (no progress available)\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
		return
	}
	if progressPercent == 0 {
		qEntry.editReply(ctx, progressStr+"..."+q.currentlyDownloadedEntry.progressInfo+"\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
		return
	}
	fmt.Print("  progress: ", progressPercent, "%\n")
	qEntry.editReply(ctx, progressStr+": "+getProgressbar(progressPercent, progressBarLength)+q.currentlyDownloadedEntry.progressInfo+"\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
	q.currentlyDownloadedEntry.lastDisplayedProgressPercent = progressPercent
}

func (q *DownloadQueue) HandleProgressPercentUpdate(progressStr string, progressPercent int) {
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
	defer q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()

	if q.currentlyDownloadedEntry.disableProgressPercentUpdate || q.currentlyDownloadedEntry.lastProgressPercent == progressPercent {
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

	qEntry.editReply(ctx, processStartStr)

	downloader := Downloader{
		ConvertStartFunc: func(ctx context.Context, videoCodecs, audioCodecs, convertActionsNeeded string) {
			q.currentlyDownloadedEntry.sourceCodecInfo = "🎬 Source: " + videoCodecs
			if audioCodecs == "" {
				q.currentlyDownloadedEntry.sourceCodecInfo += ", no audio"
			} else {
				if videoCodecs != "" {
					q.currentlyDownloadedEntry.sourceCodecInfo += " / "
				}
				q.currentlyDownloadedEntry.sourceCodecInfo += audioCodecs
			}
			if convertActionsNeeded == "" {
				q.currentlyDownloadedEntry.sourceCodecInfo += " (no conversion needed)"
			} else {
				q.currentlyDownloadedEntry.sourceCodecInfo += " (converting: " + convertActionsNeeded + ")"
			}
			qEntry.editReply(ctx, "🎬 Preparing download...\n"+q.currentlyDownloadedEntry.sourceCodecInfo)
		},
		UpdateProgressPercentFunc: q.HandleProgressPercentUpdate,
	}

	r, outputFormat, title, err := downloader.DownloadAndConvertURL(qEntry.Ctx, qEntry.OrigMsg.Message, qEntry.Format)
	if err != nil {
		fmt.Println("  error downloading:", err)
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
		q.currentlyDownloadedEntry.disableProgressPercentUpdate = true
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()
		// Show error and auto-delete after 10 seconds
		qEntry.editReplyWithAutoDelete(ctx, fmt.Sprint(errorStr+": ", err), 10*time.Second)
		return
	}

	// Feeding the returned io.ReadCloser to the uploader.
	fmt.Println("  processing...")
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
	q.updateProgress(ctx, qEntry, processStr, q.currentlyDownloadedEntry.lastProgressPercent)
	q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()

	err = dlUploader.UploadFileFromEntry(qEntry.Ctx, qEntry, r, outputFormat, title)
	if err != nil {
		fmt.Println("  error processing:", err)
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Lock()
		q.currentlyDownloadedEntry.disableProgressPercentUpdate = true
		q.currentlyDownloadedEntry.progressPercentUpdateMutex.Unlock()
		r.Close()
		// Show error and auto-delete after 10 seconds
		qEntry.editReplyWithAutoDelete(ctx, fmt.Sprint(errorStr+": ", err), 10*time.Second)
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
		qEntry.editReplyWithAutoDelete(ctx, fmt.Sprint(canceledStr, " by user"), 5*time.Second)
	} else {
		fmt.Print("  success! deleting progress message\n")
		// Success: delete the progress message immediately since video was sent as reply
		qEntry.editReply(ctx, "✅ Upload complete!")

		// Wait a moment then properly delete the progress message
		go func() {
			time.Sleep(3 * time.Second)
			fmt.Printf("  deleting progress message ID: %d\n", qEntry.ProgressMsgID)
			// Properly delete the progress message using the correct API
			_, err := telegramSender.Delete().Messages(ctx, qEntry.ProgressMsgID)
			if err != nil {
				fmt.Println("  error deleting progress message:", err)
			} else {
				fmt.Println("  progress message deleted successfully!")
			}
		}()
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
