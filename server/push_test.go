package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Apple rejects the VAPID JWT (403 BadJwtToken) unless "sub" is exactly one
// mailto: URL; webpush-go prefixes mailto: itself, so a configured
// "mailto:x" must not become "mailto:mailto:x".
func TestPushJWTSubjectHasSingleMailto(t *testing.T) {
	var sub string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization") // "vapid t=<jwt>, k=<key>"
		jwt := strings.TrimPrefix(strings.Split(auth, ",")[0], "vapid t=")
		parts := strings.Split(jwt, ".")
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct{ Sub string }
		_ = json.Unmarshal(payload, &claims)
		sub = claims.Sub
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	app := newTestApp(t)
	app.cfg.VAPIDSubject = "mailto:admin@example.com"
	ctx := context.Background()
	if err := app.ensureVAPID(ctx); err != nil {
		t.Fatal(err)
	}
	// Browser-style subscription keys (RFC 8291 test vector).
	_, _ = app.db.Exec(`INSERT INTO push_subs(endpoint, p256dh, auth, created_at) VALUES(?, ?, ?, 0)`,
		srv.URL+"/push", "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM", "tBHItJI5svbpez7KI4CCXg")
	if n := app.sendPush(ctx, map[string]any{"title": "t"}); n != 1 {
		t.Fatalf("delivered = %d, want 1", n)
	}
	if sub != "mailto:admin@example.com" {
		t.Fatalf("JWT sub = %q, want mailto:admin@example.com", sub)
	}
}
