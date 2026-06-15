package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"orchestrator/backend/internal/email"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/monitor"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/results"
	"orchestrator/backend/internal/worker"
	"orchestrator/backend/internal/workers"
	"orchestrator/backend/internal/workflowruns"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := configFromEnv()
	if cfg.DatabaseURL == "" {
		logger.Error("ORCH_DATABASE_URL is required for standalone workers")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	jobStore, closeJobs, err := buildJobStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize job store", "error", err)
		os.Exit(1)
	}
	defer closeJobs()

	postingStore, closePostings, err := buildPostingStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize posting store", "error", err)
		os.Exit(1)
	}
	defer closePostings()

	resultStore, closeResults, err := buildResultStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize result store", "error", err)
		os.Exit(1)
	}
	defer closeResults()

	runStore, closeRuns, err := buildWorkflowRunStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize workflow run store", "error", err)
		os.Exit(1)
	}
	defer closeRuns()

	registry, closeRegistry, err := buildWorkerRegistry(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize worker registry", "error", err)
		os.Exit(1)
	}
	defer closeRegistry()

	queue := jobs.NewStoreQueue(jobStore, cfg.QueuePollDelay)
	monitorRunner := monitor.NewRunner(postingStore, nil)
	executor := worker.NewSimulatedExecutor(logger, monitorRunner, resultStore, buildEmailSender(cfg, logger))
	executor.SetDefaultRecipients(cfg.DefaultRecipients)

	pool := worker.NewPoolWithRuns(worker.PoolConfig{
		WorkerCount:       cfg.WorkerCount,
		PollDelay:         cfg.QueuePollDelay,
		HeartbeatInterval: cfg.HeartbeatInterval,
	}, queue, jobStore, runStore, executor, registry, logger)
	pool.Start(ctx)

	logger.Info("worker process started", "workers", cfg.WorkerCount)
	<-ctx.Done()
	logger.Info("shutdown signal received")
	pool.Wait()
	logger.Info("worker process stopped")
}

type config struct {
	DatabaseURL       string
	AutoMigrateDB     bool
	WorkerCount       int
	QueuePollDelay    time.Duration
	HeartbeatInterval time.Duration
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string
	SMTPFrom          string
	DefaultRecipients []string
}

func configFromEnv() config {
	return config{
		DatabaseURL:       os.Getenv("ORCH_DATABASE_URL"),
		AutoMigrateDB:     envBool("ORCH_AUTO_MIGRATE", true),
		WorkerCount:       envInt("ORCH_WORKERS", 1),
		QueuePollDelay:    time.Duration(envInt("ORCH_QUEUE_POLL_MS", 250)) * time.Millisecond,
		HeartbeatInterval: time.Duration(envInt("ORCH_HEARTBEAT_SECONDS", 5)) * time.Second,
		SMTPHost:          os.Getenv("ORCH_SMTP_HOST"),
		SMTPPort:          envInt("ORCH_SMTP_PORT", 587),
		SMTPUsername:      os.Getenv("ORCH_SMTP_USERNAME"),
		SMTPPassword:      os.Getenv("ORCH_SMTP_PASSWORD"),
		SMTPFrom:          os.Getenv("ORCH_SMTP_FROM"),
		DefaultRecipients: envStringList("ORCH_DEFAULT_RECIPIENTS"),
	}
}

func buildJobStore(ctx context.Context, cfg config, logger *slog.Logger) (*jobs.PostgresStore, func(), error) {
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
	return store, closePostgres("job", store.Close, logger), nil
}

func buildPostingStore(ctx context.Context, cfg config, logger *slog.Logger) (postings.Store, func(), error) {
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
	return store, closePostgres("posting", store.Close, logger), nil
}

func buildResultStore(ctx context.Context, cfg config, logger *slog.Logger) (results.Store, func(), error) {
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
	return store, closePostgres("result", store.Close, logger), nil
}

func buildWorkflowRunStore(ctx context.Context, cfg config, logger *slog.Logger) (workflowruns.Store, func(), error) {
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
	return store, closePostgres("workflow run", store.Close, logger), nil
}

func buildWorkerRegistry(ctx context.Context, cfg config, logger *slog.Logger) (workers.Registry, func(), error) {
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
	return registry, closePostgres("worker registry", registry.Close, logger), nil
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

func closePostgres(name string, close func() error, logger *slog.Logger) func() {
	return func() {
		if err := close(); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("failed to close postgres store", "store", name, "error", err)
		}
	}
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
