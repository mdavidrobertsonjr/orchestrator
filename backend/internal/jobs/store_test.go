package jobs

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCreatesAndReturnsJobCopies(t *testing.T) {
	store := NewMemoryStore()

	job, err := store.Create(CreateJobParams{
		Name:        "demo",
		Type:        "demo.sleep",
		Payload:     map[string]any{"duration_ms": float64(250)},
		MaxAttempts: 2,
		Metadata:    map[string]string{"owner": "test"},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if job.ID == "" {
		t.Fatal("expected generated job ID")
	}
	if job.Status != StatusQueued {
		t.Fatalf("expected status %q, got %q", StatusQueued, job.Status)
	}
	if job.MaxAttempts != 2 {
		t.Fatalf("expected max attempts 2, got %d", job.MaxAttempts)
	}
	if len(job.Logs) != 1 || job.Logs[0].Message != "job queued" {
		t.Fatalf("expected initial queued log, got %#v", job.Logs)
	}

	job.Payload["duration_ms"] = float64(999)
	job.Metadata["owner"] = "mutated"
	job.Logs[0].Message = "mutated"

	got, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Payload["duration_ms"] != float64(250) {
		t.Fatalf("store payload was mutated through returned job: %#v", got.Payload)
	}
	if got.Metadata["owner"] != "test" {
		t.Fatalf("store metadata was mutated through returned job: %#v", got.Metadata)
	}
	if got.Logs[0].Message != "job queued" {
		t.Fatalf("store logs were mutated through returned job: %#v", got.Logs)
	}
}

func TestMemoryStoreRequeuesExpiredLease(t *testing.T) {
	store := NewMemoryStore()
	job, err := store.Create(CreateJobParams{Name: "demo", Type: "demo.sleep", MaxAttempts: 2})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.MarkRunningWithLease(job.ID, "worker-1", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("mark running with lease: %v", err)
	}

	reclaimed, err := store.RequeueExpiredLeases(time.Now())
	if err != nil {
		t.Fatalf("requeue expired leases: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].Status != StatusQueued {
		t.Fatalf("expected requeued job, got %#v", reclaimed)
	}
}

func TestMemoryStoreClaimsOldestQueuedJob(t *testing.T) {
	store := NewMemoryStore()
	first, err := store.Create(CreateJobParams{Name: "first", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create first job: %v", err)
	}
	second, err := store.Create(CreateJobParams{Name: "second", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create second job: %v", err)
	}

	claimed, err := store.ClaimQueued("worker-1", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("claim queued job: %v", err)
	}
	if claimed.ID != first.ID {
		t.Fatalf("expected oldest job %q, got %q", first.ID, claimed.ID)
	}
	if claimed.Status != StatusRunning || claimed.Attempts != 1 || claimed.LeaseOwner != "worker-1" || claimed.LeaseUntil == nil {
		t.Fatalf("unexpected claimed job: %#v", claimed)
	}

	next, err := store.ClaimQueued("worker-2", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("claim second queued job: %v", err)
	}
	if next.ID != second.ID {
		t.Fatalf("expected second job %q, got %q", second.ID, next.ID)
	}
}

func TestMemoryStoreClaimQueuedNotFound(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.ClaimQueued("worker-1", time.Now().Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemoryStoreDeadLettersExpiredLeaseAfterAttempts(t *testing.T) {
	store := NewMemoryStore()
	job, err := store.Create(CreateJobParams{Name: "demo", Type: "demo.sleep", MaxAttempts: 1})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.MarkRunningWithLease(job.ID, "worker-1", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("mark running with lease: %v", err)
	}

	reclaimed, err := store.RequeueExpiredLeases(time.Now())
	if err != nil {
		t.Fatalf("requeue expired leases: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].Status != StatusDeadLetter {
		t.Fatalf("expected dead-lettered job, got %#v", reclaimed)
	}
}

func TestMemoryStoreStateTransitions(t *testing.T) {
	store := NewMemoryStore()
	job, err := store.Create(CreateJobParams{Name: "demo", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	running, err := store.MarkRunning(job.ID)
	if err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if running.Status != StatusRunning {
		t.Fatalf("expected running status, got %q", running.Status)
	}
	if running.Attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", running.Attempts)
	}
	if running.StartedAt == nil {
		t.Fatal("expected started timestamp")
	}

	if err := store.AppendLog(job.ID, "custom log"); err != nil {
		t.Fatalf("append log: %v", err)
	}

	succeeded, err := store.MarkSucceeded(job.ID, "job succeeded")
	if err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	if succeeded.Status != StatusSucceeded {
		t.Fatalf("expected succeeded status, got %q", succeeded.Status)
	}
	if succeeded.FinishedAt == nil {
		t.Fatal("expected finished timestamp")
	}
	if succeeded.Error != "" {
		t.Fatalf("expected empty error, got %q", succeeded.Error)
	}
	if len(succeeded.Logs) < 4 {
		t.Fatalf("expected transition logs, got %#v", succeeded.Logs)
	}
}

func TestMemoryStoreCancelsQueuedJob(t *testing.T) {
	store := NewMemoryStore()
	job, err := store.Create(CreateJobParams{Name: "demo", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	canceled, err := store.MarkCanceled(job.ID, "job canceled")
	if err != nil {
		t.Fatalf("mark canceled: %v", err)
	}
	if canceled.Status != StatusCanceled {
		t.Fatalf("expected canceled status, got %q", canceled.Status)
	}
	if canceled.FinishedAt == nil {
		t.Fatal("expected finished timestamp")
	}
	if len(canceled.Logs) != 2 || canceled.Logs[1].Message != "job canceled" {
		t.Fatalf("expected cancel log, got %#v", canceled.Logs)
	}
}

func TestMemoryStoreDoesNotCancelRunningJob(t *testing.T) {
	store := NewMemoryStore()
	job, err := store.Create(CreateJobParams{Name: "demo", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.MarkRunning(job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	if _, err := store.MarkCanceled(job.ID, "job canceled"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemoryStoreNotFound(t *testing.T) {
	store := NewMemoryStore()

	_, err := store.Get("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from get, got %v", err)
	}

	_, err = store.MarkRunning("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from mark running, got %v", err)
	}

	if err := store.AppendLog("missing", "log"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from append log, got %v", err)
	}
}
