package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/wader/goutubedl"
	"golang.org/x/exp/slices"
)

var dlQueue DownloadQueue

var telegramUploader *uploader.Uploader
var telegramSender *message.Sender

type MessageContext struct {
	MessageText          string
	IsOutgoing           bool
	Entities             tg.Entities
	FromUserID           int64
	FromGroupID          *int64 // nil if not from group
	ReplyBuilder         func() *message.Builder
	OrigMsgUpdate        *tg.UpdateNewMessage        // for regular messages
	OrigChannelMsgUpdate *tg.UpdateNewChannelMessage // for channel messages
}

func handleCmdDLP(ctx context.Context, msgCtx *MessageContext, messageText string) {
	format := "video"
	s := strings.Split(messageText, " ")
	if len(s) >= 2 && s[0] == "mp3" {
		messageText = strings.Join(s[1:], " ")
		format = "mp3"
	}

	// Check if message is an URL.
	validURI := true
	uri, err := url.ParseRequestURI(messageText)
	if err != nil || (uri.Scheme != "http" && uri.Scheme != "https") {
		validURI = false
	} else {
		_, err = net.LookupHost(uri.Host)
		if err != nil {
			validURI = false
		}
	}
	if !validURI {
		fmt.Println("  (not an url)")
		return
	}

	dlQueue.AddFromContext(ctx, msgCtx, messageText, format)
}

func handleCmdDLPCancel(ctx context.Context, msgCtx *MessageContext, messageText string) {
	dlQueue.CancelCurrentEntryFromContext(ctx, msgCtx, messageText)
}

func handleMessageContext(ctx context.Context, msgCtx *MessageContext) error {
	if msgCtx.IsOutgoing {
		// Outgoing message, not interesting.
		return nil
	}

	fromUsername := getFromUsername(msgCtx.Entities, msgCtx.FromUserID)

	fmt.Print("got message")
	if fromUsername != "" {
		fmt.Print(" from ", fromUsername, "#", msgCtx.FromUserID)
	}
	fmt.Println(":", msgCtx.MessageText)

	if msgCtx.FromGroupID != nil {
		fmt.Println("  (group message)")
		fmt.Print("  msg from group #", *msgCtx.FromGroupID)
		if !slices.Contains(params.AllowedGroupIDs, *msgCtx.FromGroupID) {
			fmt.Println(", group not allowed, ignoring")
			return nil
		}
		fmt.Println()
	} else {
		if !slices.Contains(params.AllowedUserIDs, msgCtx.FromUserID) {
			fmt.Println("  user not allowed, ignoring")
			return nil
		}
	}

	// Check if message is a command.
	if msgCtx.MessageText[0] == '/' || msgCtx.MessageText[0] == '!' {
		cmd := strings.Split(msgCtx.MessageText, " ")[0]
		messageText := strings.TrimPrefix(msgCtx.MessageText, cmd+" ")
		if strings.Contains(cmd, "@") {
			cmd = strings.Split(cmd, "@")[0]
		}
		cmd = cmd[1:] // Cutting the command character.
		switch cmd {
		case "dlp":
			handleCmdDLP(ctx, msgCtx, messageText)
			return nil
		case "dlpcancel":
			handleCmdDLPCancel(ctx, msgCtx, messageText)
			return nil
		case "start":
			fmt.Println("  (start cmd)")
			if msgCtx.FromGroupID == nil {
				_, _ = msgCtx.ReplyBuilder().Text(ctx, "🤖 Welcome! This bot downloads videos from various "+
					"supported sources and then re-uploads them to Telegram, so they can be viewed with Telegram's built-in "+
					"video player.\n\nMore info: https://github.com/nonoo/yt-dlp-telegram-bot")
			}
			return nil
		default:
			fmt.Println("  (invalid cmd)")
			if msgCtx.FromGroupID == nil {
				_, _ = msgCtx.ReplyBuilder().Text(ctx, errorStr+": invalid command")
			}
			return nil
		}
	}

	// Process URLs for both direct messages and authorized groups
	handleCmdDLP(ctx, msgCtx, msgCtx.MessageText)
	return nil
}

