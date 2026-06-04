package results

import (
	"errors"
	"testing"
)

func TestMemoryStoreCreatesAndReturnsResultCopies(t *testing.T) {
	store := NewMemoryStore()

	result, err := store.Create(CreateResultParams{
		JobID:      "job-1",
		WorkflowID: "workflow-1",
		Type:       "monitor.summary",
		Summary:    "1 new match",
		Data:       map[string]any{"created": float64(1)},
	})
	if err != nil {
		t.Fatalf("create result: %v", err)
	}
	if result.ID == "" {
		t.Fatal("expected generated result ID")
	}

	result.Data["created"] = float64(99)

	got, err := store.Get(result.ID)
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.Data["created"] != float64(1) {
		t.Fatalf("store data mutated through returned result: %#v", got.Data)
	}
}

func TestMemoryStoreListsResultsByJobAndWorkflow(t *testing.T) {
	store := NewMemoryStore()

	first, err := store.Create(CreateResultParams{JobID: "job-1", WorkflowID: "workflow-1", Type: "monitor.summary"})
	if err != nil {
		t.Fatalf("create first result: %v", err)
	}
	if _, err := store.Create(CreateResultParams{JobID: "job-2", WorkflowID: "workflow-2", Type: "monitor.summary"}); err != nil {
		t.Fatalf("create second result: %v", err)
	}

	byJob, err := store.ListByJob("job-1")
	if err != nil {
		t.Fatalf("list by job: %v", err)
	}
	if len(byJob) != 1 || byJob[0].ID != first.ID {
		t.Fatalf("expected first result by job, got %#v", byJob)
	}

	byWorkflow, err := store.ListByWorkflow("workflow-1")
	if err != nil {
		t.Fatalf("list by workflow: %v", err)
	}
	if len(byWorkflow) != 1 || byWorkflow[0].ID != first.ID {
		t.Fatalf("expected first result by workflow, got %#v", byWorkflow)
	}
}

func TestMemoryStoreNotFound(t *testing.T) {
	store := NewMemoryStore()

	_, err := store.Get("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
