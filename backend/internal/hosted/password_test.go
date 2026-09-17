package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"orchestrator/backend/internal/email"
)

func TestPasswordHash(t *testing.T) {
	password := "a sufficiently long password"
	first, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashPassword(password)
	if err != nil || first == second {
		t.Fatal("password salts must be unique")
	}
	if !checkPassword(first, password) || checkPassword(first, "incorrect password") || checkPassword("", password) || checkPassword("broken", password) {
		t.Fatal("incorrect password verification")
	}
	if _, err := hashPassword("too short"); err == nil {
		t.Fatal("accepted short password")
	}
	if _, err := hashPassword(strings.Repeat("a", 1025)); err == nil {
		t.Fatal("accepted unbounded password")
	}
}
func TestNormalizeEmail(t *testing.T) {
	if value, err := normalizeEmail(" Alice@Example.com "); err != nil || value != "alice@example.com" {
		t.Fatal(value, err)
	}
	for _, value := range []string{"", "Alice <alice@example.com>", "alice@example.com\r\nBcc: bob@example.com", "not-an-address"} {
		if _, err := normalizeEmail(value); err == nil {
			t.Fatal("accepted invalid email")
		}
	}
}

type captureAccountMail struct{ messages []email.Message }

func (m *captureAccountMail) Send(_ context.Context, message email.Message) error {
	m.messages = append(m.messages, message)
	return nil
}
func (m *captureAccountMail) token(t *testing.T) string {
	t.Helper()
	if len(m.messages) == 0 {
		t.Fatal("no email sent")
	}
	fields := strings.Split(m.messages[len(m.messages)-1].Body, "#email_token=")
	if len(fields) != 2 {
		t.Fatal("no verification link")
	}
	return strings.Fields(fields[1])[0]
}

func TestPostgresEmailAccounts(t *testing.T) {
	dsn := os.Getenv("ORCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ORCH_TEST_DATABASE_URL is required; use a disposable database")
	}
	ctx := context.Background()
	accounts, err := NewAccounts(ctx, dsn, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	address := "email-" + randomToken() + "@example.com"
	defer func() {
		_, _ = accounts.db.ExecContext(ctx, `DELETE FROM hosted.sessions WHERE user_id IN (SELECT id FROM hosted.users WHERE email=$1)`, address)
		_, _ = accounts.db.ExecContext(ctx, `DELETE FROM hosted.users WHERE email=$1`, address)
		_, _ = accounts.db.ExecContext(ctx, `DELETE FROM hosted.email_tokens WHERE email=$1`, address)
	}()
	mailer := &captureAccountMail{}
	server := NewServer(accounts, nil, GoogleAuth{Origin: "https://jobs.example", ClientID: "test"}, "", slog.New(slog.NewTextHandler(io.Discard, nil)), mailer)
	request := func(path string, body map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(data)))
		r.Header.Set("Origin", "https://jobs.example")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	w := request("/auth/email/signup", map[string]string{"email": strings.ToUpper(address)})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	token := mailer.token(t)
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("email token exposed in response")
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("unverified signup issued session")
	}
	if _, _, err = accounts.PasswordLogin(ctx, address, "a sufficiently long password"); !errors.Is(err, errCredentials) {
		t.Fatal("unverified account could sign in")
	}
	var count int
	if err = accounts.db.QueryRowContext(ctx, `SELECT count(*) FROM hosted.users WHERE email=$1`, address).Scan(&count); err != nil || count != 0 {
		t.Fatal("unverified signup consumed capacity", err)
	}
	if _, _, err = accounts.CompleteEmail(ctx, token, "short", "Alice"); err == nil {
		t.Fatal("weak password accepted")
	}
	w = request("/auth/email/complete", map[string]string{"token": token, "password": "a sufficiently long password", "name": "Alice"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var user User
	if err = json.Unmarshal(w.Body.Bytes(), &user); err != nil || user.ID == "" {
		t.Fatal("missing account")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie missing protections")
	}
	oldSession := cookies[0].Value
	if _, _, err = accounts.CompleteEmail(ctx, token, "a sufficiently long password", "Alice"); !errors.Is(err, errEmailLink) {
		t.Fatal("verification token reused", err)
	}
	w = request("/auth/email/login", map[string]string{"email": address, "password": "incorrect password"})
	if w.Code != 401 {
		t.Fatal("bad password accepted")
	}
	w = request("/auth/email/login", map[string]string{"email": address, "password": "a sufficiently long password"})
	if w.Code != 200 {
		t.Fatal("password login failed", w.Body.String())
	}
	// A verified Google identity opens the same workspace, retaining its password.
	googleUser, googleSession, err := accounts.Login(ctx, "google-"+user.ID, address, "Alice Google")
	if err != nil || googleUser.ID != user.ID {
		t.Fatal("Google created a duplicate workspace", err)
	}
	if again, _, err := accounts.PasswordLogin(ctx, address, "a sufficiently long password"); err != nil || again.ID != user.ID {
		t.Fatal("linking lost password access", err)
	}
	// Use the store directly to avoid the intentional per-address HTTP rate limit.
	reset, err := accounts.BeginEmail(ctx, address, "reset")
	if err != nil || reset == "" {
		t.Fatal("reset unavailable", err)
	}
	expired, err := accounts.BeginEmail(ctx, address, "reset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = accounts.db.ExecContext(ctx, `UPDATE hosted.email_tokens SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, tokenHash(expired)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = accounts.CompleteEmail(ctx, expired, "another sufficiently long password", ""); !errors.Is(err, errEmailLink) {
		t.Fatal("expired reset accepted", err)
	}
	restored, newSession, err := accounts.CompleteEmail(ctx, reset, "another sufficiently long password", "")
	if err != nil || restored.ID != user.ID || restored.Name != "Alice" {
		t.Fatal("reset lost identity", err)
	}
	for _, revoked := range []string{oldSession, googleSession} {
		if _, err = accounts.Session(ctx, revoked); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("reset did not revoke session", err)
		}
	}
	if _, err = accounts.Session(ctx, newSession); err != nil {
		t.Fatal("new session invalid", err)
	}
	if _, _, err = accounts.PasswordLogin(ctx, address, "a sufficiently long password"); !errors.Is(err, errCredentials) {
		t.Fatal("old password accepted")
	}
	if _, _, err = accounts.CompleteEmail(ctx, reset, "yet another long password", ""); !errors.Is(err, errEmailLink) {
		t.Fatal("reset token reused", err)
	}
	if _, _, err = accounts.PasswordLogin(ctx, address, "another sufficiently long password"); err != nil {
		t.Fatal("new password rejected", err)
	}
	// Tokens cannot bypass exact-origin protection, even when all fields are valid.
	r := httptest.NewRequest(http.MethodPost, "/auth/email/complete", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin auth accepted")
	}
}
