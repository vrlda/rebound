package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type App struct {
	cfg    Config
	db     *sql.DB
	sealer *Sealer
	sync   *syncState
}

func newApp(cfg Config) (*App, error) {
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "attachments"), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(cfg.DataDir, "inbox.db")+
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writers, avoids SQLITE_BUSY storms
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	sealer, err := loadSealer(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	return &App{cfg: cfg, db: db, sealer: sealer, sync: newSyncState()}, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
	ip TEXT NOT NULL DEFAULT '', user_agent TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS login_failures (ip TEXT NOT NULL, at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS idx_login_failures_at ON login_failures(at);
CREATE TABLE IF NOT EXISTS accounts (
	id INTEGER PRIMARY KEY, name TEXT NOT NULL, api_key_enc TEXT NOT NULL,
	color TEXT NOT NULL DEFAULT '#6d5dfc', identities TEXT NOT NULL DEFAULT '[]',
	default_from TEXT NOT NULL DEFAULT '', signature TEXT NOT NULL DEFAULT '',
	last_sync_at INTEGER, last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	resend_id TEXT,
	direction TEXT NOT NULL,              -- in | out
	folder TEXT NOT NULL,                 -- inbox | sent | spam | archive | trash
	thread_id INTEGER,
	message_id TEXT NOT NULL DEFAULT '', in_reply_to TEXT NOT NULL DEFAULT '', refs TEXT NOT NULL DEFAULT '',
	from_addr TEXT NOT NULL, from_name TEXT NOT NULL DEFAULT '',
	to_json TEXT NOT NULL DEFAULT '[]', cc_json TEXT NOT NULL DEFAULT '[]',
	bcc_json TEXT NOT NULL DEFAULT '[]', reply_to_json TEXT NOT NULL DEFAULT '[]',
	subject TEXT NOT NULL DEFAULT '', subject_norm TEXT NOT NULL DEFAULT '',
	text_body TEXT NOT NULL DEFAULT '', html_body TEXT NOT NULL DEFAULT '', snippet TEXT NOT NULL DEFAULT '',
	received_at INTEGER NOT NULL, is_read INTEGER NOT NULL DEFAULT 0, is_starred INTEGER NOT NULL DEFAULT 0,
	spam_reason TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
	UNIQUE(account_id, resend_id));
CREATE INDEX IF NOT EXISTS idx_messages_folder ON messages(folder, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_msgid ON messages(account_id, message_id);
CREATE INDEX IF NOT EXISTS idx_messages_subject ON messages(account_id, subject_norm);
CREATE TABLE IF NOT EXISTS attachments (
	id INTEGER PRIMARY KEY,
	message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	filename TEXT NOT NULL, content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
	size INTEGER NOT NULL DEFAULT 0, content_id TEXT NOT NULL DEFAULT '', path TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments(message_id);
CREATE TABLE IF NOT EXISTS rules (
	id INTEGER PRIMARY KEY, pattern TEXT NOT NULL UNIQUE, action TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS push_subs (
	id INTEGER PRIMARY KEY, endpoint TEXT NOT NULL UNIQUE, p256dh TEXT NOT NULL, auth TEXT NOT NULL,
	user_agent TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
`

func (a *App) getSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := a.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (a *App) setSetting(ctx context.Context, key, value string) error {
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (a *App) deleteSetting(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		if _, err := a.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return nil
}

func nowUnix() int64 { return time.Now().Unix() }

// ─── Secrets ────────────────────────────────────────────────────────────────

// Sealer encrypts secrets at rest (Resend API keys, TOTP secret) with a
// 256-bit key generated on first boot and kept in DATA_DIR/master.key.
type Sealer struct{ aead cipher.AEAD }

func loadSealer(dir string) (*Sealer, error) {
	path := filepath.Join(dir, "master.key")
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("master.key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plain string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Sealer) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	n := s.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("sealed value too short")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	return string(plain), err
}

func randomToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
