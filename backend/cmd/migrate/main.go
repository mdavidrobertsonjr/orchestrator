// Command migrate applies the Postgres schema migrations used by the API and
// worker. Production deployments can run this as an explicit release step and
// then start services with ORCH_AUTO_MIGRATE=false.
package main

import (
	"context"
	"log"
	"os"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/notifications"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/results"
	"orchestrator/backend/internal/workers"
	"orchestrator/backend/internal/workflowruns"
	"orchestrator/backend/internal/workflows"
)

func main() {
	url := os.Getenv("ORCH_DATABASE_URL")
	if url == "" {
		log.Fatal("ORCH_DATABASE_URL is required")
	}
	if err := migrate(context.Background(), url); err != nil {
		log.Fatal(err)
	}
	log.Print("database migrations applied")
}

func migrate(ctx context.Context, url string) error {
	jobStore, err := jobs.NewPostgresStore(ctx, url)
	if err != nil {
		return err
	}
	defer jobStore.Close()
	if err := jobStore.Migrate(ctx); err != nil {
		return err
	}

	postingStore, err := postings.NewPostgresStore(ctx, url)
	if err != nil {
		return err
	}
	defer postingStore.Close()
	if err := postingStore.Migrate(ctx); err != nil {
		return err
	}

	resultStore, err := results.NewPostgresStore(ctx, url)
	if err != nil {
		return err
	}
	defer resultStore.Close()
	if err := resultStore.Migrate(ctx); err != nil {
		return err
	}

	workflowStore, err := workflows.NewPostgresStore(ctx, url)
	if err != nil {
		return err
	}
	defer workflowStore.Close()
	if err := workflowStore.Migrate(ctx); err != nil {
		return err
	}

	runStore, err := workflowruns.NewPostgresStore(ctx, url)
	if err != nil {
		return err
	}
	defer runStore.Close()
	if err := runStore.Migrate(ctx); err != nil {
		return err
	}

	notificationStore, err := notifications.NewPostgresStore(ctx, url)
	if err != nil {
		return err
	}
	defer notificationStore.Close()
	if err := notificationStore.Migrate(ctx); err != nil {
		return err
	}

	registry, err := workers.NewPostgresRegistry(ctx, url)
	if err != nil {
		return err
	}
	defer registry.Close()
	return registry.Migrate(ctx)
}
