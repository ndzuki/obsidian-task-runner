package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestAcquireAndRefreshGrillLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "TASK-001.md")
	if err := os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: needs-grilling\ngrill_owner: \"\"\ngrill_started_at: \"\"\ngrill_heartbeat_at: \"\"\ngrill_timeout_minutes: 30\n---\n# Task\n"), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	acquired, err := AcquireGrillLease(path, "user", now)
	if err != nil || !acquired {
		t.Fatalf("AcquireGrillLease = %v, %v", acquired, err)
	}
	if err := RefreshGrillLease(path, "user", now.Add(time.Minute)); err != nil {
		t.Fatalf("RefreshGrillLease: %v", err)
	}
	data, _ := os.ReadFile(path)
	fm, _ := yamlfrontmatter.Parse(data)
	if fm.GrillOwner != "user" || fm.GrillStartedAt != now.Format(time.RFC3339) || fm.GrillHeartbeatAt != now.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("lease fields = owner %q start %q heartbeat %q", fm.GrillOwner, fm.GrillStartedAt, fm.GrillHeartbeatAt)
	}
}

func TestPreemptExpiredGrillLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "TASK-001.md")
	if err := os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: needs-grilling\ngrill_owner: user\ngrill_started_at: \"2026-07-28T09:00:00+08:00\"\ngrill_heartbeat_at: \"2026-07-28T09:20:00+08:00\"\ngrill_timeout_minutes: 30\n---\n# Task\n"), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	preempted, err := PreemptExpiredGrillLease(path, time.Date(2026, 7, 28, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if err != nil || !preempted {
		t.Fatalf("PreemptExpiredGrillLease = %v, %v", preempted, err)
	}
}
