package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// TestAutoArchiveDecisions guards the deterministic fallback: an oversized
// answered list archives its answered blocks into Grilling-Decisions-archive.md,
// keeps pending blocks in the main list, and refreshes the answer hash.
func TestAutoArchiveDecisions(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	notesDir := filepath.Join(projDir, "Notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(notesDir, "Grilling-Decisions.md")
	// Build a list over the threshold: 40 answered blocks (each ~1.4KB) + 1 pending.
	var sb strings.Builder
	sb.WriteString("---\nid: \"grilling-decisions\"\nproject: test\nstatus: open\n---\n# Grilling Decisions — test\n\n")
	for i := 1; i <= 40; i++ {
		sb.WriteString("### D-" + itoa(i) + ": REQ-0" + itoa(i) + " — 测试决策点\n")
		sb.WriteString("- 来源任务: TASK-0" + itoa(i) + "\n")
		sb.WriteString("- 冲突: " + strings.Repeat("冲突描述内容填充 ", 40) + "\n")
		sb.WriteString("- 建议: " + strings.Repeat("建议内容填充 ", 40) + "\n")
		sb.WriteString("- 决策: 已答方案 " + itoa(i) + "（2026-08-07 用户确认）\n\n")
	}
	sb.WriteString("### D-99: REQ-099 — 待答决策\n- 来源任务: TASK-099\n- 冲突: 未决\n- 建议: 待定\n- 决策: （用户填写）\n")
	if err := os.WriteFile(listPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(sb.String()) < decisionArchiveThresholdBytes {
		t.Fatalf("test list too small (%d bytes), threshold %d", len(sb.String()), decisionArchiveThresholdBytes)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)

	if n := runner.autoArchiveDecisions(); n != 40 {
		t.Fatalf("archived %d, want 40", n)
	}

	// Main list: frontmatter + pointer + only the pending block.
	mainData, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mainData), "D-1:") || strings.Contains(string(mainData), "D-40:") {
		t.Fatal("answered blocks must not remain in the main list")
	}
	if !strings.Contains(string(mainData), "D-99") || !strings.Contains(string(mainData), "（用户填写）") {
		t.Fatal("pending block must remain in the main list")
	}
	if !strings.Contains(string(mainData), "Grilling-Decisions-archive") {
		t.Fatal("archive pointer missing")
	}

	// Archive: all 40 answered blocks present.
	archData, err := os.ReadFile(filepath.Join(notesDir, "Grilling-Decisions-archive.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(archData), "### D-1:") || !strings.Contains(string(archData), "### D-40:") {
		t.Fatal("archive must contain answered blocks D-1..D-40")
	}
	if strings.Contains(string(archData), "D-99") {
		t.Fatal("pending block must not leak into the archive")
	}

	// Idempotent: second run archives nothing.
	if n := runner.autoArchiveDecisions(); n != 0 {
		t.Fatalf("second run archived %d, want 0", n)
	}
}
