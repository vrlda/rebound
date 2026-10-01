package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie   = "inbox_session"
	sessionTTL      = 30 * 24 * time.Hour
	minPasswordLen  = 12
	failWindow      = 15 * time.Minute
	maxFailsPerIP   = 8
	maxFailsGlobal  = 40
	totpIssuer      = "Rebound"
	totpAccountName = "admin"
)

var (
	errBadCredentials = errors.New("invalid password or 2FA code")
	errRateLimited    = errors.New("too many attempts, try again in 15 minutes")
)

// ─── Setup ──────────────────────────────────────────────────────────────────

func (a *App) setupRequired(ctx context.Context) (bool, error) {
	hash, err := a.getSetting(ctx, "password_hash")
	return hash == "", err
}

// ensureSetupToken creates a one-time setup link when no password exists yet.
// Only its hash is stored; the link is printed to the container log.
func (a *App) ensureSetupToken(ctx context.Context) error {
	required, err := a.setupRequired(ctx)
	if err != nil || !required {
		return err
	}
	// Until setup is completed, every start issues a fresh link (invalidating the
	// previous one), so the newest "SETUP LINK" in the logs is always valid.
	token := randomToken(24)
	if err := a.setSetting(ctx, "setup_token_hash", sha256Hex(token)); err != nil {
		return err
	}
	log.Printf("SETUP LINK (one-time): %s/setup#%s", a.cfg.PublicURL, token)
	return nil
}

func (a *App) resetAuth(ctx context.Context) (string, error) {
	if err := a.deleteSetting(ctx, "password_hash", "totp_secret", "pending_totp", "last_totp_step"); err != nil {
		return "", err
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return "", err
	}
	token := randomToken(24)
	if err := a.setSetting(ctx, "setup_token_hash", sha256Hex(token)); err != nil {
		return "", err
	}
	return a.cfg.PublicURL + "/setup#" + token, nil
}

func (a *App) checkSetupToken(ctx context.Context, token string) bool {
	stored, err := a.getSetting(ctx, "setup_token_hash")
	if err != nil || stored == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(sha256Hex(token))) == 1
}

func (a *App) handleSetupBegin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !a.checkSetupToken(r.Context(), body.Token) {
		writeErr(w, http.StatusForbidden, "setup link is invalid or already used")
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpIssuer, AccountName: totpAccountName})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create 2FA secret")
		return
	}
	sealed, err := a.sealer.Seal(key.Secret())
	if err != nil || a.setSetting(r.Context(), "pending_totp", sealed) != nil {
		writeErr(w, http.StatusInternalServerError, "could not store 2FA secret")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"otpauth_url": key.URL(), "secret": key.Secret()})
}

func (a *App) handleSetupFinish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if !a.checkSetupToken(ctx, body.Token) {
		writeErr(w, http.StatusForbidden, "setup link is invalid or already used")
		return
	}
	if msg := passwordProblem(body.Password); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	sealed, _ := a.getSetting(ctx, "pending_totp")
	secret, err := a.sealer.Open(sealed)
	if sealed == "" || err != nil {
		writeErr(w, http.StatusBadRequest, "start setup again")
		return
	}
	step, ok := matchTOTP(secret, body.Code, time.Now())
	if !ok {
		writeErr(w, http.StatusBadRequest, "2FA code is incorrect — check your authenticator app's clock")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), 12)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	for k, v := range map[string]string{
		"password_hash":  string(hash),
		"totp_secret":    sealed,
		"last_totp_step": strconv.FormatInt(step, 10),
	} {
		if err := a.setSetting(ctx, k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not save credentials")
			return
		}
	}
	_ = a.deleteSetting(ctx, "pending_totp", "setup_token_hash")
	a.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func passwordProblem(p string) string {
	if utf8.RuneCountInString(p) < minPasswordLen {
		return "password must be at least 12 characters"
	}
	if len(p) > 72 {
		return "password must be at most 72 bytes"
	}
	return ""
}

// ─── Login ──────────────────────────────────────────────────────────────────

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	ip := clientIP(r)
	if a.rateLimited(ctx, ip) {
		writeErr(w, http.StatusTooManyRequests, errRateLimited.Error())
		return
	}
	if err := a.verifyCredentials(ctx, body.Password, body.Code); err != nil {
		a.recordFailure(ctx, ip)
		time.Sleep(400 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, errBadCredentials.Error())
		return
	}
	a.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// verifyCredentials checks password + TOTP and consumes the TOTP step so a
