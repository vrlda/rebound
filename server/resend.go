package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// resendBase is overridable (RESEND_API_BASE) so the client can be exercised
// against a local mock; production always uses the real API.
var resendBase = env("RESEND_API_BASE", "https://api.resend.com")

type Resend struct {
	key  string
	http *http.Client
}

func newResend(key string) *Resend {
	return &Resend{key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

type resendError struct {
	Status  int
	Message string
}

func (e *resendError) Error() string { return fmt.Sprintf("resend %d: %s", e.Status, e.Message) }

func (c *Resend) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, resendBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", "rebound/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(raw))
		}
		return &resendError{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// strList decodes a JSON string, array of strings, or null.
type strList []string

func (s *strList) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one != "" {
			*s = []string{one}
		}
		return nil
	}
	*s = nil
	return nil
}

type receivedSummary struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

func (c *Resend) ListReceiving(ctx context.Context, after string, limit int) ([]receivedSummary, bool, error) {
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	var out struct {
		Data    []receivedSummary `json:"data"`
		HasMore bool              `json:"has_more"`
	}
	err := c.do(ctx, http.MethodGet, "/emails/receiving?"+q.Encode(), nil, &out)
	return out.Data, out.HasMore, err
}

type receivedAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	ContentID   string `json:"content_id"`
	DownloadURL string `json:"download_url"`
}

type receivedEmail struct {
	ID          string                     `json:"id"`
	From        string                     `json:"from"`
	To          strList                    `json:"to"`
	Cc          strList                    `json:"cc"`
	Bcc         strList                    `json:"bcc"`
	ReplyTo     strList                    `json:"reply_to"`
	ReceivedFor strList                    `json:"received_for"`
	Subject     string                     `json:"subject"`
	HTML        string                     `json:"html"`
	Text        string                     `json:"text"`
	CreatedAt   string                     `json:"created_at"`
	MessageID   string                     `json:"message_id"`
	Headers     map[string]json.RawMessage `json:"headers"`
	Attachments []receivedAttachment       `json:"attachments"`
}

// header returns the first value of a header (Resend gives string or array).
func (e *receivedEmail) header(name string) string {
	raw, ok := e.Headers[strings.ToLower(name)]
	if !ok {
		return ""
	}
	var l strList
	_ = l.UnmarshalJSON(raw)
	if len(l) == 0 {
		return ""
	}
	return strings.TrimSpace(l[0])
}

func (c *Resend) GetReceiving(ctx context.Context, id string) (*receivedEmail, error) {
	var out receivedEmail
	err := c.do(ctx, http.MethodGet, "/emails/receiving/"+url.PathEscape(id), nil, &out)
	return &out, err
}

func (c *Resend) Download(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("download status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 40<<20))
}

type sendAttachment struct {
	Filename    string `json:"filename"`
	Content     string `json:"content"` // base64
	ContentType string `json:"content_type,omitempty"`
}

type sendRequest struct {
	From        string            `json:"from"`
	To          []string          `json:"to"`
	Cc          []string          `json:"cc,omitempty"`
	Bcc         []string          `json:"bcc,omitempty"`
	ReplyTo     []string          `json:"reply_to,omitempty"`
	Subject     string            `json:"subject"`
	HTML        string            `json:"html,omitempty"`
	Text        string            `json:"text,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []sendAttachment  `json:"attachments,omitempty"`
}

func (c *Resend) Send(ctx context.Context, req sendRequest) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/emails", req, &out)
	return out.ID, err
}

// VerifiedDomains lists domains that can send (used to validate From addresses).
func (c *Resend) VerifiedDomains(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/domains", nil, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, d := range out.Data {
		if d.Status == "verified" {
			names = append(names, strings.ToLower(d.Name))
		}
	}
	return names, nil
}

func parseResendTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999+00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Now()
}
