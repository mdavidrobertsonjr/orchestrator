package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"orchestrator/backend/internal/jobs"
)

type PoolConfig struct {
	WorkerCount int
	PollDelay   time.Duration
}

type Pool struct {
	config   PoolConfig
	queue    jobs.Queue
	store    jobs.Store
	executor Executor
	logger   *slog.Logger
	wg       sync.WaitGroup
}

func NewPool(config PoolConfig, queue jobs.Queue, store jobs.Store, executor Executor, logger *slog.Logger) *Pool {
	if config.WorkerCount <= 0 {
		config.WorkerCount = 1
	}
	if config.PollDelay <= 0 {
		config.PollDelay = 250 * time.Millisecond
	}

	return &Pool{
		config:   config,
		queue:    queue,
		store:    store,
		executor: executor,
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
	logger := p.logger.With("worker_id", workerID)
	logger.Info("worker started")
	defer logger.Info("worker stopped")

	for {
		jobID, err := p.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			logger.Error("dequeue failed", "error", err)
			sleep(ctx, p.config.PollDelay)
			continue
		}

		p.execute(ctx, logger, jobID)
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
