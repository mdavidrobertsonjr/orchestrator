package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"orchestrator/backend/internal/api"
	"orchestrator/backend/internal/email"
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

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := configFromEnv()
	planner := buildPlanner(cfg, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, closeStore, err := buildStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize job store", "error", err)
		return
	}
	defer closeStore()

	queue, hydrateQueue := buildQueue(cfg, store, logger)

	registry, closeRegistry, err := buildWorkerRegistry(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize worker registry", "error", err)
		return
	}
	defer closeRegistry()

	postingStore, closePostingStore, err := buildPostingStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize posting store", "error", err)
		return
	}
	defer closePostingStore()

	workflowStore, closeWorkflowStore, err := buildWorkflowStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize workflow store", "error", err)
		return
	}
	defer closeWorkflowStore()

	resultStore, closeResultStore, err := buildResultStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize result store", "error", err)
		return
	}
	defer closeResultStore()

	notificationStore, closeNotificationStore, err := buildNotificationStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize notification store", "error", err)
		return
	}
	defer closeNotificationStore()

	runStore, closeRunStore, err := buildWorkflowRunStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize workflow run store", "error", err)
		return
	}
	defer closeRunStore()

	monitorRunner := monitor.NewRunner(postingStore, nil)
	emailSender := buildEmailSender(cfg, logger)
	executor := worker.NewSimulatedExecutor(logger, monitorRunner, resultStore, emailSender)
	executor.SetDefaultRecipients(cfg.DefaultRecipients)
	executor.SetNotificationStore(notificationStore)

	if hydrateQueue {
		if err := enqueuePendingJobs(ctx, store, queue); err != nil {
			logger.Error("failed to hydrate pending jobs", "error", err)
			return
		}
	}

	var pool *worker.Pool
	if cfg.EmbeddedWorkers {
		pool = worker.NewPoolWithRuns(worker.PoolConfig{
			WorkerCount:       cfg.WorkerCount,
			WorkerIDPrefix:    cfg.WorkerIDPrefix,
			PollDelay:         250 * time.Millisecond,
			HeartbeatInterval: 5 * time.Second,
		}, queue, store, runStore, executor, registry, logger)
		pool.Start(ctx)
	} else {
		logger.Info("embedded workers disabled")
	}

	if cfg.SchedulerEnabled {
		schedulerLock, closeSchedulerLock, err := buildSchedulerLock(ctx, cfg, logger)
		if err != nil {
			logger.Error("failed to initialize scheduler lock", "error", err)
			return
		}
		defer closeSchedulerLock()

		scheduled := scheduler.NewWithRuns(scheduler.Config{
			PollInterval: cfg.SchedulerPollInterval,
			Lock:         schedulerLock,
		}, workflowStore, runStore, store, queue, logger)
		scheduled.Start(ctx)
	} else {
		logger.Info("scheduler disabled")
	}

	handler := api.NewRouter(api.Config{
		Queue:         queue,
		Store:         store,
		Workers:       registry,
		Logger:        logger,
		Static:        cfg.StaticDir,
		Planner:       planner,
		Postings:      postingStore,
		Workflows:     workflowStore,
		Runs:          runStore,
		Results:       resultStore,
		Notifications: notificationStore,
		AuthToken:     cfg.AuthToken,
		APIKeys:       cfg.APIKeys,
	})

	server := &http.Server{
		Addr:        cfg.Addr,
		Handler:     handler,
		ReadTimeout: 5 * time.Second,
		// Leave time for the planner's 20-second request deadline and an error response.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("api server listening", "addr", cfg.Addr, "workers", cfg.WorkerCount)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api server shutdown failed", "error", err)
	}

	if pool != nil {
		pool.Wait()
	}
	logger.Info("shutdown complete")
}

type config struct {
	Addr                  string
	QueueSize             int
	QueueBackend          string
	WorkerCount           int
	WorkerIDPrefix        string
	EmbeddedWorkers       bool
	DatabaseURL           string
	AutoMigrateDB         bool
	StaticDir             string
	OpenAIAPIKey          string
	OpenAIModel           string
	SchedulerEnabled      bool
	SchedulerPollInterval time.Duration
	SMTPHost              string
	SMTPPort              int
	SMTPUsername          string
	SMTPPassword          string
	SMTPFrom              string
	DefaultRecipients     []string
	AuthToken             string
	APIKeys               []api.APIKey
}

