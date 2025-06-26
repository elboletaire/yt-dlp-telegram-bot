package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/wader/goutubedl"
	"golang.org/x/exp/slices"
)

type paramsType struct {
	ApiID    int
	ApiHash  string
	BotToken string

	AllowedUserIDs  []int64
	AdminUserIDs    []int64
	AllowedGroupIDs []int64

	TrollUserIDs     []int64
	TrollSentences   []string
	TrollProbability int // 0-100

	MaxSize int64
}

var params paramsType

func (p *paramsType) Init() error {
	// Further available environment variables:
	// 	SESSION_FILE:  path to session file
	// 	SESSION_DIR:   path to session directory, if SESSION_FILE is not set

	var apiID string
	flag.StringVar(&apiID, "api-id", "", "telegram api_id")
	flag.StringVar(&p.ApiHash, "api-hash", "", "telegram api_hash")
	flag.StringVar(&p.BotToken, "bot-token", "", "telegram bot token")
	flag.StringVar(&goutubedl.Path, "yt-dlp-path", "", "yt-dlp path")
	var allowedUserIDs string
	flag.StringVar(&allowedUserIDs, "allowed-user-ids", "", "allowed telegram user ids")
	var adminUserIDs string
	flag.StringVar(&adminUserIDs, "admin-user-ids", "", "admin telegram user ids")
	var allowedGroupIDs string
	flag.StringVar(&allowedGroupIDs, "allowed-group-ids", "", "allowed telegram group ids")
	var trollUserIDs string
	flag.StringVar(&trollUserIDs, "troll-user-ids", "", "telegram user ids to troll")
	var trollSentences string
	flag.StringVar(&trollSentences, "troll-sentences", "", "comma-separated list of troll sentences")
	flag.IntVar(&p.TrollProbability, "troll-probability", 0, "probability (0-100) of trolling")
	var maxSize string
	flag.StringVar(&maxSize, "max-size", "", "allowed max size of video files")
	flag.Parse()

	var err error
	if apiID == "" {
		apiID = os.Getenv("API_ID")
	}
	if apiID == "" {
		return fmt.Errorf("api id not set")
	}
	p.ApiID, err = strconv.Atoi(apiID)
	if err != nil {
		return fmt.Errorf("invalid api_id")
	}

	if p.ApiHash == "" {
		p.ApiHash = os.Getenv("API_HASH")
	}
	if p.ApiHash == "" {
		return fmt.Errorf("api hash not set")
	}

	if p.BotToken == "" {
		p.BotToken = os.Getenv("BOT_TOKEN")
	}
	if p.BotToken == "" {
		return fmt.Errorf("bot token not set")
	}

	if goutubedl.Path == "" {
		goutubedl.Path = os.Getenv("YTDLP_PATH")
	}
	if goutubedl.Path == "" {
		goutubedl.Path = "yt-dlp"
	}

	if allowedUserIDs == "" {
		allowedUserIDs = os.Getenv("ALLOWED_USERIDS")
	}
	sa := strings.Split(allowedUserIDs, ",")
	for _, idStr := range sa {
		if idStr == "" {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return fmt.Errorf("allowed user ids contains invalid user ID: " + idStr)
		}
		p.AllowedUserIDs = append(p.AllowedUserIDs, id)
	}

	if adminUserIDs == "" {
		adminUserIDs = os.Getenv("ADMIN_USERIDS")
	}
	sa = strings.Split(adminUserIDs, ",")
	for _, idStr := range sa {
		if idStr == "" {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return fmt.Errorf("admin ids contains invalid user ID: " + idStr)
		}
		p.AdminUserIDs = append(p.AdminUserIDs, id)
		if !slices.Contains(p.AllowedUserIDs, id) {
			p.AllowedUserIDs = append(p.AllowedUserIDs, id)
		}
	}

	if allowedGroupIDs == "" {
		allowedGroupIDs = os.Getenv("ALLOWED_GROUPIDS")
	}
	sa = strings.Split(allowedGroupIDs, ",")
	for _, idStr := range sa {
		if idStr == "" {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return fmt.Errorf("allowed group ids contains invalid group ID: " + idStr)
		}
		p.AllowedGroupIDs = append(p.AllowedGroupIDs, id)
	}

	if trollUserIDs == "" {
		trollUserIDs = os.Getenv("TROLL_USERIDS")
	}
	sa = strings.Split(trollUserIDs, ",")
	for _, idStr := range sa {
		if idStr == "" {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return fmt.Errorf("troll user ids contains invalid user ID: " + idStr)
		}
		p.TrollUserIDs = append(p.TrollUserIDs, id)
	}

	if trollSentences == "" {
		trollSentences = os.Getenv("TROLL_SENTENCES")
	}
	if trollSentences != "" {
		sentences := strings.Split(trollSentences, ",")
		for _, sentence := range sentences {
			sentence = strings.TrimSpace(sentence)
			if sentence != "" {
				p.TrollSentences = append(p.TrollSentences, sentence)
			}
		}
	}

	if p.TrollProbability == 0 {
		if probStr := os.Getenv("TROLL_PROBABILITY"); probStr != "" {
			prob, err := strconv.Atoi(probStr)
			if err != nil {
				return fmt.Errorf("invalid troll probability: %w", err)
			}
			p.TrollProbability = prob
		}
	}

	if p.TrollProbability < 0 || p.TrollProbability > 100 {
		return fmt.Errorf("troll probability must be between 0 and 100")
	}

	if maxSize == "" {
		maxSize = os.Getenv("MAX_SIZE")
	}
	if maxSize != "" {
		b, err := humanize.ParseBigBytes(maxSize)
		if err != nil {
			return fmt.Errorf("invalid max size: %w", err)
		}
		p.MaxSize = b.Int64()
	}

	// Writing env. var YTDLP_COOKIES contents to a file.
	// In case a docker container is used, the yt-dlp.conf points yt-dlp to this cookie file.
	if cookies := os.Getenv("YTDLP_COOKIES"); cookies != "" {
		f, err := os.Create("/tmp/ytdlp-cookies.txt")
		if err != nil {
			return fmt.Errorf("couldn't create cookies file: %w", err)
		}
		_, err = f.WriteString(cookies)
		if err != nil {
			return fmt.Errorf("couldn't write cookies file: %w", err)
		}
		f.Close()
	}

	return nil
}
