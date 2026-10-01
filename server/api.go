package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	auth := a.requireAuth

	mux.HandleFunc("GET /api/auth/state", a.handleAuthState)
	mux.HandleFunc("POST /api/auth/setup/begin", csrf(a.handleSetupBegin))
	mux.HandleFunc("POST /api/auth/setup/finish", csrf(a.handleSetupFinish))
	mux.HandleFunc("POST /api/auth/login", csrf(a.handleLogin))
	mux.HandleFunc("POST /api/auth/logout", csrf(a.handleLogout))
	mux.HandleFunc("POST /api/auth/logout-all", auth(a.handleLogoutAll))
	mux.HandleFunc("POST /api/auth/password", auth(a.handleChangePassword))

	mux.HandleFunc("GET /api/accounts", auth(a.handleListAccounts))
	mux.HandleFunc("POST /api/accounts", auth(a.handleCreateAccount))
	mux.HandleFunc("PATCH /api/accounts/{id}", auth(a.handleUpdateAccount))
	mux.HandleFunc("DELETE /api/accounts/{id}", auth(a.handleDeleteAccount))
	mux.HandleFunc("POST /api/accounts/{id}/sync", auth(a.handleSyncAccount))

	mux.HandleFunc("GET /api/counts", auth(a.handleCounts))
	mux.HandleFunc("GET /api/messages", auth(a.handleListMessages))
	mux.HandleFunc("GET /api/messages/{id}", auth(a.handleGetThread))
	mux.HandleFunc("PATCH /api/messages/{id}", auth(a.handleUpdateMessage))
	mux.HandleFunc("DELETE /api/messages/{id}", auth(a.handleDeleteMessage))
	mux.HandleFunc("GET /api/attachments/{id}", auth(a.handleAttachment))
	mux.HandleFunc("POST /api/send", auth(a.handleSend))

	mux.HandleFunc("GET /api/rules", auth(a.handleListRules))
	mux.HandleFunc("POST /api/rules", auth(a.handleCreateRule))
	mux.HandleFunc("DELETE /api/rules/{id}", auth(a.handleDeleteRule))

	mux.HandleFunc("GET /api/push/key", auth(a.handlePushKey))
	mux.HandleFunc("POST /api/push/subscribe", auth(a.handlePushSubscribe))
	mux.HandleFunc("POST /api/push/test", auth(a.handlePushTest))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("/", a.staticHandler())
	return securityHeaders(mux)
}

func csrf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Inbox") != "1" {
			writeErr(w, http.StatusForbidden, "missing X-Inbox header")
			return
		}
		next(w, r)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	// Email HTML renders in a sandboxed srcdoc iframe (no scripts); img-src https:
	// lets the user opt in to remote images, which the iframe blocks by default.
	const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https:; font-src 'self' data:; connect-src 'self'; " +
		"frame-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; " +
		"object-src 'none'; worker-src 'self'; manifest-src 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) staticHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Fatal(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" {
			if f, err := sub.Open(p); err == nil {
				f.Close()
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback.
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}

// ─── JSON helpers ───────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// ─── Accounts ───────────────────────────────────────────────────────────────

type accountDTO struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Color       string   `json:"color"`
	Identities  []string `json:"identities"`
	DefaultFrom string   `json:"default_from"`
	Signature   string   `json:"signature"`
	LastSyncAt  *int64   `json:"last_sync_at"`
	LastError   string   `json:"last_error"`
	KeyHint     string   `json:"key_hint"`
}

