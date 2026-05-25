package workers

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryRegistryTracksWorkerLifecycle(t *testing.T) {
	registry := NewMemoryRegistry()

	registered, err := registry.Register("worker-1")
	if err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if registered.Status != StatusIdle {
		t.Fatalf("expected idle status, got %q", registered.Status)
	}
	if registered.LastHeartbeat.IsZero() {
		t.Fatal("expected heartbeat timestamp")
	}

	running, err := registry.MarkRunning("worker-1", "job-1")
	if err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if running.Status != StatusRunning {
		t.Fatalf("expected running status, got %q", running.Status)
	}
	if running.CurrentJobID != "job-1" {
		t.Fatalf("expected current job job-1, got %q", running.CurrentJobID)
	}

	idle, err := registry.MarkIdle("worker-1")
	if err != nil {
		t.Fatalf("mark idle: %v", err)
	}
	if idle.Status != StatusIdle {
		t.Fatalf("expected idle status, got %q", idle.Status)
	}
	if idle.CurrentJobID != "" {
		t.Fatalf("expected no current job, got %q", idle.CurrentJobID)
	}

	stopped, err := registry.MarkStopped("worker-1")
	if err != nil {
		t.Fatalf("mark stopped: %v", err)
	}
	if stopped.Status != StatusStopped {
		t.Fatalf("expected stopped status, got %q", stopped.Status)
	}
}

func TestMemoryRegistryHeartbeatUpdatesLastSeen(t *testing.T) {
	registry := NewMemoryRegistry()

	registered, err := registry.Register("worker-1")
	if err != nil {
		t.Fatalf("register worker: %v", err)
	}

	time.Sleep(time.Millisecond)
	if err := registry.Heartbeat("worker-1"); err != nil {
		t.Fatalf("heartbeat worker: %v", err)
	}

	got, err := registry.Get("worker-1")
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if !got.LastHeartbeat.After(registered.LastHeartbeat) {
		t.Fatalf("expected heartbeat to advance from %s to %s", registered.LastHeartbeat, got.LastHeartbeat)
	}
}

func TestMemoryRegistryReturnsWorkersSortedByID(t *testing.T) {
	registry := NewMemoryRegistry()

	for _, id := range []string{"worker-2", "worker-1"} {
		if _, err := registry.Register(id); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	got := registry.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(got))
	}
	if got[0].ID != "worker-1" || got[1].ID != "worker-2" {
		t.Fatalf("expected sorted workers, got %#v", got)
	}
}

func TestMemoryRegistryNotFound(t *testing.T) {
	registry := NewMemoryRegistry()

	if err := registry.Heartbeat("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from heartbeat, got %v", err)
	}

	_, err := registry.MarkRunning("missing", "job-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from mark running, got %v", err)
	}

	_, err = registry.Get("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from get, got %v", err)
	}
}
