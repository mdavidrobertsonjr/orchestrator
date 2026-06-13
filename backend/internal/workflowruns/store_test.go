package workflowruns

import (
	"testing"
	"time"
)

func TestMemoryStoreCreatesAndListsRuns(t *testing.T) {
	store := NewMemoryStore()
	run, err := store.Create(CreateRunParams{
		WorkflowID: "workflow-1",
		JobID:      "job-1",
		Trigger:    "manual",
		Metadata:   map[string]string{"owner": "test"},
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.ID == "" || run.Status != "queued" {
		t.Fatalf("unexpected run: %#v", run)
	}

	byWorkflow, err := store.ListByWorkflow("workflow-1")
	if err != nil {
		t.Fatalf("list by workflow: %v", err)
	}
	if len(byWorkflow) != 1 || byWorkflow[0].ID != run.ID {
		t.Fatalf("expected workflow run %q, got %#v", run.ID, byWorkflow)
	}

	run.Metadata["owner"] = "mutated"
	got, err := store.Get(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Metadata["owner"] != "test" {
		t.Fatalf("store metadata mutated through returned run: %#v", got.Metadata)
	}
}

func TestMemoryStoreIdempotencyAndStatusUpdate(t *testing.T) {
	store := NewMemoryStore()
	scheduledFor := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	run, err := store.Create(CreateRunParams{
		WorkflowID:     "workflow-1",
		JobID:          "job-1",
		Trigger:        "schedule",
		ScheduledFor:   &scheduledFor,
		IdempotencyKey: "workflow-1:2026-06-13T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	byKey, err := store.GetByIdempotencyKey("workflow-1:2026-06-13T12:00:00Z")
	if err != nil {
		t.Fatalf("get by idempotency key: %v", err)
	}
	if byKey.ID != run.ID || byKey.ScheduledFor == nil || !byKey.ScheduledFor.Equal(scheduledFor) {
		t.Fatalf("unexpected idempotency lookup: %#v", byKey)
	}

	updated, err := store.MarkStatusByJob("job-1", "succeeded")
	if err != nil {
		t.Fatalf("mark status by job: %v", err)
	}
	if updated.Status != "succeeded" {
		t.Fatalf("expected succeeded status, got %q", updated.Status)
	}
}
