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
	"orchestrator/backend/internal/workflowruns"
)

type PoolConfig struct {
	WorkerCount        int
	PollDelay          time.Duration
	HeartbeatInterval  time.Duration
	LeaseDuration      time.Duration
	LeaseSweepInterval time.Duration
}

type Pool struct {
	config   PoolConfig
	queue    jobs.Queue
	store    jobs.Store
	runs     workflowruns.Store
	executor Executor
	registry workers.Registry
	logger   *slog.Logger
	wg       sync.WaitGroup
}

func NewPool(config PoolConfig, queue jobs.Queue, store jobs.Store, executor Executor, registry workers.Registry, logger *slog.Logger) *Pool {
	return NewPoolWithRuns(config, queue, store, nil, executor, registry, logger)
}

func NewPoolWithRuns(config PoolConfig, queue jobs.Queue, store jobs.Store, runStore workflowruns.Store, executor Executor, registry workers.Registry, logger *slog.Logger) *Pool {
	if config.WorkerCount <= 0 {
		config.WorkerCount = 1
	}
	if config.PollDelay <= 0 {
		config.PollDelay = 250 * time.Millisecond
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 5 * time.Second
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = 2 * time.Minute
	}
	if config.LeaseSweepInterval <= 0 {
		config.LeaseSweepInterval = 15 * time.Second
	}

	return &Pool{
		config:   config,
		queue:    queue,
		store:    store,
		runs:     runStore,
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
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.leaseReclaimer(ctx)
	}()
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

		if claimQueue, ok := p.queue.(jobs.ClaimQueue); ok {
			if !p.claimAndExecute(ctx, logger, id, claimQueue) {
				return
			}
			continue
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

		p.execute(ctx, logger, id, jobID)
	}
}

func (p *Pool) claimAndExecute(ctx context.Context, logger *slog.Logger, workerID string, queue jobs.ClaimQueue) bool {
	job, err := queue.Claim(ctx, workerID, time.Now().UTC().Add(p.config.LeaseDuration))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false
		}
		logger.Error("claim failed", "error", err)
		sleep(ctx, p.config.PollDelay)
		return true
	}

	if _, err := p.registry.MarkRunning(workerID, job.ID); err != nil {
		logger.Error("failed to mark worker running", "job_id", job.ID, "error", err)
	}

	p.executeClaimed(ctx, logger, job)
	return true
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

func (p *Pool) execute(ctx context.Context, logger *slog.Logger, workerID string, jobID string) {
	job, err := p.store.MarkRunningWithLease(jobID, workerID, time.Now().UTC().Add(p.config.LeaseDuration))
	if err != nil {
		logger.Error("failed to mark job running", "job_id", jobID, "error", err)
		return
	}
	p.executeClaimed(ctx, logger, job)
}

func (p *Pool) executeClaimed(ctx context.Context, logger *slog.Logger, job *jobs.Job) {
	jl := jobLogger(logger, job)
	p.markRunStatus(job.ID, string(jobs.StatusRunning), jl)
	jl.Info("job execution started", "type", job.Type)

	logf := func(message string) {
		if err := p.store.AppendLog(job.ID, message); err != nil {
			jl.Error("failed to append job log", "error", err)
		}
		jl.Info(message)
	}

	if err := p.executor.Execute(ctx, job, logf); err != nil {
		p.handleFailure(ctx, jl, job, err)
		return
	}

	if _, err := p.store.MarkSucceeded(job.ID, "job succeeded"); err != nil {
		jl.Error("failed to mark job succeeded", "error", err)
		return
	}
	p.markRunStatus(job.ID, string(jobs.StatusSucceeded), jl)
	jl.Info("job execution succeeded")
}

func (p *Pool) handleFailure(ctx context.Context, logger *slog.Logger, job *jobs.Job, execErr error) {
	if job.Attempts < job.MaxAttempts {
		if _, err := p.store.MarkQueued(job.ID, "job attempt failed; requeueing: "+execErr.Error()); err != nil {
			logger.Error("failed to mark retryable job queued", "error", err)
			_, _ = p.store.MarkFailed(job.ID, execErr.Error())
			p.markRunStatus(job.ID, string(jobs.StatusFailed), logger)
			return
		}
		if err := p.queue.Enqueue(ctx, job.ID); err != nil {
			logger.Error("failed to requeue job", "error", err)
			_, _ = p.store.MarkFailed(job.ID, execErr.Error())
			p.markRunStatus(job.ID, string(jobs.StatusFailed), logger)
			return
		}
		p.markRunStatus(job.ID, string(jobs.StatusQueued), logger)
		logger.Warn("job execution failed; retry queued", "error", execErr)
		return
	}

	if _, err := p.store.MarkDeadLetter(job.ID, execErr.Error()); err != nil {
		logger.Error("failed to mark job failed", "error", err)
		return
	}
	p.markRunStatus(job.ID, string(jobs.StatusDeadLetter), logger)
	logger.Error("job execution failed", "error", execErr)
}

func (p *Pool) leaseReclaimer(ctx context.Context) {
	ticker := time.NewTicker(p.config.LeaseSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			reclaimed, err := p.store.RequeueExpiredLeases(now)
			if err != nil {
				p.logger.Error("failed to reclaim expired job leases", "error", err)
				continue
			}
			for _, job := range reclaimed {
				p.markRunStatus(job.ID, string(job.Status), p.logger.With("job_id", job.ID))
				if job.Status == jobs.StatusQueued {
					if err := p.queue.Enqueue(ctx, job.ID); err != nil {
						p.logger.Error("failed to requeue expired lease job", "job_id", job.ID, "error", err)
					}
				}
			}
		}
	}
}

func (p *Pool) markRunStatus(jobID string, status string, logger *slog.Logger) {
	if p.runs == nil {
		return
	}
	if _, err := p.runs.MarkStatusByJob(jobID, status); err != nil && !errors.Is(err, workflowruns.ErrNotFound) {
		logger.Error("failed to update workflow run status", "status", status, "error", err)
	}
}

func jobLogger(logger *slog.Logger, job *jobs.Job) *slog.Logger {
	return logger.With("job_id", job.ID, "attempt", job.Attempts)
}

func sleep(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
