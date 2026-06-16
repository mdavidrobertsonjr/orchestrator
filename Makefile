SHELL := /bin/bash

ifneq (,$(wildcard .env))
include .env
export
endif

POSTGRES_URL := postgres://orchestrator:orchestrator@localhost:5432/orchestrator?sslmode=disable
BACKEND_ENV := GOCACHE=/tmp/go-build-cache ORCH_ADDR=:8080 ORCH_WORKERS=2

.PHONY: dev dev-postgres dev-distributed smoke-distributed smoke-postgres compose-app compose-app-stop website website-postgres frontend backend worker postgres postgres-stop demo seed-job-monitors test

dev:
	(cd backend && env $(BACKEND_ENV) go run ./cmd/api) & \
	backend_pid=$$!; \
	trap 'kill $$backend_pid 2>/dev/null' EXIT INT TERM; \
	cd frontend && npm run dev

dev-postgres: postgres
	(cd backend && env $(BACKEND_ENV) ORCH_DATABASE_URL='$(POSTGRES_URL)' go run ./cmd/api) & \
	backend_pid=$$!; \
	trap 'kill $$backend_pid 2>/dev/null' EXIT INT TERM; \
	cd frontend && npm run dev

dev-distributed: postgres
	(cd backend && env $(BACKEND_ENV) ORCH_DATABASE_URL='$(POSTGRES_URL)' ORCH_EMBEDDED_WORKERS=false go run ./cmd/api) & \
	api_pid=$$!; \
	(cd backend && env GOCACHE=/tmp/go-build-cache ORCH_DATABASE_URL='$(POSTGRES_URL)' ORCH_WORKERS=1 go run ./cmd/worker) & \
	worker_pid=$$!; \
	trap 'kill $$api_pid $$worker_pid 2>/dev/null' EXIT INT TERM; \
	cd frontend && npm run dev

website:
	cd frontend && npm run build
	cd backend && env $(BACKEND_ENV) go run ./cmd/api

website-postgres: postgres
	cd frontend && npm run build
	cd backend && env $(BACKEND_ENV) ORCH_DATABASE_URL='$(POSTGRES_URL)' go run ./cmd/api

frontend:
	cd frontend && npm run dev

backend:
	cd backend && env $(BACKEND_ENV) go run ./cmd/api

worker: postgres
	cd backend && env GOCACHE=/tmp/go-build-cache ORCH_DATABASE_URL='$(POSTGRES_URL)' ORCH_WORKERS=1 go run ./cmd/worker

smoke-distributed:
	bash scripts/smoke_distributed.sh

smoke-postgres: postgres
	bash scripts/smoke_postgres.sh

compose-app:
	docker compose --profile app up --build

compose-app-stop:
	docker compose --profile app down

postgres:
	docker compose up -d postgres

postgres-stop:
	docker compose down

demo:
	bash scripts/demo.sh

seed-job-monitors:
	bash scripts/seed_job_monitors.sh

test:
	cd backend && env GOCACHE=/tmp/go-build-cache go test ./...
	cd frontend && npm run build
