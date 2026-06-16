//go:build integration

package integration

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/notifications"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/results"
	"orchestrator/backend/internal/scheduler"
	"orchestrator/backend/internal/workers"
	"orchestrator/backend/internal/workflowruns"
	"orchestrator/backend/internal/workflows"
)

func databaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("ORCH_DATABASE_URL")
	if url == "" {
		t.Skip("ORCH_DATABASE_URL is required for integration tests")
	}
	return url
}

func TestPostgresProductionPaths(t *testing.T) {
	ctx := context.Background()
	url := databaseURL(t)
	marker := "integration-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	jobStore, err := jobs.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	defer jobStore.Close()
	if err := jobStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate jobs: %v", err)
	}

	postingStore, err := postings.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open posting store: %v", err)
	}
	defer postingStore.Close()
	if err := postingStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate postings: %v", err)
	}

	resultStore, err := results.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open result store: %v", err)
	}
	defer resultStore.Close()
	if err := resultStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate results: %v", err)
	}

	workflowStore, err := workflows.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open workflow store: %v", err)
	}
	defer workflowStore.Close()
	if err := workflowStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate workflows: %v", err)
	}

	runStore, err := workflowruns.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open workflow run store: %v", err)
	}
	defer runStore.Close()
	if err := runStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate workflow runs: %v", err)
	}

	notificationStore, err := notifications.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open notification store: %v", err)
	}
	defer notificationStore.Close()
	if err := notificationStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate notifications: %v", err)
	}

	registry, err := workers.NewPostgresRegistry(ctx, url)
	if err != nil {
		t.Fatalf("open worker registry: %v", err)
	}
	defer registry.Close()
	if err := registry.Migrate(ctx); err != nil {
		t.Fatalf("migrate workers: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := jobStore.Create(jobs.CreateJobParams{
			Name:        marker + "-job",
			Type:        "integration.job",
			MaxAttempts: 1,
			Metadata:    map[string]string{"submitted_by": marker},
		}); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}
	jobsPage, jobsTotal, err := jobStore.ListPage(jobs.ListParams{SubmittedBy: marker, Limit: 1})
	if err != nil {
		t.Fatalf("list job page: %v", err)
	}
	if jobsTotal != 2 || len(jobsPage) != 1 {
		t.Fatalf("expected paged jobs total 2 returned 1, got total=%d returned=%d", jobsTotal, len(jobsPage))
	}

	for i := 0; i < 2; i++ {
		if _, _, err := postingStore.Upsert(postings.UpsertPostingParams{
			Company:  marker,
			Title:    "Software Engineer",
			URL:      "https://example.com/" + marker + "/" + strconv.Itoa(i),
			Source:   "integration",
			SourceID: marker + "-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("upsert posting: %v", err)
		}
	}
	postingsPage, postingsTotal, err := postingStore.ListPage(postings.ListParams{Company: marker, Limit: 1})
	if err != nil {
		t.Fatalf("list posting page: %v", err)
	}
	if postingsTotal != 2 || len(postingsPage) != 1 {
		t.Fatalf("expected paged postings total 2 returned 1, got total=%d returned=%d", postingsTotal, len(postingsPage))
	}

	workflow, err := workflowStore.Create(workflows.CreateWorkflowParams{
		Name:            marker + "-workflow",
		JobType:         "integration.job",
		Enabled:         true,
		IntervalSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := runStore.Create(workflowruns.CreateRunParams{
			WorkflowID: workflow.ID,
			JobID:      marker + "-job-" + strconv.Itoa(i),
			Trigger:    "manual",
			Status:     "queued",
		}); err != nil {
			t.Fatalf("create workflow run: %v", err)
		}
	}
	runsPage, runsTotal, err := runStore.ListPage(workflowruns.ListParams{WorkflowID: workflow.ID, Limit: 1})
	if err != nil {
		t.Fatalf("list workflow run page: %v", err)
	}
	if runsTotal != 2 || len(runsPage) != 1 {
		t.Fatalf("expected paged runs total 2 returned 1, got total=%d returned=%d", runsTotal, len(runsPage))
	}

	for i := 0; i < 2; i++ {
		if _, err := resultStore.Create(results.CreateResultParams{
			WorkflowID: workflow.ID,
			Type:       "integration.result",
			Summary:    marker,
		}); err != nil {
			t.Fatalf("create result: %v", err)
		}
	}
	resultsPage, resultsTotal, err := resultStore.ListPage(results.ListParams{WorkflowID: workflow.ID, Type: "integration.result", Limit: 1})
	if err != nil {
		t.Fatalf("list result page: %v", err)
	}
	if resultsTotal != 2 || len(resultsPage) != 1 {
		t.Fatalf("expected paged results total 2 returned 1, got total=%d returned=%d", resultsTotal, len(resultsPage))
	}

	delivery, err := notificationStore.Create(notifications.CreateDeliveryParams{
		WorkflowID: workflow.ID,
		Kind:       "integration",
		Provider:   "smtp",
		Recipients: []string{"test@example.com"},
		Subject:    marker,
	})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	if _, err := notificationStore.MarkAttempt(delivery.ID); err != nil {
		t.Fatalf("mark delivery attempt: %v", err)
	}
	if _, err := notificationStore.MarkFailed(delivery.ID, "integration failure"); err != nil {
		t.Fatalf("mark delivery failed: %v", err)
	}
	deliveries, err := notificationStore.List()
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	foundDelivery := false
	for _, stored := range deliveries {
		if stored.ID == delivery.ID && stored.Status == "failed" && stored.Attempts == 1 {
			foundDelivery = true
		}
	}
	if !foundDelivery {
		t.Fatalf("expected failed delivery with one attempt in %#v", deliveries)
	}
}

func TestPostgresSchedulerAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	url := databaseURL(t)
	lockName := "integration-scheduler-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	first, err := scheduler.NewPostgresAdvisoryLock(ctx, url, lockName)
	if err != nil {
		t.Fatalf("create first lock: %v", err)
	}
	defer first.Close()
	second, err := scheduler.NewPostgresAdvisoryLock(ctx, url, lockName)
	if err != nil {
		t.Fatalf("create second lock: %v", err)
	}
	defer second.Close()

	unlock, acquired, err := first.TryLock(ctx)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if !acquired {
		t.Fatal("expected first lock acquisition")
	}

	_, acquired, err = second.TryLock(ctx)
	if err != nil {
		t.Fatalf("acquire second lock: %v", err)
	}
	if acquired {
		t.Fatal("expected second lock to be blocked")
	}

	unlock()
	unlockSecond, acquired, err := second.TryLock(ctx)
	if err != nil {
		t.Fatalf("acquire second lock after release: %v", err)
	}
	if !acquired {
		t.Fatal("expected second lock after release")
	}
	unlockSecond()
}
