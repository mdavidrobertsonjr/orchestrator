// hosted serves the public account-based website. The private API binary remains
// available for existing single-owner deployments.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"orchestrator/backend/internal/email"
	"orchestrator/backend/internal/hosted"
)

func main() {
	if err := run(); err != nil {
		slog.Error("hosted server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	origin := strings.TrimRight(os.Getenv("ORCH_PUBLIC_URL"), "/")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("ORCH_PUBLIC_URL must be a website origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return errors.New("ORCH_PUBLIC_URL must use HTTPS (HTTP is allowed only on localhost)")
	}
	dsn := os.Getenv("ORCH_DATABASE_URL")
	if dsn == "" {
		return errors.New("ORCH_DATABASE_URL is required for durable hosted accounts")
	}
	id, secret := os.Getenv("ORCH_GOOGLE_CLIENT_ID"), os.Getenv("ORCH_GOOGLE_CLIENT_SECRET")
	if (id == "") != (secret == "") {
		return errors.New("set both Google web OAuth client ID and secret")
	}
	var mailer email.Sender
	mail := hosted.AuthMail{Host: os.Getenv("ORCH_AUTH_SMTP_HOST"), Port: os.Getenv("ORCH_AUTH_SMTP_PORT"), Username: os.Getenv("ORCH_AUTH_SMTP_USERNAME"), Password: os.Getenv("ORCH_AUTH_SMTP_PASSWORD"), From: os.Getenv("ORCH_AUTH_SMTP_FROM")}
	if mail.Host != "" {
		if mail.Port == "" {
			mail.Port = "587"
		}
		if mail.Username == "" || mail.Password == "" || mail.From == "" {
			return errors.New("account email requires SMTP username, password, and from address")
		}
		mailer = mail
	}
	if id == "" && mailer == nil {
		return errors.New("configure Google sign-in or account SMTP before starting hosted mode")
	}
	root := os.Getenv("ORCH_USER_DATA_DIR")
	if !filepath.IsAbs(root) {
		return errors.New("ORCH_USER_DATA_DIR must be an absolute persistent directory")
	}
	binary := os.Getenv("ORCH_CODEX_BINARY")
	if binary == "" {
		binary = "codex"
	}
	binary, err = exec.LookPath(binary)
	if err != nil {
		return errors.New("install Codex on the hosting server before starting hosted mode")
	}
	maxUsers := 5
	if raw := os.Getenv("ORCH_MAX_USERS"); raw != "" {
		maxUsers, err = strconv.Atoi(raw)
		if err != nil || maxUsers < 1 || maxUsers > 25 {
			return errors.New("ORCH_MAX_USERS must be between 1 and 25")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := hosted.NewAccounts(ctx, dsn, maxUsers)
	if err != nil {
		return err
	}
	defer a.Close()
	release, err := a.AcquireHost(ctx)
	if err != nil {
		return err
	}
	defer release()
	logger := slog.Default()
	runtimes := hosted.NewRuntimes(ctx, a, dsn, root, binary, os.Getenv("ORCH_CODEX_MODEL"), logger, mailer)
	defer runtimes.Close()
	if err := runtimes.Restore(ctx); err != nil {
		return err
	}
	static := os.Getenv("ORCH_STATIC_DIR")
	if static == "" {
		static = "../frontend/dist"
	}
	handler := hosted.NewServer(a, runtimes, hosted.GoogleAuth{ClientID: id, ClientSecret: secret, Origin: origin}, static, logger, mailer)
	addr := os.Getenv("ORCH_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 3 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	failure := make(chan error, 1)
	go func() {
		logger.Info("hosted website listening", "addr", addr, "origin", origin)
		failure <- server.ListenAndServe()
	}()
	select {
	case err := <-failure:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve website: %w", err)
		}
	case <-ctx.Done():
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
