package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type syncState struct {
	mu      sync.Mutex
	running map[int64]bool
	kick    chan int64
}

func newSyncState() *syncState {
	return &syncState{running: map[int64]bool{}, kick: make(chan int64, 16)}
}

type account struct {
	ID          int64
	Name        string
	APIKey      string
	Color       string
	Identities  []string
	DefaultFrom string
	Signature   string
	LastSyncAt  sql.NullInt64
	LastError   string
}

func (a *App) loadAccounts(ctx context.Context) ([]*account, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, name, api_key_enc, color, identities, default_from, signature, last_sync_at, last_error FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*account
	for rows.Next() {
		var acc account
		var enc, ids string
		if err := rows.Scan(&acc.ID, &acc.Name, &enc, &acc.Color, &ids, &acc.DefaultFrom, &acc.Signature, &acc.LastSyncAt, &acc.LastError); err != nil {
			return nil, err
		}
		if acc.APIKey, err = a.sealer.Open(enc); err != nil {
			return nil, fmt.Errorf("decrypt key for account %d: %w", acc.ID, err)
		}
		_ = json.Unmarshal([]byte(ids), &acc.Identities)
		out = append(out, &acc)
	}
	return out, rows.Err()
}

func (a *App) loadAccount(ctx context.Context, id int64) (*account, error) {
	accs, err := a.loadAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, acc := range accs {
		if acc.ID == id {
			return acc, nil
		}
	}
	return nil, sql.ErrNoRows
}

// bootstrap issues the setup link and pre-connects the account whose key was
// passed via BOOTSTRAP_RESEND_KEY (only once, so deleting it sticks).
func (a *App) bootstrap(ctx context.Context) error {
	if err := a.ensureSetupToken(ctx); err != nil {
		return err
	}
	if err := a.ensureVAPID(ctx); err != nil {
		return err
	}
	if err := a.reclassifyUnread(ctx, "v2"); err != nil {
		log.Printf("reclassify: %v", err)
	}
	if a.cfg.BootstrapKey == "" {
		return nil
	}
	if done, _ := a.getSetting(ctx, "bootstrap_done"); done != "" {
		return nil
	}
	if _, err := a.createAccount(ctx, a.cfg.BootstrapName, a.cfg.BootstrapKey, "#6d5dfc"); err != nil {
		log.Printf("bootstrap account: %v", err)
		return nil
	}
	return a.setSetting(ctx, "bootstrap_done", "1")
}