func configFromEnv() config {
	return config{
		Addr:                  envString("ORCH_ADDR", ":8080"),
		QueueSize:             envInt("ORCH_QUEUE_SIZE", 128),
		QueueBackend:          os.Getenv("ORCH_QUEUE_BACKEND"),
		WorkerCount:           envInt("ORCH_WORKERS", 2),
		WorkerIDPrefix:        envString("ORCH_WORKER_ID", "worker"),
		EmbeddedWorkers:       envBool("ORCH_EMBEDDED_WORKERS", true),
		DatabaseURL:           os.Getenv("ORCH_DATABASE_URL"),
		AutoMigrateDB:         envBool("ORCH_AUTO_MIGRATE", true),
		StaticDir:             envOptionalString("ORCH_STATIC_DIR", "../frontend/dist"),
		OpenAIAPIKey:          os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:           envString("ORCH_OPENAI_MODEL", "gpt-5.4-nano"),
		SchedulerEnabled:      envBool("ORCH_SCHEDULER_ENABLED", true),
		SchedulerPollInterval: time.Duration(envInt("ORCH_SCHEDULER_POLL_SECONDS", 30)) * time.Second,
		SMTPHost:              os.Getenv("ORCH_SMTP_HOST"),
		SMTPPort:              envInt("ORCH_SMTP_PORT", 587),
		SMTPUsername:          os.Getenv("ORCH_SMTP_USERNAME"),
		SMTPPassword:          os.Getenv("ORCH_SMTP_PASSWORD"),
		SMTPFrom:              os.Getenv("ORCH_SMTP_FROM"),
		DefaultRecipients:     envStringList("ORCH_DEFAULT_RECIPIENTS"),
		AuthToken:             os.Getenv("ORCH_AUTH_TOKEN"),
		APIKeys:               envAPIKeys("ORCH_API_KEYS"),
	}
}

func buildQueue(cfg config, store jobs.Store, logger *slog.Logger) (jobs.Queue, bool) {
	backend := strings.ToLower(strings.TrimSpace(cfg.QueueBackend))
	if backend == "" && cfg.DatabaseURL != "" {
		backend = "store"
	}
	if backend == "store" {
		logger.Info("using store-backed job queue")
		return jobs.NewStoreQueue(store, 250*time.Millisecond), false
	}

	logger.Info("using in-memory job queue")
	return jobs.NewMemoryQueue(cfg.QueueSize), true
}

func buildWorkerRegistry(ctx context.Context, cfg config, logger *slog.Logger) (workers.Registry, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("using in-memory worker registry")
		return workers.NewMemoryRegistry(), func() {}, nil
	}

	registry, err := workers.NewPostgresRegistry(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if cfg.AutoMigrateDB {
		if err := registry.Migrate(ctx); err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
	}

	logger.Info("using postgres worker registry")
	return registry, func() {
		if err := registry.Close(); err != nil {
			logger.Error("failed to close postgres worker registry", "error", err)
		}
	}, nil
}

func buildPlanner(cfg config, logger *slog.Logger) llm.Planner {
	if cfg.OpenAIAPIKey == "" {
		logger.Info("LLM planner disabled; OPENAI_API_KEY is not set")
		return nil
	}

	logger.Info("LLM planner enabled", "model", cfg.OpenAIModel)
	return llm.NewOpenAIPlanner(cfg.OpenAIAPIKey, cfg.OpenAIModel)
}

func buildEmailSender(cfg config, logger *slog.Logger) email.Sender {
	if cfg.SMTPHost == "" {
		logger.Info("SMTP email delivery disabled; ORCH_SMTP_HOST is not set")
		return nil
	}

	logger.Info("SMTP email delivery enabled", "host", cfg.SMTPHost, "port", cfg.SMTPPort)
	return email.NewSMTPSender(email.SMTPConfig{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword,
		From:     cfg.SMTPFrom,
	})
}

