package hosted

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"orchestrator/backend/internal/api"
	"orchestrator/backend/internal/codex"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/llm"
	"orchestrator/backend/internal/monitor"
	"orchestrator/backend/internal/notifications"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/results"
	"orchestrator/backend/internal/scheduler"
	"orchestrator/backend/internal/worker"
	"orchestrator/backend/internal/workers"
	"orchestrator/backend/internal/workflowruns"
	"orchestrator/backend/internal/workflows"
)

var userIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Runtime struct {
	Handler http.Handler
	Codex   *codex.Client
	cancel  context.CancelFunc
	close   []func()
	pool    *worker.Pool
}

func (r *Runtime) Close() {
	r.cancel()
	r.Codex.Close()
	if r.pool != nil {
		r.pool.Wait()
	}
	for i := len(r.close) - 1; i >= 0; i-- {
		r.close[i]()
	}
}

type Runtimes struct {
	mu                       sync.Mutex
	items                    map[string]*Runtime
	accounts                 *Accounts
	ctx                      context.Context
	dsn, root, binary, model string
	logger                   *slog.Logger
}

func NewRuntimes(ctx context.Context, a *Accounts, dsn, root, binary, model string, logger *slog.Logger) *Runtimes {
	return &Runtimes{items: map[string]*Runtime{}, accounts: a, ctx: ctx, dsn: dsn, root: root, binary: binary, model: model, logger: logger}
}
func (m *Runtimes) Restore(ctx context.Context) error {
	users, err := m.accounts.Users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if _, err := m.Get(u); err != nil {
			return fmt.Errorf("restore workspace: %w", err)
		}
	}
	return nil
}
func (m *Runtimes) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.items {
		r.Close()
	}
}
func workspaceDSN(dsn, id string) (string, error) {
	if !userIDPattern.MatchString(id) {
		return "", errors.New("invalid workspace identifier")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", errors.New("hosted mode requires a PostgreSQL URL")
	}
	q := u.Query()
	q.Set("search_path", "workspace_"+id)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (m *Runtimes) Get(u User) (*Runtime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.items[u.ID]; r != nil {
		return r, nil
	}
	dsn, err := workspaceDSN(m.dsn, u.ID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r := &Runtime{cancel: cancel}
	success := false
	defer func() {
		if !success {
			cancel()
			for i := len(r.close) - 1; i >= 0; i-- {
				r.close[i]()
			}
		}
	}()
	// The schema name comes only from a server-generated, validated account ID.
	if _, err = m.accounts.db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS "workspace_`+u.ID+`"`); err != nil {
		return nil, err
	}
	persistentJobs, err := jobs.NewPostgresStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	r.close = append(r.close, func() { _ = persistentJobs.Close() })
	if err = persistentJobs.Migrate(ctx); err != nil {
		return nil, err
	}
	js := &boundedJobs{Store: persistentJobs}
	ps, err := postings.NewPostgresStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	r.close = append(r.close, func() { _ = ps.Close() })
	if err = ps.Migrate(ctx); err != nil {
		return nil, err
	}
	ws, err := workflows.NewPostgresStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	r.close = append(r.close, func() { _ = ws.Close() })
	if err = ws.Migrate(ctx); err != nil {
		return nil, err
	}
	rs, err := results.NewPostgresStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	r.close = append(r.close, func() { _ = rs.Close() })
	if err = rs.Migrate(ctx); err != nil {
		return nil, err
	}
	ns, err := notifications.NewPostgresStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	r.close = append(r.close, func() { _ = ns.Close() })
	if err = ns.Migrate(ctx); err != nil {
		return nil, err
	}
	runs, err := workflowruns.NewPostgresStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	r.close = append(r.close, func() { _ = runs.Close() })
	if err = runs.Migrate(ctx); err != nil {
		return nil, err
	}
	registry := workers.NewMemoryRegistry()
	client := publicHTTPClient()
	sources := map[string]monitor.Source{"greenhouse": monitor.NewGreenhouseSource(client), "lever": monitor.NewLeverSource(client), "ashby": monitor.NewAshbySource(client), "workday": monitor.NewWorkdaySource(client), "custom": monitor.NewCustomSource(client)}
	runner := monitor.NewRunner(ps, sources)
	executor := worker.NewSimulatedExecutor(m.logger, runner, rs, nil)
	executor.SetHTTPClient(client)
	executor.SetNotificationStore(ns)
	// Hosted accounts never inherit the private instance's SMTP recipients/key.
	queue := jobs.NewStoreQueue(js, time.Second)
	c, err := codex.New(ctx, filepath.Join(m.root, u.ID), m.binary, m.model)
	if err != nil {
		return nil, err
	}
	r.Codex = c
	r.Handler = workspaceLimits(js, ws, api.NewRouter(api.Config{Queue: queue, Store: js, Workers: registry, Logger: m.logger, Planner: llm.NewConnectedPlanner(c.Generate), Postings: ps, Workflows: ws, Runs: runs, Results: rs, Notifications: ns}))
	r.pool = worker.NewPoolWithRuns(worker.PoolConfig{WorkerCount: 1, WorkerIDPrefix: "web-" + u.ID, PollDelay: time.Second, HeartbeatInterval: 5 * time.Second}, queue, js, runs, executor, registry, m.logger)
	r.pool.Start(ctx)
	scheduler.NewWithRuns(scheduler.Config{PollInterval: 30 * time.Second}, ws, runs, js, queue, m.logger).Start(ctx)
	m.items[u.ID] = r
	success = true
	return r, nil
}

// Bound retained work while this initial hosted version has no billing or
// retention service. The check and creation are serialized per workspace.
func workspaceLimits(js jobs.Store, ws workflows.Store, next http.Handler) http.Handler {
	var create sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && (r.URL.Path == "/v1/jobs" || r.URL.Path == "/v1/workflows" || strings.HasSuffix(r.URL.Path, "/natural") || strings.HasSuffix(r.URL.Path, "/run")) {
			create.Lock()
			defer create.Unlock()
			allJobs, err := js.List()
			if err != nil {
				fail(w, 503, "workspace unavailable")
				return
			}
			allWorkflows, err := ws.List()
			if err != nil {
				fail(w, 503, "workspace unavailable")
				return
			}
			if len(allJobs) >= 5000 || len(allWorkflows) >= 50 {
				fail(w, 409, "workspace capacity reached; contact the website operator")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// All producers, including the scheduler, share this creation guard.
type boundedJobs struct {
	jobs.Store
	mu sync.Mutex
}

func (s *boundedJobs) Create(p jobs.CreateJobParams) (*jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.Store.List()
	if err != nil {
		return nil, err
	}
	if len(items) >= 5000 {
		return nil, errors.New("workspace job capacity reached")
	}
	return s.Store.Create(p)
}
