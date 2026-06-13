package workflowruns

import "testing"

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
