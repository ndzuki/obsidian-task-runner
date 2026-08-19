package vaultweb

import (
	"errors"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

func TestUpdateTaskWritableField(t *testing.T) {
	s := New(newTestVault(t))
	updated, err := s.UpdateTask("demo", "001", TaskUpdateRequest{
		ExpectedGeneration: 1,
		Updates:            map[string]any{"priority": "P0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Priority != "P0" {
		t.Fatalf("priority=%q, want P0", updated.Priority)
	}
}

func TestUpdateTaskStaleGenerationRejected(t *testing.T) {
	s := New(newTestVault(t))
	_, err := s.UpdateTask("demo", "001", TaskUpdateRequest{
		ExpectedGeneration: 999, // does not match on-disk generation 1
		Updates:            map[string]any{"priority": "P0"},
	})
	if !errors.Is(err, task.ErrStaleGeneration) {
		t.Fatalf("err=%v, want ErrStaleGeneration", err)
	}
	// Field unchanged.
	tasks, terr := s.Tasks("demo")
	if terr != nil {
		t.Fatal(terr)
	}
	for _, tt := range tasks {
		if tt.ID == "001" && tt.Priority != "P1" {
			t.Fatalf("stale write must not modify task, priority=%q", tt.Priority)
		}
	}
}

func TestUpdateTaskRejectsSystemField(t *testing.T) {
	s := New(newTestVault(t))
	// status is System-owned: must be rejected regardless of generation.
	_, err := s.UpdateTask("demo", "001", TaskUpdateRequest{
		ExpectedGeneration: 1,
		Updates:            map[string]any{"status": "done"},
	})
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("err=%v, want ErrNotWritable", err)
	}
	_, err = s.UpdateTask("demo", "001", TaskUpdateRequest{
		ExpectedGeneration: 1,
		Updates:            map[string]any{"generation": 99},
	})
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("generation write err=%v, want ErrNotWritable", err)
	}
}

func TestUpdateTaskGateFields(t *testing.T) {
	s := New(newTestVault(t))
	// plan_approved is a Shared human-gate field and is writable.
	updated, err := s.UpdateTask("demo", "001", TaskUpdateRequest{
		ExpectedGeneration: 1,
		Updates:            map[string]any{"plan_approved": true, "resume_approved": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != "001" {
		t.Fatalf("unexpected task returned: %+v", updated)
	}
}

func TestUpdateTaskUnknownTask(t *testing.T) {
	s := New(newTestVault(t))
	if _, err := s.UpdateTask("demo", "999", TaskUpdateRequest{
		ExpectedGeneration: 1,
		Updates:            map[string]any{"priority": "P1"},
	}); err == nil {
		t.Fatal("unknown task must fail")
	}
}

func TestUpdateTaskEmptyUpdates(t *testing.T) {
	s := New(newTestVault(t))
	if _, err := s.UpdateTask("demo", "001", TaskUpdateRequest{
		ExpectedGeneration: 1,
		Updates:            map[string]any{},
	}); err == nil {
		t.Fatal("empty updates must fail")
	}
}