func buildSchedulerLock(ctx context.Context, cfg config, logger *slog.Logger) (scheduler.Lock, func(), error) {
	if cfg.DatabaseURL == "" {
		return nil, func() {}, nil
	}
	lock, err := scheduler.NewPostgresAdvisoryLock(ctx, cfg.DatabaseURL, "orchestrator.scheduler")
	if err != nil {
		return nil, nil, err
	}
	logger.Info("using postgres scheduler advisory lock")
	return lock, func() {
		if err := lock.Close(); err != nil {
			logger.Error("failed to close scheduler lock", "error", err)
		}
	}, nil
}

func buildStore(ctx context.Context, cfg config, logger *slog.Logger) (jobs.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("using in-memory job store")
		return jobs.NewMemoryStore(), func() {}, nil
	}

	store, err := jobs.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	if cfg.AutoMigrateDB {
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}

	logger.Info("using postgres job store")
	return store, func() {
		if err := store.Close(); err != nil {
			logger.Error("failed to close postgres job store", "error", err)
		}
	}, nil
}

func buildPostingStore(ctx context.Context, cfg config, logger *slog.Logger) (postings.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("using in-memory posting store")
		return postings.NewMemoryStore(), func() {}, nil
	}

	store, err := postings.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	if cfg.AutoMigrateDB {
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}

	logger.Info("using postgres posting store")
	return store, func() {
		if err := store.Close(); err != nil {
			logger.Error("failed to close postgres posting store", "error", err)
		}
	}, nil
}

func buildWorkflowStore(ctx context.Context, cfg config, logger *slog.Logger) (workflows.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("using in-memory workflow store")
		return workflows.NewMemoryStore(), func() {}, nil
	}

	store, err := workflows.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	if cfg.AutoMigrateDB {
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}

	logger.Info("using postgres workflow store")
	return store, func() {
		if err := store.Close(); err != nil {
			logger.Error("failed to close postgres workflow store", "error", err)
		}
	}, nil
}

func buildResultStore(ctx context.Context, cfg config, logger *slog.Logger) (results.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("using in-memory result store")
		return results.NewMemoryStore(), func() {}, nil
	}

	store, err := results.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	if cfg.AutoMigrateDB {
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}

	logger.Info("using postgres result store")
	return store, func() {
		if err := store.Close(); err != nil {
			logger.Error("failed to close postgres result store", "error", err)
		}
	}, nil
}

func buildNotificationStore(ctx context.Context, cfg config, logger *slog.Logger) (notifications.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("using in-memory notification store")
		return notifications.NewMemoryStore(), func() {}, nil
	}

	store, err := notifications.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	if cfg.AutoMigrateDB {
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}

	logger.Info("using postgres notification store")
	return store, func() {
		if err := store.Close(); err != nil {
			logger.Error("failed to close postgres notification store", "error", err)
		}
	}, nil
}

func buildWorkflowRunStore(ctx context.Context, cfg config, logger *slog.Logger) (workflowruns.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("using in-memory workflow run store")
		return workflowruns.NewMemoryStore(), func() {}, nil
	}

	store, err := workflowruns.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	if cfg.AutoMigrateDB {
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}

	logger.Info("using postgres workflow run store")
	return store, func() {
		if err := store.Close(); err != nil {
			logger.Error("failed to close postgres workflow run store", "error", err)
		}
	}, nil
}

func enqueuePendingJobs(ctx context.Context, store jobs.Store, queue jobs.Queue) error {
	storedJobs, err := store.List()
	if err != nil {
		return err
	}

	for _, job := range storedJobs {
		if job.Status != jobs.StatusQueued && job.Status != jobs.StatusRunning {
			continue
		}
		if err := queue.Enqueue(ctx, job.ID); err != nil {
			return fmt.Errorf("enqueue persisted job %s: %w", job.ID, err)
		}
	}
	return nil
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envOptionalString(key, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envStringList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envAPIKeys(key string) []api.APIKey {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	entries := strings.Split(raw, ";")
	out := make([]api.APIKey, 0, len(entries))
	for _, entry := range entries {
		parts := strings.Split(entry, ":")
		if len(parts) < 2 {
			continue
		}
		scopes := []string{"admin"}
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			scopes = strings.Split(parts[2], "+")
		}
		out = append(out, api.APIKey{
			Name:   strings.TrimSpace(parts[0]),
			Token:  strings.TrimSpace(parts[1]),
			Scopes: scopes,
		})
	}
	return out
}