func (a *App) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accs, err := a.loadAccounts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]accountDTO, 0, len(accs))
	for _, acc := range accs {
		d := accountDTO{ID: acc.ID, Name: acc.Name, Color: acc.Color, Identities: acc.Identities,
			DefaultFrom: acc.DefaultFrom, Signature: acc.Signature, LastError: acc.LastError}
		if d.Identities == nil {
			d.Identities = []string{}
		}
		if acc.LastSyncAt.Valid {
			v := acc.LastSyncAt.Int64
			d.LastSyncAt = &v
		}
		if len(acc.APIKey) > 6 {
			d.KeyHint = acc.APIKey[:3] + "…" + acc.APIKey[len(acc.APIKey)-4:]
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (a *App) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		APIKey string `json:"api_key"`
		Color  string `json:"color"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name, body.APIKey = strings.TrimSpace(body.Name), strings.TrimSpace(body.APIKey)
	if body.Name == "" || !strings.HasPrefix(body.APIKey, "re_") {
		writeErr(w, http.StatusBadRequest, "name and a Resend API key (re_…) are required")
		return
	}
	if !colorRe.MatchString(body.Color) {
		body.Color = "#6d5dfc"
	}
	id, err := a.createAccount(r.Context(), body.Name, body.APIKey, body.Color)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (a *App) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		Name        *string   `json:"name"`
		Color       *string   `json:"color"`
		Identities  *[]string `json:"identities"`
		DefaultFrom *string   `json:"default_from"`
		Signature   *string   `json:"signature"`
		APIKey      *string   `json:"api_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	set := func(col string, v any) error {
		_, err := a.db.ExecContext(ctx, `UPDATE accounts SET `+col+` = ? WHERE id = ?`, v, id)
		return err
	}
	var err error
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		err = errors.Join(err, set("name", strings.TrimSpace(*body.Name)))
	}
	if body.Color != nil && colorRe.MatchString(*body.Color) {
		err = errors.Join(err, set("color", *body.Color))
	}
	if body.Identities != nil {
		clean := []string{}
		for _, s := range *body.Identities {
			if _, addr := splitAddress(s); addr != "" && strings.Contains(addr, "@") {
				clean = append(clean, addr)
			}
		}
		b, _ := json.Marshal(clean)
		err = errors.Join(err, set("identities", string(b)))
	}
	if body.DefaultFrom != nil {
		err = errors.Join(err, set("default_from", strings.TrimSpace(*body.DefaultFrom)))
	}
	if body.Signature != nil {
		err = errors.Join(err, set("signature", *body.Signature))
	}
	if body.APIKey != nil && strings.HasPrefix(strings.TrimSpace(*body.APIKey), "re_") {
		key := strings.TrimSpace(*body.APIKey)
		if _, _, e := newResend(key).ListReceiving(ctx, "", 1); e != nil {
			writeErr(w, http.StatusBadRequest, "Resend rejected this API key: "+e.Error())
			return
		}
		sealed, e := a.sealer.Seal(key)
		err = errors.Join(err, e, set("api_key_enc", sealed))
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx := r.Context()
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM messages WHERE account_id = ?`, id)
	if err == nil {
		var ids []int64
		for rows.Next() {
			var mid int64
			if rows.Scan(&mid) == nil {
				ids = append(ids, mid)
			}
		}
		rows.Close()
		for _, mid := range ids {
			_ = os.RemoveAll(filepath.Join(a.cfg.DataDir, "attachments", itoa(mid)))
		}
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleSyncAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	acc, err := a.loadAccount(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	a.syncAccount(r.Context(), acc)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ─── Messages ───────────────────────────────────────────────────────────────

type messageRow struct {
	ID          int64    `json:"id"`
	AccountID   int64    `json:"account_id"`
	Direction   string   `json:"direction"`
	Folder      string   `json:"folder"`
	ThreadID    int64    `json:"thread_id"`
	FromAddr    string   `json:"from_addr"`
	FromName    string   `json:"from_name"`
	To          []string `json:"to"`
	Subject     string   `json:"subject"`
	Snippet     string   `json:"snippet"`
	ReceivedAt  int64    `json:"received_at"`
	IsRead      bool     `json:"is_read"`
	IsStarred   bool     `json:"is_starred"`
	SpamReason  string   `json:"spam_reason"`
	Attachments int      `json:"attachments"`
	ThreadCount int      `json:"thread_count"`
}

var validFolders = map[string]bool{"inbox": true, "sent": true, "spam": true, "archive": true, "trash": true}

func (a *App) handleListMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := []string{}
	args := []any{}
	folder := q.Get("folder")
	search := strings.TrimSpace(q.Get("q"))
	switch {
	case folder == "starred":
		where = append(where, "m.is_starred = 1", "m.folder != 'trash'")
	case validFolders[folder]:
		// One row per conversation: the newest message of the thread in this folder.
		where = append(where, "m.folder = ?", `m.id = (SELECT t.id FROM messages t WHERE t.thread_id = m.thread_id AND t.folder = m.folder
			ORDER BY t.received_at DESC, t.id DESC LIMIT 1)`)
		args = append(args, folder)
	case search != "":
		where = append(where, "m.folder != 'trash'")
	default:
		where = append(where, "m.folder = 'inbox'")
	}
	if acc, err := strconv.ParseInt(q.Get("account"), 10, 64); err == nil && acc > 0 {
		where = append(where, "m.account_id = ?")
		args = append(args, acc)
	}
	if search != "" {
		like := "%" + strings.ReplaceAll(search, "%", "") + "%"
		where = append(where, "(m.subject LIKE ? OR m.from_addr LIKE ? OR m.from_name LIKE ? OR m.to_json LIKE ? OR m.text_body LIKE ?)")
		args = append(args, like, like, like, like, like)
	}
	if before, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && before > 0 {
		where = append(where, "m.received_at < ?")
		args = append(args, before)
	}
	limit := 60
	args = append(args, limit)
	rows, err := a.db.QueryContext(r.Context(), `SELECT m.id, m.account_id, m.direction, m.folder, COALESCE(m.thread_id, m.id),
		m.from_addr, m.from_name, m.to_json, m.subject, m.snippet, m.received_at,
		NOT EXISTS (SELECT 1 FROM messages u WHERE u.thread_id = m.thread_id AND u.is_read = 0 AND u.direction = 'in' AND u.folder != 'trash'),
		m.is_starred, m.spam_reason,
		(SELECT COUNT(*) FROM attachments at WHERE at.message_id = m.id),
		(SELECT COUNT(*) FROM messages t WHERE t.thread_id = m.thread_id AND t.folder != 'trash')
		FROM messages m WHERE `+strings.Join(where, " AND ")+` ORDER BY m.received_at DESC, m.id DESC LIMIT ?`, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []messageRow{}
	for rows.Next() {
		var m messageRow
		var toJSON string
		if err := rows.Scan(&m.ID, &m.AccountID, &m.Direction, &m.Folder, &m.ThreadID, &m.FromAddr, &m.FromName, &toJSON,
			&m.Subject, &m.Snippet, &m.ReceivedAt, &m.IsRead, &m.IsStarred, &m.SpamReason, &m.Attachments, &m.ThreadCount); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.Unmarshal([]byte(toJSON), &m.To)
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out, "has_more": len(out) == limit})
}

type attachmentDTO struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	ContentID   string `json:"content_id"`
}

type messageDetail struct {
	messageRow
	MessageIDHdr string          `json:"message_id"`
	References   string          `json:"references"`
	Cc           []string        `json:"cc"`
	Bcc          []string        `json:"bcc"`
	ReplyTo      []string        `json:"reply_to"`
	Text         string          `json:"text"`
	HTML         string          `json:"html"`
	Files        []attachmentDTO `json:"files"`
}

func (a *App) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx := r.Context()
	var threadID int64
	var folder string
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(thread_id, id), folder FROM messages WHERE id = ?`, id).Scan(&threadID, &folder); err != nil {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	trashFilter := "AND folder != 'trash'"
	if folder == "trash" {
		trashFilter = ""
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id, account_id, direction, folder, COALESCE(thread_id, id), from_addr, from_name, to_json,
		subject, snippet, received_at, is_read, is_starred, spam_reason, message_id, refs, cc_json, bcc_json, reply_to_json, text_body, html_body
		FROM messages WHERE (thread_id = ? OR id = ?) `+trashFilter+` ORDER BY received_at ASC, id ASC`, threadID, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var out []*messageDetail
	for rows.Next() {
		m := &messageDetail{}
		var toJ, ccJ, bccJ, rtJ string
		if err := rows.Scan(&m.ID, &m.AccountID, &m.Direction, &m.Folder, &m.ThreadID, &m.FromAddr, &m.FromName, &toJ,
			&m.Subject, &m.Snippet, &m.ReceivedAt, &m.IsRead, &m.IsStarred, &m.SpamReason, &m.MessageIDHdr, &m.References,
			&ccJ, &bccJ, &rtJ, &m.Text, &m.HTML); err != nil {
			rows.Close()
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.Unmarshal([]byte(toJ), &m.To)
		_ = json.Unmarshal([]byte(ccJ), &m.Cc)
		_ = json.Unmarshal([]byte(bccJ), &m.Bcc)
		_ = json.Unmarshal([]byte(rtJ), &m.ReplyTo)
		m.Files = []attachmentDTO{}
		out = append(out, m)
	}
	rows.Close()
	for _, m := range out {
		arows, err := a.db.QueryContext(ctx, `SELECT id, filename, content_type, size, content_id FROM attachments WHERE message_id = ? ORDER BY id`, m.ID)
		if err != nil {
			continue
		}
		for arows.Next() {
			var f attachmentDTO
			if arows.Scan(&f.ID, &f.Filename, &f.ContentType, &f.Size, &f.ContentID) == nil {
				m.Files = append(m.Files, f)
			}
		}
		arows.Close()
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE messages SET is_read = 1 WHERE (thread_id = ? OR id = ?) AND is_read = 0`, threadID, id)
	writeJSON(w, http.StatusOK, map[string]any{"thread_id": threadID, "messages": out})
}

