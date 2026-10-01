package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func (a *App) ensureVAPID(ctx context.Context) error {
	if pub, _ := a.getSetting(ctx, "vapid_public"); pub != "" {
		return nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	sealed, err := a.sealer.Seal(priv)
	if err != nil {
		return err
	}
	if err := a.setSetting(ctx, "vapid_private", sealed); err != nil {
		return err
	}
	return a.setSetting(ctx, "vapid_public", pub)
}

func (a *App) handlePushKey(w http.ResponseWriter, r *http.Request) {
	pub, _ := a.getSetting(r.Context(), "vapid_public")
	writeJSON(w, http.StatusOK, map[string]string{"public_key": pub})
}

func (a *App) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Endpoint == "" || body.Keys.P256dh == "" || body.Keys.Auth == "" {
		writeErr(w, http.StatusBadRequest, "incomplete subscription")
		return
	}
	_, err := a.db.ExecContext(r.Context(), `INSERT INTO push_subs(endpoint, p256dh, auth, user_agent, created_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth`,
		body.Endpoint, body.Keys.P256dh, body.Keys.Auth, truncate(r.UserAgent(), 200), nowUnix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handlePushTest(w http.ResponseWriter, r *http.Request) {
	n := a.sendPush(r.Context(), map[string]any{"title": "Rebound", "body": "Notifications are working.", "url": "/"})
	writeJSON(w, http.StatusOK, map[string]int{"delivered": n})
}

func (a *App) notifyNewMail(ctx context.Context, acc *account, messageID int64, fromName, fromAddr, subject string) {
	title := fromName
	if title == "" {
		title = fromAddr
	}
	a.sendPush(ctx, map[string]any{
		"title": title,
		"body":  firstNonEmpty(subject, "(no subject)") + " · " + acc.Name,
		"url":   "/m/" + itoa(messageID),
		"tag":   "msg-" + itoa(messageID),
	})
}

// sendPush delivers to every subscribed device and prunes expired ones.
func (a *App) sendPush(ctx context.Context, payload map[string]any) int {
	pub, _ := a.getSetting(ctx, "vapid_public")
	sealed, _ := a.getSetting(ctx, "vapid_private")
	priv, err := a.sealer.Open(sealed)
	if pub == "" || err != nil {
		return 0
	}
	body, _ := json.Marshal(payload)
	rows, err := a.db.QueryContext(ctx, `SELECT id, endpoint, p256dh, auth FROM push_subs`)
	if err != nil {
		return 0
	}
	type sub struct {
		id                     int64
		endpoint, p256dh, auth string
	}
	var subs []sub
	for rows.Next() {
		var s sub
		if rows.Scan(&s.id, &s.endpoint, &s.p256dh, &s.auth) == nil {
			subs = append(subs, s)
		}
	}
	rows.Close()
	delivered := 0
	for _, s := range subs {
		resp, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
			Endpoint: s.endpoint, Keys: webpush.Keys{P256dh: s.p256dh, Auth: s.auth},
		}, &webpush.Options{Subscriber: vapidSubscriber(a.cfg.VAPIDSubject), VAPIDPublicKey: pub, VAPIDPrivateKey: priv, TTL: 86400, Urgency: webpush.UrgencyHigh})
		if err != nil {
			log.Printf("push: %v", err)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
			_, _ = a.db.ExecContext(ctx, `DELETE FROM push_subs WHERE id = ?`, s.id)
		case resp.StatusCode < 300:
			delivered++
		default:
			log.Printf("push: endpoint returned %d: %s", resp.StatusCode, readSnippet(resp))
		}
		resp.Body.Close()
	}
	return delivered
}

// vapidSubscriber strips a "mailto:" prefix: webpush-go adds its own, and a
// doubled "mailto:mailto:" subject makes Apple's push service reject every
// notification with 403 BadJwtToken (Chrome/Firefox tolerate it).
func vapidSubscriber(subject string) string {
	return strings.TrimPrefix(strings.TrimSpace(subject), "mailto:")
}

func readSnippet(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return strings.TrimSpace(string(b))
}
