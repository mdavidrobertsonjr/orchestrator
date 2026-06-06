SHELL := /bin/bash

ifneq (,$(wildcard .env))
include .env
export
endif

POSTGRES_URL := postgres://orchestrator:orchestrator@localhost:5432/orchestrator?sslmode=disable
BACKEND_ENV := GOCACHE=/tmp/go-build-cache ORCH_ADDR=:8080 ORCH_WORKERS=2

.PHONY: dev dev-postgres website website-postgres frontend backend postgres postgres-stop test

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

postgres:
	docker compose up -d postgres

postgres-stop:
	docker compose down

test:
	cd backend && env GOCACHE=/tmp/go-build-cache go test ./...
	cd frontend && npm run build
