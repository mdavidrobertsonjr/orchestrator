package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"orchestrator/backend/internal/api"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/worker"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := configFromEnv()
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(cfg.QueueSize)
	executor := worker.NewSimulatedExecutor(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool := worker.NewPool(worker.PoolConfig{
		WorkerCount: cfg.WorkerCount,
		PollDelay:   250 * time.Millisecond,
	}, queue, store, executor, logger)
	pool.Start(ctx)

	handler := api.NewRouter(api.Config{
		Queue:  queue,
		Store:  store,
		Logger: logger,
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
	Addr        string
	QueueSize   int
	WorkerCount int
}

func configFromEnv() config {
	return config{
		Addr:        envString("ORCH_ADDR", ":8080"),
		QueueSize:   envInt("ORCH_QUEUE_SIZE", 128),
		WorkerCount: envInt("ORCH_WORKERS", 2),
	}
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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
