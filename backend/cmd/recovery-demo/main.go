package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/worker"
	"orchestrator/backend/internal/workers"
)

const leaseDuration = 3 * time.Second

type event struct {
	Elapsed        float64     `json:"elapsed"`
	JobID          string      `json:"job_id,omitempty"`
	Message        string      `json:"message"`
	Status         jobs.Status `json:"status,omitempty"`
	Attempt        int         `json:"attempt,omitempty"`
	Owner          string      `json:"owner,omitempty"`
	LeaseRemaining float64     `json:"lease_remaining,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	url := os.Getenv("ORCH_DATABASE_URL")
	if url == "" {
		return errors.New("ORCH_DATABASE_URL must point to an isolated demo database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := jobs.NewPostgresStore(ctx, url)
	if err != nil {
		return err
	}
	defer store.Close()

	if len(os.Args) == 3 && os.Args[1] == "--worker" {
		registry, err := workers.NewPostgresRegistry(ctx, url)
		if err != nil {
			return err
		}
		defer registry.Close()
		if err := registry.Migrate(ctx); err != nil {
			return err
		}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		executor := worker.NewSimulatedExecutor(logger, nil, nil, nil)
		pool := worker.NewPool(worker.PoolConfig{
			WorkerCount: 1, WorkerIDPrefix: os.Args[2], LeaseDuration: leaseDuration,
			LeaseSweepInterval: 100 * time.Millisecond, HeartbeatInterval: 100 * time.Millisecond,
		}, jobs.NewStoreQueue(store, 50*time.Millisecond), store, executor, registry, logger)
		pool.Start(ctx)
		<-ctx.Done()
		pool.Wait()
		return nil
	}
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	started := time.Now()
	encoder := json.NewEncoder(os.Stdout)
	emit := func(message string, job *jobs.Job) error {
		e := event{Elapsed: time.Since(started).Seconds(), Message: message}
		if job != nil {
			e.JobID = job.ID
			e.Status, e.Attempt, e.Owner = job.Status, job.Attempts, job.LeaseOwner
			if job.LeaseUntil != nil {
				e.LeaseRemaining = time.Until(*job.LeaseUntil).Seconds()
			}
		}
		return encoder.Encode(e)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	startWorker := func(name string) (*exec.Cmd, error) {
		if err := emit("Command: "+filepath.Base(executable)+" --worker "+name, nil); err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, executable, "--worker", name)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		if err := emit(fmt.Sprintf("Started %s (PID %d).", name, cmd.Process.Pid), nil); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, err
		}
		return cmd, nil
	}
	stopWorker := func(cmd *exec.Cmd) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	if err := emit("PostgreSQL ready. Starting worker A.", nil); err != nil {
		return err
	}
	a, err := startWorker("worker-a")
	if err != nil {
		return err
	}
	aStopped := false
	defer func() {
		if !aStopped {
			stopWorker(a)
		}
	}()
	job, err := store.Create(jobs.CreateJobParams{
		Name: "worker-crash-recovery-demo", Type: "demo.sleep", MaxAttempts: 3,
		Payload:  map[string]any{"duration_ms": 12000},
		Metadata: map[string]string{"submitted_by": filepath.Base(executable)},
	})
	if err != nil {
		return err
	}
	if err := emit("Submitted a 12-second job with a 3-second lease.", job); err != nil {
		return err
	}
	waitFor := func(check func(*jobs.Job) bool) (*jobs.Job, error) {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			current, err := store.Get(job.ID)
			if err != nil {
				return nil, err
			}
			if check(current) {
				return current, nil
			}
			if current.Status == jobs.StatusDeadLetter || current.Status == jobs.StatusFailed {
				return nil, fmt.Errorf("job terminated unexpectedly: %+v", current)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	claimed, err := waitFor(func(j *jobs.Job) bool { return j.Status == jobs.StatusRunning })
	if err != nil {
		return err
	}
	if err := emit("Worker A claimed attempt 1.", claimed); err != nil {
		return err
	}
	b, err := startWorker("worker-b")
	if err != nil {
		return err
	}
	defer stopWorker(b)
	registry, err := workers.NewPostgresRegistry(ctx, url)
	if err != nil {
		return err
	}
	defer registry.Close()
	for {
		standby, err := registry.Get("worker-b-1")
		if err == nil && standby.Status == workers.StatusIdle {
			break
		}
		if err != nil && !errors.Is(err, workers.ErrNotFound) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err := emit("Worker B is registered and idle. Worker A owns attempt 1.", claimed); err != nil {
		return err
	}
	originalDeadline := *claimed.LeaseUntil
	renewed, err := waitFor(func(j *jobs.Job) bool {
		return time.Now().After(originalDeadline) && j.Status == jobs.StatusRunning && j.Attempts == 1 && j.LeaseUntil.After(originalDeadline)
	})
	if err != nil {
		return err
	}
	if err := emit("Original lease deadline passed. Renewal keeps attempt 1 running.", renewed); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
	}
	crashTime := time.Now()
	var kill *exec.Cmd
	if runtime.GOOS == "windows" {
		kill = exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(a.Process.Pid), "/F")
	} else {
		kill = exec.CommandContext(ctx, "kill", "-KILL", strconv.Itoa(a.Process.Pid))
	}
	if err := emit("Command: "+filepath.Base(kill.Path)+" "+strings.Join(kill.Args[1:], " "), nil); err != nil {
		return err
	}
	output, err := kill.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kill worker A: %w: %s", err, output)
	}
	if err := emit("Process output: "+strings.TrimSpace(string(output)), nil); err != nil {
		return err
	}
	_ = a.Wait()
	aStopped = true
	crashTime = time.Now()
	if err := emit("Hard-killed worker A mid-job.", renewed); err != nil {
		return err
	}
	recovered, err := waitFor(func(j *jobs.Job) bool {
		return j.Status == jobs.StatusRunning && j.LeaseOwner == "worker-b-1" && j.Attempts == 2
	})
	if err != nil {
		return err
	}
	if err := emit(fmt.Sprintf("Worker B reclaimed the job %.2fs after the crash.", time.Since(crashTime).Seconds()), recovered); err != nil {
		return err
	}
	finished, err := waitFor(func(j *jobs.Job) bool { return j.Status == jobs.StatusSucceeded })
	if err != nil {
		return err
	}
	if finished.Attempts != 2 {
		return fmt.Errorf("expected 2 attempts, got %d", finished.Attempts)
	}
	if err := emit("Job succeeded on attempt 2. State persisted in PostgreSQL.", finished); err != nil {
		return err
	}
	for _, entry := range finished.Logs {
		if err := emit("Job log: "+entry.Message, nil); err != nil {
			return err
		}
	}
	if len(os.Args) == 2 && os.Args[1] == "--dashboard" {
		registry, err := workers.NewPostgresRegistry(ctx, url)
		if err != nil {
			return err
		}
		defer registry.Close()
		if err := emit("Waiting for the dashboard's 30-second heartbeat timeout.", finished); err != nil {
			return err
		}
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			crashed, err := registry.Get("worker-a-1")
			if err != nil {
				return err
			}
			if time.Since(crashed.LastHeartbeat) > 31*time.Second {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		if err := emit("Worker A heartbeat timed out. Worker B remains healthy.", finished); err != nil {
			return err
		}
	}
	return emit("PASS: lease renewal, real process crash, lease recovery, successful retry.", finished)
}