func (a *App) handleUpdateMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		IsRead    *bool   `json:"is_read"`
		IsStarred *bool   `json:"is_starred"`
		Folder    *string `json:"folder"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	var threadID int64
	var fromAddr, direction string
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(thread_id, id), from_addr, direction FROM messages WHERE id = ?`, id).Scan(&threadID, &fromAddr, &direction); err != nil {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	if body.IsRead != nil {
		_, _ = a.db.ExecContext(ctx, `UPDATE messages SET is_read = ? WHERE thread_id = ? OR id = ?`, *body.IsRead, threadID, id)
	}
	if body.IsStarred != nil {
		_, _ = a.db.ExecContext(ctx, `UPDATE messages SET is_starred = ? WHERE id = ?`, *body.IsStarred, id)
	}
	if body.Folder != nil {
		f := *body.Folder
		if !validFolders[f] || f == "sent" {
			writeErr(w, http.StatusBadRequest, "invalid folder")
			return
		}
		// Folder moves act on the whole conversation. Sent copies stay in Sent
		// unless the conversation is trashed.
		q := `UPDATE messages SET folder = ? WHERE (thread_id = ? OR id = ?) AND direction = 'in'`
		if f == "trash" {
			q = `UPDATE messages SET folder = ? WHERE (thread_id = ? OR id = ?)`
		}
		if _, err := a.db.ExecContext(ctx, q, f, threadID, id); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if f == "inbox" && direction == "in" {
			// Restoring from trash/archive must not resurrect trashed sent copies into inbox.
			_, _ = a.db.ExecContext(ctx, `UPDATE messages SET folder = 'sent' WHERE (thread_id = ? OR id = ?) AND direction = 'out'`, threadID, id)
		}
		if direction == "in" {
			switch f {
			case "spam":
				a.upsertRule(ctx, rulePatternFor(fromAddr), "spam")
			case "inbox":
				// "Not spam": drop a blocking rule and whitelist this sender.
				_, _ = a.db.ExecContext(ctx, `DELETE FROM rules WHERE action = 'spam' AND pattern IN (?, ?)`, fromAddr, rulePatternFor(fromAddr))
				a.upsertRule(ctx, strings.ToLower(fromAddr), "inbox")
				_, _ = a.db.ExecContext(ctx, `UPDATE messages SET spam_reason = '' WHERE thread_id = ? OR id = ?`, threadID, id)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) upsertRule(ctx context.Context, pattern, action string) {
	_, _ = a.db.ExecContext(ctx, `INSERT INTO rules(pattern, action, created_at) VALUES(?, ?, ?)
		ON CONFLICT(pattern) DO UPDATE SET action = excluded.action`, pattern, action, nowUnix())
}

// handleDeleteMessage permanently deletes a trashed conversation.
func (a *App) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx := r.Context()
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM messages WHERE folder = 'trash' AND (thread_id = (SELECT COALESCE(thread_id, id) FROM messages WHERE id = ?) OR id = ?)`, id, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var ids []int64
	for rows.Next() {
		var mid int64
		if rows.Scan(&mid) == nil {
			ids = append(ids, mid)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "only messages in Trash can be deleted permanently")
		return
	}
	for _, mid := range ids {
		_, _ = a.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, mid)
		_ = os.RemoveAll(filepath.Join(a.cfg.DataDir, "attachments", itoa(mid)))
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(ids)})
}

func (a *App) handleCounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	folders := map[string]int{}
	rows, err := a.db.QueryContext(ctx, `SELECT folder, COUNT(*) FROM messages WHERE is_read = 0 AND direction = 'in' GROUP BY folder`)
	if err == nil {
		for rows.Next() {
			var f string
			var n int
			if rows.Scan(&f, &n) == nil {
				folders[f] = n
			}
		}
		rows.Close()
	}
	accounts := map[string]int{}
	rows, err = a.db.QueryContext(ctx, `SELECT account_id, COUNT(*) FROM messages WHERE is_read = 0 AND folder = 'inbox' GROUP BY account_id`)
	if err == nil {
		for rows.Next() {
			var id int64
			var n int
			if rows.Scan(&id, &n) == nil {
				accounts[itoa(id)] = n
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders, "accounts": accounts})
}

func (a *App) handleAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var name, ctype, p string
	if err := a.db.QueryRowContext(r.Context(), `SELECT filename, content_type, path FROM attachments WHERE id = ?`, id).Scan(&name, &ctype, &p); err != nil {
		writeErr(w, http.StatusNotFound, "attachment not found")
		return
	}
	// Only files inside DATA_DIR/attachments are served.
	root := filepath.Join(a.cfg.DataDir, "attachments") + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(p), root) {
		writeErr(w, http.StatusNotFound, "attachment not found")
		return
	}
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" && strings.HasPrefix(ctype, "image/") && ctype != "image/svg+xml" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, p)
}

// ─── Send ───────────────────────────────────────────────────────────────────

func (a *App) handleSend(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 45<<20)
	var body struct {
		AccountID   int64            `json:"account_id"`
		From        string           `json:"from"`
		To          []string         `json:"to"`
		Cc          []string         `json:"cc"`
		Bcc         []string         `json:"bcc"`
		Subject     string           `json:"subject"`
		Text        string           `json:"text"`
		HTML        string           `json:"html"`
		ReplyToID   int64            `json:"reply_to_id"`
		Attachments []sendAttachment `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body (attachments limited to ~30 MB)")
		return
	}
	ctx := r.Context()
	acc, err := a.loadAccount(ctx, body.AccountID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "choose an account to send from")
		return
	}
	fromName, fromAddr := splitAddress(body.From)
	to, cc, bcc := cleanAddrs(body.To), cleanAddrs(body.Cc), cleanAddrs(body.Bcc)
	if fromAddr == "" || !strings.Contains(fromAddr, "@") {
		writeErr(w, http.StatusBadRequest, "invalid From address")
		return
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		writeErr(w, http.StatusBadRequest, "add at least one recipient")
		return
	}
	req := sendRequest{From: body.From, To: to, Cc: cc, Bcc: bcc, Subject: body.Subject, Text: body.Text, HTML: body.HTML, Attachments: body.Attachments}
	if len(req.To) == 0 {
		// Resend requires "to"; move the first cc/bcc recipient there.
		if len(req.Cc) > 0 {
			req.To, req.Cc = req.Cc[:1], req.Cc[1:]
		} else {
			req.To, req.Bcc = req.Bcc[:1], req.Bcc[1:]
		}
	}
	var threadID int64
	var inReplyTo, refs string
	if body.ReplyToID > 0 {
		var parentMsgID, parentRefs string
		if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(thread_id, id), message_id, refs FROM messages WHERE id = ? AND account_id = ?`,
			body.ReplyToID, acc.ID).Scan(&threadID, &parentMsgID, &parentRefs); err == nil && parentMsgID != "" {
			inReplyTo = parentMsgID
			refs = strings.TrimSpace(parentRefs + " " + parentMsgID)
			req.Headers = map[string]string{"In-Reply-To": inReplyTo, "References": refs}
		}
	}
	resendID, err := newResend(acc.APIKey).Send(ctx, req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Resend: "+err.Error())
		return
	}
	now := time.Now().Unix()
	res, err := a.db.ExecContext(ctx, `INSERT INTO messages(account_id, resend_id, direction, folder, thread_id, in_reply_to, refs,
		from_addr, from_name, to_json, cc_json, bcc_json, subject, subject_norm, text_body, html_body, snippet, received_at, is_read, created_at)
		VALUES(?, ?, 'out', 'sent', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		acc.ID, "out:"+resendID, nullableID(threadID), inReplyTo, refs, fromAddr, fromName, jsonList(req.To), jsonList(req.Cc), jsonList(req.Bcc),
		body.Subject, normalizeSubject(body.Subject), body.Text, body.HTML, makeSnippet(body.Text, body.HTML), now, now)
	if err != nil {
		// Mail went out; only the local copy failed.
		log.Printf("send: store sent copy: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": "sent, but the local copy could not be saved"})
		return
	}
	id, _ := res.LastInsertId()
	if threadID == 0 {
		_, _ = a.db.ExecContext(ctx, `UPDATE messages SET thread_id = ? WHERE id = ?`, id, id)
		threadID = id
	}
	for i, att := range body.Attachments {
		data, err := base64.StdEncoding.DecodeString(att.Content)
		if err != nil {
			continue
		}
		if p, err := a.writeAttachment(id, i, data); err == nil {
			_, _ = a.db.ExecContext(ctx, `INSERT INTO attachments(message_id, filename, content_type, size, path) VALUES(?, ?, ?, ?, ?)`,
				id, safeFilename(att.Filename), firstNonEmpty(att.ContentType, "application/octet-stream"), len(data), p)
		}
	}
	a.rememberIdentity(ctx, acc, fromAddr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "thread_id": threadID})
}