// captured code cannot be replayed.
func (a *App) verifyCredentials(ctx context.Context, password, code string) error {
	hash, _ := a.getSetting(ctx, "password_hash")
	if hash == "" {
		return errBadCredentials
	}
	pwErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	sealed, _ := a.getSetting(ctx, "totp_secret")
	secret, err := a.sealer.Open(sealed)
	if err != nil {
		return errBadCredentials
	}
	step, ok := matchTOTP(secret, code, time.Now())
	if pwErr != nil || !ok {
		return errBadCredentials
	}
	lastStr, _ := a.getSetting(ctx, "last_totp_step")
	if last, _ := strconv.ParseInt(lastStr, 10, 64); step <= last {
		return errBadCredentials
	}
	return a.setSetting(ctx, "last_totp_step", strconv.FormatInt(step, 10))
}

// matchTOTP accepts the current 30s step or one step either side and returns
// which step matched.
func matchTOTP(secret, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	for _, offset := range []int64{0, -1, 1} {
		t := now.Add(time.Duration(offset*30) * time.Second)
		ok, _ := totp.ValidateCustom(code, secret, t, totp.ValidateOpts{
			Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
		})
		if ok {
			return t.Unix() / 30, true
		}
	}
	return 0, false
}

func (a *App) rateLimited(ctx context.Context, ip string) bool {
	since := time.Now().Add(-failWindow).Unix()
	_, _ = a.db.ExecContext(ctx, `DELETE FROM login_failures WHERE at < ?`, since)
	var perIP, global int
	_ = a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_failures WHERE ip = ? AND at >= ?`, ip, since).Scan(&perIP)
	_ = a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_failures WHERE at >= ?`, since).Scan(&global)
	return perIP >= maxFailsPerIP || global >= maxFailsGlobal
}

func (a *App) recordFailure(ctx context.Context, ip string) {
	_, _ = a.db.ExecContext(ctx, `INSERT INTO login_failures(ip, at) VALUES(?, ?)`, ip, nowUnix())
	log.Printf("auth: failed login from %s", ip)
}

// ─── Sessions ───────────────────────────────────────────────────────────────

func (a *App) startSession(w http.ResponseWriter, r *http.Request) {
	token := randomToken(32)
	now := time.Now()
	_, err := a.db.ExecContext(r.Context(),
		`INSERT INTO sessions(token_hash, created_at, expires_at, ip, user_agent) VALUES(?, ?, ?, ?, ?)`,
		sha256Hex(token), now.Unix(), now.Add(sessionTTL).Unix(), clientIP(r), truncate(r.UserAgent(), 200))
	if err != nil {
		log.Printf("auth: create session: %v", err)
		return
	}
	a.setSessionCookie(w, token, now.Add(sessionTTL))
}

func (a *App) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (a *App) sessionValid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	var expires int64
	err = a.db.QueryRowContext(r.Context(), `SELECT expires_at FROM sessions WHERE token_hash = ?`, sha256Hex(c.Value)).Scan(&expires)
	return err == nil && expires > nowUnix()
}

// requireAuth guards API routes. Mutating requests must also carry the
// X-Inbox header, which cross-site forms cannot set (CSRF defence on top of
// SameSite=Strict).
func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-Inbox") != "1" {
			writeErr(w, http.StatusForbidden, "missing X-Inbox header")
			return
		}
		if !a.sessionValid(r) {
			writeErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		next(w, r)
	}
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash = ?`, sha256Hex(c.Value))
	}
	a.setSessionCookie(w, "", time.Unix(0, 0))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	_, _ = a.db.ExecContext(r.Context(), `DELETE FROM sessions`)
	a.setSessionCookie(w, "", time.Unix(0, 0))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
		Code    string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	ip := clientIP(r)
	if a.rateLimited(ctx, ip) {
		writeErr(w, http.StatusTooManyRequests, errRateLimited.Error())
		return
	}
	if err := a.verifyCredentials(ctx, body.Current, body.Code); err != nil {
		a.recordFailure(ctx, ip)
		writeErr(w, http.StatusUnauthorized, errBadCredentials.Error())
		return
	}
	if msg := passwordProblem(body.New); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.New), 12)
	if err != nil || a.setSetting(ctx, "password_hash", string(hash)) != nil {
		writeErr(w, http.StatusInternalServerError, "could not update password")
		return
	}
	// Invalidate every other session.
	_, _ = a.db.ExecContext(ctx, `DELETE FROM sessions`)
	a.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleAuthState(w http.ResponseWriter, r *http.Request) {
	required, err := a.setupRequired(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "state unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"setup_required": required, "authenticated": !required && a.sessionValid(r)})
}

// clientIP trusts X-Real-IP because the container is reachable only via the
// internal nginx, which overwrites that header.
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
