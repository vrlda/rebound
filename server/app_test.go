package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := Config{DataDir: t.TempDir(), PublicURL: "https://mail.test", SyncInterval: time.Hour}
	app, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.db.Close() })
	return app
}

func call(t *testing.T, h http.Handler, method, path string, body any, cookies []*http.Cookie, csrfHeader bool) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Real-IP", "203.0.113.7")
	if csrfHeader {
		req.Header.Set("X-Inbox", "1")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The whole point of the app is that nobody but the owner reads this mail:
// setup must be one-time, login must need both factors, codes must not replay.
func TestSetupAndLoginRequireBothFactors(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	token := randomToken(24)
	if err := app.setSetting(ctx, "setup_token_hash", sha256Hex(token)); err != nil {
		t.Fatal(err)
	}
	h := app.routes()

	if rec := call(t, h, "GET", "/api/accounts", nil, nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API access = %d, want 401", rec.Code)
	}
	if rec := call(t, h, "POST", "/api/auth/setup/begin", map[string]string{"token": "wrong"}, nil, true); rec.Code != http.StatusForbidden {
		t.Fatalf("bad setup token = %d, want 403", rec.Code)
	}
	rec := call(t, h, "POST", "/api/auth/setup/begin", map[string]string{"token": token}, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup begin = %d %s", rec.Code, rec.Body)
	}
	var begin struct{ Secret string }
	_ = json.Unmarshal(rec.Body.Bytes(), &begin)

	setupTime := time.Now().Add(-30 * time.Second) // earlier step, so a fresh code works for login below
	code, _ := totp.GenerateCode(begin.Secret, setupTime)
	if rec := call(t, h, "POST", "/api/auth/setup/finish", map[string]string{"token": token, "password": "short", "code": code}, nil, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password accepted: %d", rec.Code)
	}
	rec = call(t, h, "POST", "/api/auth/setup/finish", map[string]string{"token": token, "password": "correct horse battery", "code": code}, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup finish = %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, "POST", "/api/auth/setup/begin", map[string]string{"token": token}, nil, true); rec.Code != http.StatusForbidden {
		t.Fatalf("setup link reusable after completion: %d", rec.Code)
	}

	now, _ := totp.GenerateCode(begin.Secret, time.Now())
	if rec := call(t, h, "POST", "/api/auth/login", map[string]string{"password": "wrong password!!", "code": now}, nil, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/api/auth/login", map[string]string{"password": "correct horse battery", "code": "000000"}, nil, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong 2FA code = %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/api/auth/login", map[string]string{"password": "correct horse battery", "code": now}, nil, false); rec.Code != http.StatusForbidden {
		t.Fatalf("login without CSRF header = %d, want 403", rec.Code)
	}
	rec = call(t, h, "POST", "/api/auth/login", map[string]string{"password": "correct horse battery", "code": now}, nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid login = %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if rec := call(t, h, "GET", "/api/accounts", nil, cookies, false); rec.Code != http.StatusOK {
		t.Fatalf("authenticated API = %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/api/auth/login", map[string]string{"password": "correct horse battery", "code": now}, nil, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed TOTP code accepted: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/api/rules", map[string]string{"pattern": "@x.com", "action": "spam"}, cookies, false); rec.Code != http.StatusForbidden {
		t.Fatalf("mutation without CSRF header = %d, want 403", rec.Code)
	}
}

func TestLoginLockoutAfterRepeatedFailures(t *testing.T) {
	app := newTestApp(t)
	h := app.routes()
	for i := 0; i < maxFailsPerIP; i++ {
		call(t, h, "POST", "/api/auth/login", map[string]string{"password": "nope nope nope", "code": "123456"}, nil, true)
	}
	if rec := call(t, h, "POST", "/api/auth/login", map[string]string{"password": "x", "code": "1"}, nil, true); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures got %d, want 429", maxFailsPerIP, rec.Code)
	}
}

// Health-scam floods must go to Spam while ordinary business mail stays in Inbox.
func TestClassifySpam(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	cases := []struct {
		from, subject, verdict, want string
	}{
		{"support@pharma-deals.example", "CBS News: Dr. Oz reveals the 4-second high blood pressure flush", "PASS", "spam"},
		{"support@miracle-cure.example", "Forgetting names and passwords? Try this 15-second Vicks protocol", "PASS", "spam"},
		{"sales@acme.example", "Partnership inquiry — Acme", "PASS", "inbox"},
		{"sales@acme.example", "Invoice #1042 question", "PASS", "inbox"},
		{"x@y.com", "Hello", "FAIL", "spam"},
	}
	for _, c := range cases {
		e := &receivedEmail{Subject: c.subject, Headers: map[string]json.RawMessage{"x-ses-spam-verdict": json.RawMessage(`"` + c.verdict + `"`)}}
		if got, _ := app.classify(ctx, c.from, e); got != c.want {
			t.Errorf("classify(%q) = %s, want %s", c.subject, got, c.want)
		}
	}
	// Rotating subjects from a domain that already spammed twice → spam.
	for i := 0; i < 2; i++ {
		_, _ = app.db.Exec(`INSERT INTO accounts(id, name, api_key_enc, created_at) VALUES(1, 'a', 'x', 0) ON CONFLICT DO NOTHING`)
		_, _ = app.db.Exec(`INSERT INTO messages(account_id, direction, folder, from_addr, received_at, created_at) VALUES(1, 'in', 'spam', 'support@spammy.example', 0, 0)`)
	}
	if got, _ := app.classify(ctx, "other@spammy.example", &receivedEmail{Subject: "Photos are safe"}); got != "spam" {
		t.Errorf("repeat spammer domain not caught: %s", got)
	}
	if got, _ := app.classify(ctx, "friend@gmail.com", &receivedEmail{Subject: "Lunch?"}); got != "inbox" {
		t.Errorf("freemail sender misfiled: %s", got)
	}
	app.upsertRule(ctx, "sales@acme.example", "spam")
	if got, _ := app.classify(ctx, "sales@acme.example", &receivedEmail{Subject: "Hi"}); got != "spam" {
		t.Errorf("user spam rule ignored: %s", got)
	}
	app.upsertRule(ctx, "support@pharma-deals.example", "inbox")
	if got, _ := app.classify(ctx, "support@pharma-deals.example", &receivedEmail{Subject: "blood pressure"}); got != "inbox" {
		t.Errorf("whitelist rule ignored: %s", got)
	}
}

func TestHelpers(t *testing.T) {
	if got := normalizeSubject("Re: RE: Fwd: Business inquiry"); got != "business inquiry" {
		t.Errorf("normalizeSubject = %q", got)
	}
	if got := rulePatternFor("Spammer@Evil.com"); got != "@evil.com" {
		t.Errorf("rulePatternFor corporate = %q", got)
	}
	if got := rulePatternFor("someone@gmail.com"); got != "someone@gmail.com" {
		t.Errorf("must not block all of gmail: %q", got)
	}
	if got := cleanAddrs([]string{"a@x.com, Bob <b@y.com>", "a@x.com", "not-an-email"}); len(got) != 2 {
		t.Errorf("cleanAddrs = %v", got)
	}
	if name, addr := splitAddress(`"Alex" <Alex.Doe@Gmail.com>`); name != "Alex" || addr != "alex.doe@gmail.com" {
		t.Errorf("splitAddress = %q %q", name, addr)
	}
	if !strings.Contains(safeFilename("../../etc/passwd"), "passwd") || strings.Contains(safeFilename("../../etc/passwd"), "/") {
		t.Errorf("safeFilename path traversal: %q", safeFilename("../../etc/passwd"))
	}
}
