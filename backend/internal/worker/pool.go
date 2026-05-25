package worker

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/workers"
)

type PoolConfig struct {
	WorkerCount       int
	PollDelay         time.Duration
	HeartbeatInterval time.Duration
}

type Pool struct {
	config   PoolConfig
	queue    jobs.Queue
	store    jobs.Store
	executor Executor
	registry workers.Registry
	logger   *slog.Logger
	wg       sync.WaitGroup
}

func NewPool(config PoolConfig, queue jobs.Queue, store jobs.Store, executor Executor, registry workers.Registry, logger *slog.Logger) *Pool {
	if config.WorkerCount <= 0 {
		config.WorkerCount = 1
	}
	if config.PollDelay <= 0 {
		config.PollDelay = 250 * time.Millisecond
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 5 * time.Second
	}

	return &Pool{
		config:   config,
		queue:    queue,
		store:    store,
		executor: executor,
		registry: registry,
		logger:   logger,
	}
}

func (p *Pool) Start(ctx context.Context) {
	for i := 1; i <= p.config.WorkerCount; i++ {
		workerID := i
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.runWorker(ctx, workerID)
		}()
	}
}

func (p *Pool) Wait() {
	p.wg.Wait()
}

func (p *Pool) runWorker(ctx context.Context, workerID int) {
	id := "worker-" + strconv.Itoa(workerID)
	logger := p.logger.With("worker_id", id)
	logger.Info("worker started")

	if _, err := p.registry.Register(id); err != nil {
		logger.Error("failed to register worker", "error", err)
		return
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go p.heartbeatLoop(heartbeatCtx, id, logger)

	defer func() {
		if _, err := p.registry.MarkStopped(id); err != nil {
			logger.Error("failed to mark worker stopped", "error", err)
		}
		logger.Info("worker stopped")
	}()

	for {
		if _, err := p.registry.MarkIdle(id); err != nil {
			logger.Error("failed to mark worker idle", "error", err)
		}

		jobID, err := p.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			logger.Error("dequeue failed", "error", err)
			sleep(ctx, p.config.PollDelay)
			continue
		}

		if _, err := p.registry.MarkRunning(id, jobID); err != nil {
			logger.Error("failed to mark worker running", "job_id", jobID, "error", err)
		}

		p.execute(ctx, logger, jobID)
	}
}

func (p *Pool) heartbeatLoop(ctx context.Context, workerID string, logger *slog.Logger) {
	ticker := time.NewTicker(p.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.registry.Heartbeat(workerID); err != nil {
				logger.Error("failed to heartbeat worker", "error", err)
			}
		}
	}
}

func (p *Pool) execute(ctx context.Context, logger *slog.Logger, jobID string) {
	job, err := p.store.MarkRunning(jobID)
	if err != nil {
		logger.Error("failed to mark job running", "job_id", jobID, "error", err)
		return
	}

	jobLogger := logger.With("job_id", job.ID, "attempt", job.Attempts)
	jobLogger.Info("job execution started", "type", job.Type)

	logf := func(message string) {
		if err := p.store.AppendLog(job.ID, message); err != nil {
			jobLogger.Error("failed to append job log", "error", err)
		}
		jobLogger.Info(message)
	}

	if err := p.executor.Execute(ctx, job, logf); err != nil {
		p.handleFailure(ctx, jobLogger, job, err)
		return
	}

	if _, err := p.store.MarkSucceeded(job.ID, "job succeeded"); err != nil {
		jobLogger.Error("failed to mark job succeeded", "error", err)
		return
	}
	jobLogger.Info("job execution succeeded")
}

func (p *Pool) handleFailure(ctx context.Context, logger *slog.Logger, job *jobs.Job, execErr error) {
	if job.Attempts < job.MaxAttempts {
		if err := p.store.AppendLog(job.ID, "job attempt failed; requeueing: "+execErr.Error()); err != nil {
			logger.Error("failed to append retry log", "error", err)
		}
		if err := p.queue.Enqueue(ctx, job.ID); err != nil {
			logger.Error("failed to requeue job", "error", err)
			_, _ = p.store.MarkFailed(job.ID, execErr.Error())
			return
		}
		logger.Warn("job execution failed; retry queued", "error", execErr)
		return
	}

	if _, err := p.store.MarkFailed(job.ID, execErr.Error()); err != nil {
		logger.Error("failed to mark job failed", "error", err)
		return
	}
	logger.Error("job execution failed", "error", execErr)
}

func sleep(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
