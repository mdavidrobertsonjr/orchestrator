package workflows

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCreatesAndReturnsWorkflowCopies(t *testing.T) {
	store := NewMemoryStore()
	nextRunAt := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	workflow, err := store.Create(CreateWorkflowParams{
		Name:            "Datadog monitor",
		JobType:         "jobs.monitor.new_grad",
		Payload:         map[string]any{"min_score": float64(20)},
		Metadata:        map[string]string{"owner": "test"},
		MaxAttempts:     2,
		Enabled:         true,
		IntervalSeconds: 3600,
		NextRunAt:       &nextRunAt,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	if workflow.ID == "" {
		t.Fatal("expected generated workflow ID")
	}
	if workflow.MaxAttempts != 2 {
		t.Fatalf("expected max attempts 2, got %d", workflow.MaxAttempts)
	}
	if !workflow.NextRunAt.Equal(nextRunAt) {
		t.Fatalf("expected next run %s, got %s", nextRunAt, workflow.NextRunAt)
	}

	workflow.Payload["min_score"] = float64(99)
	workflow.Metadata["owner"] = "mutated"

	got, err := store.Get(workflow.ID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	if got.Payload["min_score"] != float64(20) {
		t.Fatalf("store payload mutated through returned workflow: %#v", got.Payload)
	}
	if got.Metadata["owner"] != "test" {
		t.Fatalf("store metadata mutated through returned workflow: %#v", got.Metadata)
	}
}

func TestMemoryStoreListDue(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)

	due, err := store.Create(CreateWorkflowParams{Name: "due", JobType: "demo.sleep", Enabled: true, IntervalSeconds: 60, NextRunAt: &past})
	if err != nil {
		t.Fatalf("create due workflow: %v", err)
	}
	if _, err := store.Create(CreateWorkflowParams{Name: "future", JobType: "demo.sleep", Enabled: true, IntervalSeconds: 60, NextRunAt: &future}); err != nil {
		t.Fatalf("create future workflow: %v", err)
	}
	if _, err := store.Create(CreateWorkflowParams{Name: "disabled", JobType: "demo.sleep", Enabled: false, IntervalSeconds: 60, NextRunAt: &past}); err != nil {
		t.Fatalf("create disabled workflow: %v", err)
	}

	workflows, err := store.ListDue(now)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(workflows) != 1 || workflows[0].ID != due.ID {
		t.Fatalf("expected only due workflow %q, got %#v", due.ID, workflows)
	}
}

func TestMemoryStoreMarkDispatched(t *testing.T) {
	store := NewMemoryStore()
	workflow, err := store.Create(CreateWorkflowParams{Name: "due", JobType: "demo.sleep", Enabled: true, IntervalSeconds: 60})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	lastRunAt := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	nextRunAt := lastRunAt.Add(time.Hour)
	updated, err := store.MarkDispatched(workflow.ID, "job-1", lastRunAt, nextRunAt)
	if err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(lastRunAt) {
		t.Fatalf("expected last run %s, got %#v", lastRunAt, updated.LastRunAt)
	}
	if updated.LastJobID != "job-1" {
		t.Fatalf("expected last job job-1, got %q", updated.LastJobID)
	}
	if !updated.NextRunAt.Equal(nextRunAt) {
		t.Fatalf("expected next run %s, got %s", nextRunAt, updated.NextRunAt)
	}
}

func TestMemoryStoreSetEnabled(t *testing.T) {
	store := NewMemoryStore()
	workflow, err := store.Create(CreateWorkflowParams{Name: "due", JobType: "demo.sleep", Enabled: true, IntervalSeconds: 60})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	updated, err := store.SetEnabled(workflow.ID, false)
	if err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if updated.Enabled {
		t.Fatal("expected workflow to be disabled")
	}

	updated, err = store.SetEnabled(workflow.ID, true)
	if err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if !updated.Enabled {
		t.Fatal("expected workflow to be enabled")
	}
}

func TestMemoryStoreNotFound(t *testing.T) {
	store := NewMemoryStore()

	_, err := store.Get("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from get, got %v", err)
	}

	_, err = store.MarkDispatched("missing", "job-1", time.Now(), time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from mark dispatched, got %v", err)
	}

	_, err = store.SetEnabled("missing", true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from set enabled, got %v", err)
	}
}
