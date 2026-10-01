// Rebound — a self-hosted, multi-account inbox for Resend.
//
// Inbound mail is pulled from each connected Resend account's receiving API,
// stored locally (SQLite + attachment files under DATA_DIR), and sent through
// Resend's send API. Access is single-user: password + mandatory TOTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	DataDir       string
	PublicURL     string // e.g. https://mail.example.com (no trailing slash)
	Listen        string
	BootstrapKey  string // optional Resend API key to pre-connect on first boot
	BootstrapName string
	VAPIDSubject  string
	SyncInterval  time.Duration
	SecureCookies bool
}

func loadConfig() Config {
	c := Config{
		DataDir:       env("DATA_DIR", "/data"),
		PublicURL:     strings.TrimRight(env("PUBLIC_URL", "http://localhost:8080"), "/"),
		Listen:        env("LISTEN", ":8080"),
		BootstrapKey:  os.Getenv("BOOTSTRAP_RESEND_KEY"),
		BootstrapName: env("BOOTSTRAP_ACCOUNT_NAME", "Default"),
		VAPIDSubject:  os.Getenv("VAPID_SUBJECT"),
		SyncInterval:  45 * time.Second,
	}
	c.SecureCookies = strings.HasPrefix(c.PublicURL, "https://")
	if c.VAPIDSubject == "" {
		// Push services (Apple especially) need a contact; default to admin@<your host>.
		host := strings.TrimPrefix(strings.TrimPrefix(c.PublicURL, "https://"), "http://")
		if i := strings.IndexAny(host, ":/"); i >= 0 {
			host = host[:i]
		}
		c.VAPIDSubject = "mailto:admin@" + host
	}
	return c
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := loadConfig()
	app, err := newApp(cfg)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer app.db.Close()

	// Recovery: `rebound reset-auth` wipes the password/2FA and prints a fresh setup link.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-auth":
			url, err := app.resetAuth(context.Background())
			if err != nil {
				log.Fatalf("reset-auth: %v", err)
			}
			fmt.Println("Auth reset. Open this link to set a new password and 2FA:")
			fmt.Println(url)
			return
		default:
			log.Fatalf("unknown command %q", os.Args[1])
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.bootstrap(ctx); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}
	go app.syncLoop(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("inbox listening on %s (public %s)", cfg.Listen, cfg.PublicURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}
