package hosted

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testAccounts struct{ sessions map[string]User }

func (a *testAccounts) Login(context.Context, string, string, string) (User, string, error) {
	return User{}, "", nil
}
func (a *testAccounts) Session(_ context.Context, token string) (User, error) {
	u, ok := a.sessions[token]
	if !ok {
		return u, sql.ErrNoRows
	}
	return u, nil
}
func (a *testAccounts) Logout(_ context.Context, token string) error {
	delete(a.sessions, token)
	return nil
}

type testRuntimes struct{ seen []string }

func (m *testRuntimes) Get(u User) (*Runtime, error) {
	m.seen = append(m.seen, u.ID)
	return &Runtime{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Orchestrator-Owner") != u.ID {
			panic("owner was not replaced")
		}
		jsonResponse(w, 200, map[string]string{"workspace": u.ID})
	})}, nil
}
func TestHostedSessionBoundary(t *testing.T) {
	a := &testAccounts{map[string]User{"alice-token": {ID: "alice"}, "bob-token": {ID: "bob"}}}
	m := &testRuntimes{}
	s := &Server{accounts: a, runtimes: m, google: GoogleAuth{Origin: "https://jobs.example"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	security := httptest.NewRecorder()
	s.ServeHTTP(security, httptest.NewRequest("GET", "/healthz", nil))
	for header, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), payment=()",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	} {
		if got := security.Header().Get(header); got != want {
			t.Fatalf("security header %s = %q, want %q", header, got, want)
		}
	}
	if got := security.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("HSTS = %q", got)
	}
	for _, path := range []string{"/v1/jobs", "/v1/events", "/v1/postings/job-id", "/v1/results", "/v1/workflows", "/metrics", "/auth/chatgpt"} {
		r := httptest.NewRequest("GET", path+"?auth_token=alice-token", nil)
		r.Header.Set("Authorization", "Bearer alice-token")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	if len(m.seen) != 0 {
		t.Fatal("unauthenticated request reached workspace")
	}
	for _, token := range []string{"alice-token", "bob-token"} {
		r := httptest.NewRequest("GET", "/v1/jobs?owner=bob&submitted_by=bob", nil)
		r.AddCookie(&http.Cookie{Name: "orch_session", Value: token})
		r.Header.Set("X-Orchestrator-Owner", "attacker")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), a.sessions[token].ID) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, origin := range []string{"", "https://evil.example", "https://jobs.example"} {
		r := httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		r.AddCookie(&http.Cookie{Name: "orch_session", Value: "alice-token"})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		want := 403
		if origin == "https://jobs.example" {
			want = 200
		}
		if w.Code != want {
			t.Fatal(origin, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/auth/logout", nil)
	r.Header.Set("Origin", "https://jobs.example")
	r.AddCookie(&http.Cookie{Name: "orch_session", Value: "alice-token"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := a.Session(context.Background(), "alice-token"); err == nil {
		t.Fatal("logout did not revoke session")
	}
	if _, err := a.Session(context.Background(), "bob-token"); err != nil {
		t.Fatal("logout revoked other account")
	}
}
func TestWorkspaceSelection(t *testing.T) {
	for _, id := range []string{"", "public", "../other", "abc\"; DROP SCHEMA public;--"} {
		if _, err := workspaceDSN("postgres://localhost/db", id); err == nil {
			t.Fatal("accepted", id)
		}
	}
	dsn, err := workspaceDSN("postgres://localhost/db?search_path=public", strings.Repeat("a", 32))
	if err != nil || strings.Contains(dsn, "search_path=public") || !strings.Contains(dsn, "workspace_") {
		t.Fatal(dsn, err)
	}
}
