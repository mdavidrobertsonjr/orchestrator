package hosted

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"orchestrator/backend/internal/postings"
)

func TestPostgresHosted(t *testing.T) {
	dsn := os.Getenv("ORCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ORCH_TEST_DATABASE_URL is required; use a disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	accounts, err := NewAccounts(ctx, dsn, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	release, err := accounts.AcquireHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if again, err := accounts.AcquireHost(ctx); err == nil {
		again()
		t.Fatal("second hosted instance acquired the lock")
	}
	marker := randomToken()
	alice, aliceToken, err := accounts.Login(ctx, "alice-"+marker, "alice@example.com", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, bobToken, err := accounts.Login(ctx, "bob-"+marker, "bob@example.com", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, u := range []User{alice, bob} {
			_, _ = accounts.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "workspace_`+u.ID+`" CASCADE`)
			_, _ = accounts.db.ExecContext(context.Background(), `DELETE FROM hosted.sessions WHERE user_id=$1`, u.ID)
			_, _ = accounts.db.ExecContext(context.Background(), `DELETE FROM hosted.users WHERE id=$1`, u.ID)
		}
	}()
	again, _, err := accounts.Login(ctx, "alice-"+marker, "alice-new@example.com", "Alice")
	if err != nil || again.ID != alice.ID {
		t.Fatal("account identity did not persist", err)
	}
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runCtx, stop := context.WithCancel(ctx)
	runtimes := NewRuntimes(runCtx, accounts, dsn, root, "/unused-codex", "", logger, nil)
	server := NewServer(accounts, runtimes, GoogleAuth{Origin: "https://jobs.example"}, "", logger)
	request := func(token, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "orch_session", Value: token})
		r.Header.Set("Origin", "https://jobs.example")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	// Identical source IDs must deduplicate within, never across, accounts.
	ids := map[string]string{}
	for _, u := range []User{alice, bob} {
		if _, err := runtimes.Get(u); err != nil {
			t.Fatal(err)
		}
		scoped, _ := workspaceDSN(dsn, u.ID)
		store, err := postings.NewPostgresStore(ctx, scoped)
		if err != nil {
			t.Fatal(err)
		}
		item, _, err := store.Upsert(postings.UpsertPostingParams{Company: "Example", Title: u.Name + " role", URL: "https://example.com/jobs/1", Source: "test", SourceID: "same-source-id"})
		if err != nil {
			t.Fatal(err)
		}
		ids[u.ID] = item.ID
		store.Close()
	}
	w := request(aliceToken, "PATCH", "/v1/postings/"+ids[bob.ID], `{"applied":true}`)
	if w.Code != 404 {
		t.Fatalf("cross-user posting mutation: %d %s", w.Code, w.Body.String())
	}
	w = request(aliceToken, "PATCH", "/v1/postings/"+ids[alice.ID], `{"applied":true}`)
	if w.Code != 200 {
		t.Fatalf("own posting: %d %s", w.Code, w.Body.String())
	}
	w = request(aliceToken, "POST", "/v1/jobs", `{"name":"Alice saved job","type":"ai.inference","payload":{"duration_ms":1}}`)
	if w.Code != 202 {
		t.Fatalf("create job: %d %s", w.Code, w.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatal(w.Body.String())
	}
	w = request(bobToken, "GET", "/v1/jobs/"+job.ID, "")
	if w.Code != 404 {
		t.Fatalf("cross-user job read: %d", w.Code)
	}
	for _, path := range []string{"/v1/jobs", "/v1/results", "/v1/workflows", "/v1/workflow-runs", "/v1/metrics"} {
		w = request(bobToken, "GET", path, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "Alice saved job") {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	stop()
	runtimes.Close()
	// Restore uses all durable account IDs, not the last browser session.
	runCtx, stop = context.WithCancel(ctx)
	defer stop()
	runtimes = NewRuntimes(runCtx, accounts, dsn, root, "/unused-codex", "", logger, nil)
	defer runtimes.Close()
	if err := runtimes.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	server = NewServer(accounts, runtimes, GoogleAuth{Origin: "https://jobs.example"}, "", logger)
	w = request(aliceToken, "GET", "/v1/jobs/"+job.ID, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Alice saved job") {
		t.Fatal("job lost on restart", w.Code, w.Body.String())
	}
	w = request(aliceToken, "GET", "/v1/postings/"+ids[alice.ID], "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "applied_at") {
		t.Fatal("application state lost", w.Body.String())
	}
	w = request(bobToken, "GET", "/v1/postings/"+ids[bob.ID], "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "applied_at") {
		t.Fatal("other user's application state changed", w.Body.String())
	}
	if err := accounts.Logout(ctx, aliceToken); err != nil {
		t.Fatal(err)
	}
	w = request(aliceToken, "GET", "/v1/jobs", "")
	if w.Code != 401 {
		t.Fatal("revoked session accepted")
	}
}
