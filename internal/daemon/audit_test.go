package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhaseLoggerWritesRedactedJSONL(t *testing.T) {
	var output bytes.Buffer
	logger := newPhaseLogger(&output)
	logger.Event(phaseEvent{
		Timestamp: time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		VaultID: "vault", Project: "otg", TaskPathHash: "abc", TaskID: "003",
		Phase: "planning", Event: "FAILED", ErrorCode: ErrModelFailed,
		Message: "Authorization: Bearer secret-token",
	})
	var payload map[string]interface{}
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSONL: %v: %s", err, output.String())
	}
	if payload["message"] != "Authorization: Bearer <redacted>" {
		t.Fatalf("log was not redacted: %s", output.String())
	}
	if payload["error_code"] != string(ErrModelFailed) || payload["task_id"] != "003" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestAppendAuditRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "TASK-003.md")
	if err := os.WriteFile(path, []byte("---\nid: \"003\"\nstatus: planning\n---\n# Task\n\n## 变更记录\n1. old\n"), 0o644); err != nil {
		t.Fatalf("write TASK: %v", err)
	}
	if err := AppendAuditRecord(path, auditRecord{Actor: "daemon", Event: "PLAN_GENERATED", From: "planning", To: "plan-review", Plan: "v10", ErrorCode: "<none>", Hash: "sha256:abc"}); err != nil {
		t.Fatalf("AppendAuditRecord: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "actor=daemon event=PLAN_GENERATED from=planning to=plan-review plan=v10 error_code=<none> hash=sha256:abc") {
		t.Fatalf("audit record missing: %s", data)
	}
}