func (a *App) rememberIdentity(ctx context.Context, acc *account, addr string) {
	for _, id := range acc.Identities {
		if strings.EqualFold(id, addr) {
			return
		}
	}
	acc.Identities = append(acc.Identities, addr)
	b, _ := json.Marshal(acc.Identities)
	_, _ = a.db.ExecContext(ctx, `UPDATE accounts SET identities = ? WHERE id = ?`, string(b), acc.ID)
}

func cleanAddrs(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, addr := splitAddress(part); addr != "" && strings.Contains(addr, "@") && !seen[addr] {
				seen[addr] = true
				out = append(out, part)
			}
		}
	}
	return out
}

// ─── Rules ──────────────────────────────────────────────────────────────────

func (a *App) handleListRules(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, pattern, action, created_at FROM rules ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type rule struct {
		ID        int64  `json:"id"`
		Pattern   string `json:"pattern"`
		Action    string `json:"action"`
		CreatedAt int64  `json:"created_at"`
	}
	out := []rule{}
	for rows.Next() {
		var ru rule
		if rows.Scan(&ru.ID, &ru.Pattern, &ru.Action, &ru.CreatedAt) == nil {
			out = append(out, ru)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pattern string `json:"pattern"`
		Action  string `json:"action"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	p := strings.ToLower(strings.TrimSpace(body.Pattern))
	if !strings.Contains(p, "@") || (body.Action != "spam" && body.Action != "inbox") {
		writeErr(w, http.StatusBadRequest, "pattern must be an address or @domain; action spam|inbox")
		return
	}
	a.upsertRule(r.Context(), p, body.Action)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	_, _ = a.db.ExecContext(r.Context(), `DELETE FROM rules WHERE id = ?`, id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
