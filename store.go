package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	_ "modernc.org/sqlite"
)

const (
	ChatTypeChat    = "chat"
	ChatTypeChannel = "channel"
)

type Video struct {
	ChatID    int64  // basic group chat ID or channel ID (positive)
	ChatType  string // ChatTypeChat or ChatTypeChannel
	ChatHash  int64  // channel access hash, 0 for basic groups
	MsgID     int
	OrigMsgID int // user's message with the link, 0 if unknown
	UserID    int64
	UserName  string
	Title     string
	URL       string
	IsAudio   bool
	DocID     int64
	DocHash   int64
	FileRef   []byte
	PostedAt  time.Time
}

type RankedVideo struct {
	Video
	Likes     int
	Reactions int // native reactions on the original post and the video
}

// Score is what best-of digests are ranked by.
func (v RankedVideo) Score() int {
	return v.Likes + v.Reactions
}

type Chat struct {
	ID   int64
	Type string
	Hash int64
}

func (c Chat) InputPeer() tg.InputPeerClass {
	if c.Type == ChatTypeChannel {
		return &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.Hash}
	}
	return &tg.InputPeerChat{ChatID: c.ID}
}

type Store struct {
	db *sql.DB
}

const storeSchema = `
CREATE TABLE IF NOT EXISTS videos (
	chat_id      INTEGER NOT NULL,
	chat_type    TEXT    NOT NULL,
	chat_hash    INTEGER NOT NULL,
	msg_id       INTEGER NOT NULL,
	user_id      INTEGER NOT NULL,
	user_name    TEXT    NOT NULL,
	title        TEXT    NOT NULL,
	url          TEXT    NOT NULL,
	is_audio     INTEGER NOT NULL,
	doc_id       INTEGER NOT NULL,
	doc_hash     INTEGER NOT NULL,
	doc_file_ref BLOB    NOT NULL,
	posted_at    INTEGER NOT NULL,
	PRIMARY KEY (chat_id, msg_id)
);
CREATE INDEX IF NOT EXISTS videos_chat_posted ON videos (chat_id, posted_at);
CREATE TABLE IF NOT EXISTS likes (
	chat_id INTEGER NOT NULL,
	msg_id  INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	PRIMARY KEY (chat_id, msg_id, user_id)
);
CREATE TABLE IF NOT EXISTS digests (
	chat_id     INTEGER PRIMARY KEY,
	last_run_at INTEGER NOT NULL
);
`

// storeMigrations upgrade the schema in order; PRAGMA user_version holds how
// many of them have been applied.
var storeMigrations = []string{
	`ALTER TABLE videos ADD COLUMN orig_msg_id INTEGER NOT NULL DEFAULT 0`,
}

func migrateStore(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(storeMigrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(storeMigrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

func OpenStore(path string) (*Store, error) {
	// Pragmas go in the DSN so they are applied to every new connection.
	dsn := path + "?_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}
	// A single connection serializes access and keeps ":memory:" databases
	// shared across calls.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(storeSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating db schema: %w", err)
	}
	if err := migrateStore(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating db: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) AddVideo(v Video) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO videos
		(chat_id, chat_type, chat_hash, msg_id, user_id, user_name, title, url, is_audio, doc_id, doc_hash, doc_file_ref, posted_at, orig_msg_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ChatID, v.ChatType, v.ChatHash, v.MsgID, v.UserID, v.UserName, v.Title, v.URL, v.IsAudio,
		v.DocID, v.DocHash, v.FileRef, v.PostedAt.Unix(), v.OrigMsgID)
	return err
}

const videoColumns = `chat_id, chat_type, chat_hash, msg_id, user_id, user_name, title, url, is_audio, doc_id, doc_hash, doc_file_ref, posted_at, orig_msg_id`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVideo(r rowScanner, extra ...any) (Video, error) {
	var v Video
	var postedAt int64
	dest := append([]any{&v.ChatID, &v.ChatType, &v.ChatHash, &v.MsgID, &v.UserID, &v.UserName, &v.Title, &v.URL,
		&v.IsAudio, &v.DocID, &v.DocHash, &v.FileRef, &postedAt, &v.OrigMsgID}, extra...)
	if err := r.Scan(dest...); err != nil {
		return v, err
	}
	v.PostedAt = time.Unix(postedAt, 0).UTC()
	return v, nil
}

func (s *Store) GetVideo(chatID int64, msgID int) (*Video, error) {
	row := s.db.QueryRow(`SELECT `+videoColumns+` FROM videos WHERE chat_id = ? AND msg_id = ?`, chatID, msgID)
	v, err := scanVideo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Store) ToggleLike(chatID int64, msgID int, userID int64) (liked bool, count int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM likes WHERE chat_id = ? AND msg_id = ? AND user_id = ?`, chatID, msgID, userID)
	if err != nil {
		return false, 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return false, 0, err
	}
	if deleted == 0 {
		if _, err := tx.Exec(`INSERT INTO likes (chat_id, msg_id, user_id) VALUES (?, ?, ?)`, chatID, msgID, userID); err != nil {
			return false, 0, err
		}
		liked = true
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM likes WHERE chat_id = ? AND msg_id = ?`, chatID, msgID).Scan(&count); err != nil {
		return false, 0, err
	}
	return liked, count, tx.Commit()
}

// VideosInWindow returns the videos of a chat posted in [from, to) with their
// like counts, in posting order. Videos without likes are included.
func (s *Store) VideosInWindow(chatID int64, from, to time.Time) ([]RankedVideo, error) {
	rows, err := s.db.Query(`SELECT v.chat_id, v.chat_type, v.chat_hash, v.msg_id, v.user_id, v.user_name, v.title, v.url,
			v.is_audio, v.doc_id, v.doc_hash, v.doc_file_ref, v.posted_at, v.orig_msg_id, COUNT(l.user_id) AS likes
		FROM videos v LEFT JOIN likes l ON l.chat_id = v.chat_id AND l.msg_id = v.msg_id
		WHERE v.chat_id = ? AND v.posted_at >= ? AND v.posted_at < ?
		GROUP BY v.chat_id, v.msg_id
		ORDER BY v.posted_at ASC, v.msg_id ASC`, chatID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []RankedVideo
	for rows.Next() {
		var rv RankedVideo
		v, err := scanVideo(rows, &rv.Likes)
		if err != nil {
			return nil, err
		}
		rv.Video = v
		res = append(res, rv)
	}
	return res, rows.Err()
}

// Chats returns every chat with stored videos, using the most recently stored
// chat type and access hash.
func (s *Store) Chats() ([]Chat, error) {
	rows, err := s.db.Query(`SELECT chat_id, chat_type, chat_hash FROM videos
		WHERE rowid IN (SELECT MAX(rowid) FROM videos GROUP BY chat_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Type, &c.Hash); err != nil {
			return nil, err
		}
		res = append(res, c)
	}
	return res, rows.Err()
}

func (s *Store) LastDigest(chatID int64) (time.Time, bool, error) {
	var at int64
	err := s.db.QueryRow(`SELECT last_run_at FROM digests WHERE chat_id = ?`, chatID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(at, 0).UTC(), true, nil
}

func (s *Store) SetLastDigest(chatID int64, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO digests (chat_id, last_run_at) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET last_run_at = excluded.last_run_at`, chatID, at.Unix())
	return err
}

func (s *Store) UpdateFileRef(chatID int64, msgID int, ref []byte) error {
	_, err := s.db.Exec(`UPDATE videos SET doc_file_ref = ? WHERE chat_id = ? AND msg_id = ?`, ref, chatID, msgID)
	return err
}