func (a *App) createAccount(ctx context.Context, name, key, color string) (int64, error) {
	client := newResend(key)
	if _, _, err := client.ListReceiving(ctx, "", 1); err != nil {
		return 0, fmt.Errorf("Resend rejected this API key for receiving (needs full access): %w", err)
	}
	var identities []string
	if domains, err := client.VerifiedDomains(ctx); err == nil && len(domains) > 0 {
		identities = []string{"hello@" + domains[0]}
	}
	sealed, err := a.sealer.Seal(key)
	if err != nil {
		return 0, err
	}
	idsJSON, _ := json.Marshal(identities)
	defaultFrom := ""
	if len(identities) > 0 {
		defaultFrom = identities[0]
	}
	res, err := a.db.ExecContext(ctx,
		`INSERT INTO accounts(name, api_key_enc, color, identities, default_from, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		name, sealed, color, string(idsJSON), defaultFrom, nowUnix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	a.kickSync(id)
	return id, nil
}

func (a *App) kickSync(accountID int64) {
	select {
	case a.sync.kick <- accountID:
	default:
	}
}

func (a *App) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.SyncInterval)
	defer ticker.Stop()
	a.syncAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.syncAll(ctx)
		case id := <-a.sync.kick:
			if acc, err := a.loadAccount(ctx, id); err == nil {
				a.syncAccount(ctx, acc)
			}
		}
	}
}

func (a *App) syncAll(ctx context.Context) {
	accs, err := a.loadAccounts(ctx)
	if err != nil {
		log.Printf("sync: load accounts: %v", err)
		return
	}
	for _, acc := range accs {
		a.syncAccount(ctx, acc)
	}
}

// syncAccount pulls receiving emails newest-first until it reaches one already
// stored, then ingests the new ones oldest-first so replies thread correctly.
// A failed ingest stops the run so nothing newer is stored past a gap.
func (a *App) syncAccount(ctx context.Context, acc *account) {
	a.sync.mu.Lock()
	if a.sync.running[acc.ID] {
		a.sync.mu.Unlock()
		return
	}
	a.sync.running[acc.ID] = true
	a.sync.mu.Unlock()
	defer func() {
		a.sync.mu.Lock()
		delete(a.sync.running, acc.ID)
		a.sync.mu.Unlock()
	}()

	client := newResend(acc.APIKey)
	var fresh []string
	after := ""
	var syncErr error
pages:
	for page := 0; page < 50; page++ {
		items, hasMore, err := client.ListReceiving(ctx, after, 100)
		if err != nil {
			syncErr = err
			break
		}
		for _, it := range items {
			var exists int
			_ = a.db.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE account_id = ? AND resend_id = ?`, acc.ID, it.ID).Scan(&exists)
			if exists == 1 {
				break pages
			}
			fresh = append(fresh, it.ID)
		}
		if !hasMore || len(items) == 0 {
			break
		}
		after = items[len(items)-1].ID
	}
	for i := len(fresh) - 1; i >= 0 && syncErr == nil; i-- {
		if err := a.ingest(ctx, acc, client, fresh[i]); err != nil {
			syncErr = fmt.Errorf("ingest %s: %w", fresh[i], err)
		}
	}
	errText := ""
	if syncErr != nil {
		errText = syncErr.Error()
		log.Printf("sync account %d (%s): %v", acc.ID, acc.Name, syncErr)
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE accounts SET last_sync_at = ?, last_error = ? WHERE id = ?`, nowUnix(), errText, acc.ID)
}

func (a *App) ingest(ctx context.Context, acc *account, client *Resend, resendID string) error {
	e, err := client.GetReceiving(ctx, resendID)
	if err != nil {
		return err
	}
	fromName, fromAddr := splitAddress(e.From)
	receivedAt := parseResendTime(e.CreatedAt)
	folder, reason := a.classify(ctx, fromAddr, e)
	msgID := firstNonEmpty(e.MessageID, e.header("message-id"))
	inReplyTo := e.header("in-reply-to")
	refs := e.header("references")
	subjectNorm := normalizeSubject(e.Subject)
	threadID := a.findThread(ctx, acc.ID, inReplyTo, refs, subjectNorm, fromAddr)

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages(account_id, resend_id, direction, folder, thread_id, message_id, in_reply_to, refs,
		from_addr, from_name, to_json, cc_json, bcc_json, reply_to_json, subject, subject_norm, text_body, html_body, snippet,
		received_at, spam_reason, created_at) VALUES(?, ?, 'in', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		acc.ID, resendID, folder, nullableID(threadID), msgID, inReplyTo, refs,
		fromAddr, fromName, jsonList(e.To), jsonList(e.Cc), jsonList(e.Bcc), jsonList(e.ReplyTo),
		e.Subject, subjectNorm, e.Text, e.HTML, makeSnippet(e.Text, e.HTML), receivedAt.Unix(), reason, nowUnix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if threadID == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET thread_id = ? WHERE id = ?`, id, id); err != nil {
			return err
		}
	}
	for i, att := range e.Attachments {
		if att.DownloadURL == "" {
			continue
		}
		data, err := client.Download(ctx, att.DownloadURL)
		if err != nil {
			log.Printf("ingest %s: attachment %q: %v", resendID, att.Filename, err)
			continue
		}
		path, err := a.writeAttachment(id, i, data)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments(message_id, filename, content_type, size, content_id, path) VALUES(?, ?, ?, ?, ?, ?)`,
			id, safeFilename(att.Filename), firstNonEmpty(att.ContentType, "application/octet-stream"), len(data), strings.Trim(att.ContentID, "<>"), path); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.learnIdentities(ctx, acc, e)
	if folder == "inbox" {
		go a.notifyNewMail(context.Background(), acc, id, fromName, fromAddr, e.Subject)
	}
	return nil
}

func (a *App) writeAttachment(messageID int64, idx int, data []byte) (string, error) {
	dir := filepath.Join(a.cfg.DataDir, "attachments", fmt.Sprint(messageID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprint(idx))
	return path, os.WriteFile(path, data, 0o600)
}

// learnIdentities records the account's own receiving addresses so they can be
// chosen as From when replying.
func (a *App) learnIdentities(ctx context.Context, acc *account, e *receivedEmail) {
	candidates := e.ReceivedFor
	if len(candidates) == 0 {
		candidates = e.To
	}
	known := map[string]bool{}
	for _, id := range acc.Identities {
		known[strings.ToLower(id)] = true
	}
	changed := false
	for _, c := range candidates {
		_, addr := splitAddress(c)
		if addr != "" && !known[addr] {
			acc.Identities = append(acc.Identities, addr)
			known[addr] = true
			changed = true
		}
	}
	if !changed {
		return
	}
	b, _ := json.Marshal(acc.Identities)
	_, _ = a.db.ExecContext(ctx, `UPDATE accounts SET identities = ? WHERE id = ?`, string(b), acc.ID)
}

// ─── Threading ──────────────────────────────────────────────────────────────

func (a *App) findThread(ctx context.Context, accountID int64, inReplyTo, refs, subjectNorm, counterparty string) int64 {
	ids := strings.Fields(refs)
	if inReplyTo != "" {
		ids = append(ids, inReplyTo)
	}
	for i := len(ids) - 1; i >= 0; i-- {
		var tid sql.NullInt64
		if err := a.db.QueryRowContext(ctx, `SELECT thread_id FROM messages WHERE account_id = ? AND message_id = ? LIMIT 1`, accountID, ids[i]).Scan(&tid); err == nil && tid.Valid {
			return tid.Int64
		}
	}
	if subjectNorm == "" || counterparty == "" {
		return 0
	}
	var tid sql.NullInt64
	err := a.db.QueryRowContext(ctx, `SELECT thread_id FROM messages
		WHERE account_id = ? AND subject_norm = ? AND received_at > ?
		  AND (from_addr = ? OR to_json LIKE ? OR cc_json LIKE ?)
		ORDER BY received_at DESC LIMIT 1`,
		accountID, subjectNorm, time.Now().AddDate(0, 0, -90).Unix(), counterparty, "%"+counterparty+"%", "%"+counterparty+"%").Scan(&tid)
	if err == nil && tid.Valid {
		return tid.Int64
	}
	return 0
}

var subjectPrefix = regexp.MustCompile(`(?i)^\s*((re|fw|fwd|aw|sv|antw)\s*(\[\d+\])?\s*:\s*)+`)

func normalizeSubject(s string) string {
	return strings.ToLower(strings.TrimSpace(subjectPrefix.ReplaceAllString(s, "")))
}

// ─── Spam ───────────────────────────────────────────────────────────────────

// Subjects of the health-scam flood hitting support@ — matched case-insensitively.
var spamPhrases = []string{
	"blood pressure", "alzheimer", "arterial", "knee pain", "knee surgery", "honey", "cinnamon", "dr. oz", "dr oz",
	"vicks", "ozempic", "neuropathy", "brain fog", "tinnitus", "ringing in your ears", "lisinopril", "losartan",
	"amlodipine", "bredesen", "copd", "plaque", "melted", "pounds in", "lbs in", "lecanemab", "infusions",
	"storage access termination", "uploads have failed", "no longer backing up", "new video message",
	"begrenzter bestand", "weight-loss", "weight loss", "memory back", "early onset", "nerve support", "doctors stunned",
	"banned video", "being scrubbed", "without cane", "harvard researcher", "joint toxins", "neural flush",
	"ringing", "buzzing in your ears", "injections", "dr. gupta", "vitalflo", "honestly cried", "nerve health",
}

var freeMailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true,
	"yahoo.com": true, "icloud.com": true, "me.com": true, "proton.me": true, "protonmail.com": true,
	"yandex.ru": true, "mail.ru": true, "aol.com": true, "gmx.com": true,
}

func (a *App) classify(ctx context.Context, from string, e *receivedEmail) (folder, reason string) {
	if action := a.ruleFor(ctx, from); action != "" {
		if action == "spam" {
			return "spam", "your rule"
		}
		return "inbox", ""
	}
	if strings.EqualFold(e.header("x-ses-virus-verdict"), "FAIL") {
		return "spam", "virus verdict"
	}
	if strings.EqualFold(e.header("x-ses-spam-verdict"), "FAIL") {
		return "spam", "spam verdict"
	}
	if reason := spamSubject(e.Subject); reason != "" {
		return "spam", reason
	}
	if a.domainIsSpammer(ctx, from) {
		return "spam", "sender's earlier mail was spam"
	}
	return "inbox", ""
}

func spamSubject(subject string) string {
	subj := strings.ToLower(subject)
	for _, p := range spamPhrases {
		if strings.Contains(subj, p) {
			return "matched “" + p + "”"
		}
	}
	return ""
}

// domainIsSpammer: a non-freemail domain with 2+ messages already in Spam is
// treated as a spam source (these floods rotate subjects, not domains).
func (a *App) domainIsSpammer(ctx context.Context, from string) bool {
	at := strings.LastIndex(from, "@")
	if at < 0 || freeMailDomains[from[at+1:]] {
		return false
	}
	var n int
	_ = a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE folder = 'spam' AND direction = 'in' AND from_addr LIKE ?`, "%"+from[at:]).Scan(&n)
	return n >= 2
}