func handleMsg(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage) error {
	msg, ok := u.Message.(*tg.Message)
	if !ok {
		return nil
	}

	fromUser, fromGroup := resolveMsgSrc(msg)

	var fromGroupID *int64
	if fromGroup != nil {
		groupID := -fromGroup.ChatID
		fromGroupID = &groupID
	}

	msgCtx := &MessageContext{
		MessageText:   msg.Message,
		IsOutgoing:    msg.Out,
		Entities:      entities,
		FromUserID:    fromUser.UserID,
		FromGroupID:   fromGroupID,
		ReplyBuilder:  func() *message.Builder { return telegramSender.Reply(entities, u) },
		OrigMsgUpdate: u,
	}

	return handleMessageContext(ctx, msgCtx)
}

func handleChannelMsg(ctx context.Context, entities tg.Entities, u *tg.UpdateNewChannelMessage) error {
	msg, ok := u.Message.(*tg.Message)
	if !ok {
		return fmt.Errorf("expected tg.Message, got %T", u.Message)
	}

	fromUser, fromGroup := resolveMsgSrc(msg)

	var fromGroupID *int64
	if fromGroup != nil {
		groupID := -fromGroup.ChatID
		fromGroupID = &groupID
	}

	msgCtx := &MessageContext{
		MessageText:          msg.Message,
		IsOutgoing:           msg.Out,
		Entities:             entities,
		FromUserID:           fromUser.UserID,
		FromGroupID:          fromGroupID,
		ReplyBuilder:         func() *message.Builder { return telegramSender.Reply(entities, u) },
		OrigChannelMsgUpdate: u,
	}

	return handleMessageContext(ctx, msgCtx)
}

func main() {
	fmt.Println("yt-dlp-telegram-bot starting...")

	if err := params.Init(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	// Dispatcher handles incoming updates.
	dispatcher := tg.NewUpdateDispatcher()
	opts := telegram.Options{
		UpdateHandler: dispatcher,
	}
	var err error
	opts, err = telegram.OptionsFromEnvironment(opts)
	if err != nil {
		panic(fmt.Sprint("options from env err: ", err))
	}

	client := telegram.NewClient(params.ApiID, params.ApiHash, opts)

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			panic(fmt.Sprint("auth status err: ", err))
		}

		if !status.Authorized { // Not logged in?
			fmt.Println("logging in...")
			if _, err := client.Auth().Bot(ctx, params.BotToken); err != nil {
				panic(fmt.Sprint("login err: ", err))
			}
		}

		api := client.API()

		telegramUploader = uploader.NewUploader(api).WithProgress(dlUploader)
		telegramSender = message.NewSender(api).WithUploader(telegramUploader)

		goutubedl.Path, err = exec.LookPath(goutubedl.Path)
		if err != nil {
			goutubedl.Path, err = ytdlpDownloadLatest(ctx)
			if err != nil {
				panic(fmt.Sprint("error: ", err))
			}
		}

		dlQueue.Init(ctx)

		dispatcher.OnNewMessage(handleMsg)
		dispatcher.OnNewChannelMessage(handleChannelMsg)

		fmt.Println("telegram connection up")

		ytdlpVersionCheckStr, updateNeeded, _ := ytdlpVersionCheckGetStr(ctx)
		if updateNeeded {
			goutubedl.Path, err = ytdlpDownloadLatest(ctx)
			if err != nil {
				panic(fmt.Sprint("error: ", err))
			}
			ytdlpVersionCheckStr, _, _ = ytdlpVersionCheckGetStr(ctx)
		}
		sendTextToAdmins(ctx, "🤖 Bot started, "+ytdlpVersionCheckStr)

		go func() {
			for {
				time.Sleep(24 * time.Hour)
				s, updateNeeded, gotError := ytdlpVersionCheckGetStr(ctx)
				if gotError {
					sendTextToAdmins(ctx, s)
				} else if updateNeeded {
					goutubedl.Path, err = ytdlpDownloadLatest(ctx)
					if err != nil {
						panic(fmt.Sprint("error: ", err))
					}
					ytdlpVersionCheckStr, _, _ = ytdlpVersionCheckGetStr(ctx)
					sendTextToAdmins(ctx, "🤖 Bot updated, "+ytdlpVersionCheckStr)
				}
			}
		}()

		<-ctx.Done()
		return nil
	}); err != nil {
		panic(err)
	}
}
