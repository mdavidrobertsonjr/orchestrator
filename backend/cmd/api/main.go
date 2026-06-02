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
	"syscall"
	"time"

	"orchestrator/backend/internal/api"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/llm"
	"orchestrator/backend/internal/monitor"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/worker"
	"orchestrator/backend/internal/workers"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := configFromEnv()
	queue := jobs.NewMemoryQueue(cfg.QueueSize)
	registry := workers.NewMemoryRegistry()
	postingStore := postings.NewMemoryStore()
	monitorRunner := monitor.NewRunner(postingStore, nil)
	executor := worker.NewSimulatedExecutor(logger, monitorRunner)
	planner := buildPlanner(cfg, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, closeStore, err := buildStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize job store", "error", err)
		return
	}
	defer closeStore()

	if err := enqueuePendingJobs(ctx, store, queue); err != nil {
		logger.Error("failed to hydrate pending jobs", "error", err)
		return
	}

	pool := worker.NewPool(worker.PoolConfig{
		WorkerCount:       cfg.WorkerCount,
		PollDelay:         250 * time.Millisecond,
		HeartbeatInterval: 5 * time.Second,
	}, queue, store, executor, registry, logger)
	pool.Start(ctx)

	handler := api.NewRouter(api.Config{
		Queue:    queue,
		Store:    store,
		Workers:  registry,
		Logger:   logger,
		Static:   cfg.StaticDir,
		Planner:  planner,
		Postings: postingStore,
	})

	server := &http.Server{
		Addr:         cfg.Addr,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
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

	pool.Wait()
	logger.Info("shutdown complete")
}

type config struct {
	Addr          string
	QueueSize     int
	WorkerCount   int
	DatabaseURL   string
	AutoMigrateDB bool
	StaticDir     string
	OpenAIAPIKey  string
	OpenAIModel   string
}

func configFromEnv() config {
	return config{
		Addr:          envString("ORCH_ADDR", ":8080"),
		QueueSize:     envInt("ORCH_QUEUE_SIZE", 128),
		WorkerCount:   envInt("ORCH_WORKERS", 2),
		DatabaseURL:   os.Getenv("ORCH_DATABASE_URL"),
		AutoMigrateDB: envBool("ORCH_AUTO_MIGRATE", true),
		StaticDir:     envOptionalString("ORCH_STATIC_DIR", "../frontend/dist"),
		OpenAIAPIKey:  os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:   envString("ORCH_OPENAI_MODEL", "gpt-5.4-nano"),
	}
}

func buildPlanner(cfg config, logger *slog.Logger) llm.Planner {
	if cfg.OpenAIAPIKey == "" {
		logger.Info("LLM planner disabled; OPENAI_API_KEY is not set")
		return nil
	}

	logger.Info("LLM planner enabled", "model", cfg.OpenAIModel)
	return llm.NewOpenAIPlanner(cfg.OpenAIAPIKey, cfg.OpenAIModel)
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
