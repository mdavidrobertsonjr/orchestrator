package hosted

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type accountStore interface {
	Login(context.Context, string, string, string) (User, string, error)
	Session(context.Context, string) (User, error)
	Logout(context.Context, string) error
}
type runtimeStore interface{ Get(User) (*Runtime, error) }
type Server struct {
	accounts accountStore
	runtimes runtimeStore
	google   GoogleAuth
	static   string
	logger   *slog.Logger
	limits   limiter
}

func NewServer(a *Accounts, r *Runtimes, g GoogleAuth, static string, logger *slog.Logger) http.Handler {
	return &Server{accounts: a, runtimes: r, google: g, static: static, logger: logger}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if strings.HasPrefix(s.google.Origin, "https://") {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if !sameOrigin(r, s.google.Origin) {
			fail(w, 403, "request must come from this website")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	}
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		if r.Method != "GET" {
			fail(w, 405, "method not allowed")
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/auth/config" {
		jsonResponse(w, 200, map[string]any{"hosted": true, "provider": "google"})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/auth/google" {
		if !s.limits.allow("login", 120) {
			fail(w, 429, "too many sign-in attempts; try again shortly")
			return
		}
		s.google.Start(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/auth/google/callback" {
		if !s.limits.allow("callback", 120) {
			fail(w, 429, "too many sign-in attempts; try again shortly")
			return
		}
		identity, err := s.google.Callback(w, r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		_, token, err := s.accounts.Login(r.Context(), identity.Subject, identity.Email, identity.Name)
		if err != nil {
			s.logger.Error("website account login failed", "error", err)
			fail(w, 503, "sign-in could not complete; the site may be at its signup capacity")
			return
		}
		// Revoke the previous browser session when rotating its cookie.
		if old, err := r.Cookie("orch_session"); err == nil {
			_ = s.accounts.Logout(r.Context(), old.Value)
		}
		setCookie(w, "orch_session", token, 7*24*3600, s.google.Origin)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	protected := strings.HasPrefix(r.URL.Path, "/auth/") || strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/v1" || r.URL.Path == "/metrics"
	if !protected {
		s.serveStatic(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie("orch_session")
	if err != nil {
		fail(w, 401, "sign in to your website account")
		return
	}
	u, err := s.accounts.Session(r.Context(), cookie.Value)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.Error("session lookup failed", "error", err)
		}
		fail(w, 401, "your session expired; sign in again")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/auth/me" {
		jsonResponse(w, 200, u)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/auth/logout" {
		if err := s.accounts.Logout(r.Context(), cookie.Value); err != nil {
			fail(w, 503, "sign-out failed; try again")
			return
		}
		setCookie(w, "orch_session", "", -1, s.google.Origin)
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != "GET" && !s.limits.allow(u.ID, 30) {
		fail(w, 429, "too many requests; try again in a minute")
		return
	}
	runtime, err := s.runtimes.Get(u)
	if err != nil {
		s.logger.Error("workspace unavailable", "user_id", u.ID, "error", err)
		fail(w, 503, "your workspace is temporarily unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	switch {
	case r.Method == "GET" && r.URL.Path == "/auth/chatgpt":
		status, err := runtime.Codex.Account(ctx)
		if err != nil {
			fail(w, 503, "ChatGPT connection unavailable; try again shortly")
			return
		}
		jsonResponse(w, 200, status)
	case r.Method == "POST" && r.URL.Path == "/auth/chatgpt/connect":
		status, err := runtime.Codex.Connect(ctx)
		if err != nil {
			s.logger.Warn("ChatGPT sign-in failed", "user_id", u.ID, "error", err)
			fail(w, 502, "ChatGPT sign-in could not start. Check that device-code sign-in is enabled in your ChatGPT security settings, then try again.")
			return
		}
		jsonResponse(w, 200, status)
	case r.Method == "POST" && r.URL.Path == "/auth/chatgpt/disconnect":
		if err := runtime.Codex.Disconnect(ctx); err != nil {
			fail(w, 502, "ChatGPT disconnect failed; try again")
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	case strings.HasPrefix(r.URL.Path, "/auth/"):
		http.NotFound(w, r)
	default:
		// Only a server-resolved account selects a workspace. Ignore caller-supplied
		// tokens and ownership headers, including legacy tokens left in localStorage.
		r.Header.Del("Authorization")
		r.Header.Del("X-Orchestrator-Token")
		r.Header.Set("X-Orchestrator-Owner", u.ID)
		runtime.Handler.ServeHTTP(w, r)
	}
}
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(w, 405, "method not allowed")
		return
	}
	if s.static == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.static, filepath.Clean("/"+r.URL.Path))
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		http.FileServer(http.Dir(s.static)).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(s.static, "index.html"))
}

type bucket struct {
	start time.Time
	count int
}
type limiter struct {
	mu    sync.Mutex
	items map[string]bucket
}

func (l *limiter) allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.items == nil {
		l.items = map[string]bucket{}
	}
	now := time.Now()
	b := l.items[key]
	if now.Sub(b.start) >= time.Minute {
		b = bucket{start: now}
	}
	b.count++
	l.items[key] = b
	return b.count <= max
}