// reclassifyUnread re-runs spam checks over unread Inbox mail once per filter
// version, so filter improvements also clean up mail that already arrived.
// Mail the user has read or explicitly allowed is never moved.
func (a *App) reclassifyUnread(ctx context.Context, version string) error {
	if done, _ := a.getSetting(ctx, "reclassified"); done == version {
		return nil
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id, from_addr, subject FROM messages WHERE folder = 'inbox' AND direction = 'in' AND is_read = 0 ORDER BY received_at`)
	if err != nil {
		return err
	}
	type cand struct {
		id            int64
		from, subject string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.from, &c.subject) == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	moved := 0
	for _, c := range cands {
		if a.ruleFor(ctx, c.from) == "inbox" {
			continue
		}
		reason := spamSubject(c.subject)
		if reason == "" && a.domainIsSpammer(ctx, c.from) {
			reason = "sender's earlier mail was spam"
		}
		if reason != "" {
			if _, err := a.db.ExecContext(ctx, `UPDATE messages SET folder = 'spam', spam_reason = ? WHERE id = ?`, reason, c.id); err == nil {
				moved++
			}
		}
	}
	log.Printf("reclassify %s: moved %d of %d unread inbox messages to spam", version, moved, len(cands))
	return a.setSetting(ctx, "reclassified", version)
}

// ruleFor returns the action of an exact-address rule, else a domain rule.
func (a *App) ruleFor(ctx context.Context, from string) string {
	from = strings.ToLower(from)
	var action string
	if err := a.db.QueryRowContext(ctx, `SELECT action FROM rules WHERE pattern = ?`, from).Scan(&action); err == nil {
		return action
	}
	if at := strings.LastIndex(from, "@"); at >= 0 {
		if err := a.db.QueryRowContext(ctx, `SELECT action FROM rules WHERE pattern = ?`, from[at:]).Scan(&action); err == nil {
			return action
		}
	}
	return ""
}

// rulePatternFor blocks a whole domain unless it is a shared mailbox provider.
func rulePatternFor(addr string) string {
	addr = strings.ToLower(addr)
	at := strings.LastIndex(addr, "@")
	if at < 0 || freeMailDomains[addr[at+1:]] {
		return addr
	}
	return addr[at:]
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func splitAddress(s string) (name, addr string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if parsed, err := mail.ParseAddress(s); err == nil {
		return parsed.Name, strings.ToLower(parsed.Address)
	}
	return "", strings.ToLower(strings.Trim(s, "<> "))
}

func jsonList(l []string) string {
	if l == nil {
		l = []string{}
	}
	b, _ := json.Marshal(l)
	return string(b)
}

var (
	tagRe   = regexp.MustCompile(`(?s)<(script|style|head)[^>]*>.*?</(script|style|head)>|<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

func makeSnippet(text, htmlBody string) string {
	s := text
	if strings.TrimSpace(s) == "" {
		s = html.UnescapeString(tagRe.ReplaceAllString(htmlBody, " "))
	}
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > 180 {
		s = string(r[:180])
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

var unsafeFileChars = regexp.MustCompile(`[^\w.\- ()]+`)

func safeFilename(name string) string {
	name = unsafeFileChars.ReplaceAllString(filepath.Base(name), "_")
	if name == "" || name == "." || name == "_" {
		return "attachment"
	}
	return truncate(name, 120)
}
