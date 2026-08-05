package stageplan

import (
	"fmt"
	"reflect"
	"testing"
)

func TestBuildPhasesTopologicalLayering(t *testing.T) {
	tasks := []TaskInfo{
		{ID: "019", Title: "preflight"},
		{ID: "018", Title: "values"},
		{ID: "066", Title: "e2e", BlockedBy: []string{"019", "067"}},
		{ID: "067", Title: "operation", BlockedBy: []string{"019"}},
		{ID: "071", Title: "values-list", BlockedBy: []string{"018"}},
	}
	phases := BuildPhases(tasks, Options{MinTasksPerPhase: 1, MaxPhases: 4})
	// Phase order must be acyclic: 066 depends on 067/019 which depend on 019.
	pos := make(map[string]int)
	for _, p := range phases {
		for _, id := range p.Tasks {
			pos[id] = p.Number
		}
	}
	for _, ti := range tasks {
		for _, dep := range ti.BlockedBy {
			if pos[ti.ID] <= pos[dep] {
				t.Fatalf("dependency violated: %s (phase %d) depends on %s (phase %d)", ti.ID, pos[ti.ID], dep, pos[dep])
			}
		}
	}
}

func TestBuildPhasesSkipsStagedTasks(t *testing.T) {
	tasks := []TaskInfo{
		{ID: "001", Title: "a"},
		{ID: "002", Title: "b", Stage: "P1"}, // already staged
		{ID: "003", Title: "c", BlockedBy: []string{"002"}},
	}
	phases := BuildPhases(tasks, Options{MinTasksPerPhase: 1, MaxPhases: 4})
	if len(phases) != 1 {
		t.Fatalf("phases = %+v, want 1 (staged task excluded)", phases)
	}
	if !reflect.DeepEqual(phases[0].Tasks, []string{"001", "003"}) {
		t.Fatalf("phase tasks = %v, want [001 003]", phases[0].Tasks)
	}
}

func TestBuildPhasesMergesLayers(t *testing.T) {
	// 6 independent tasks should merge into one phase with default floor 3
	// when MaxPhases=4 → 1 phase? Floor 3 means layers merge until >=3 then
	// flush; 6 tasks → one phase of 6.
	tasks := make([]TaskInfo, 6)
	for i := range tasks {
		tasks[i].ID = fmtID(i + 1)
	}
	phases := BuildPhases(tasks, Options{})
	if len(phases) != 1 || len(phases[0].Tasks) != 6 {
		t.Fatalf("phases = %+v, want single phase with 6 tasks", phases)
	}
}

func TestBuildPhasesCycleDumpedToFinalLayer(t *testing.T) {
	tasks := []TaskInfo{
		{ID: "001", BlockedBy: []string{"002"}},
		{ID: "002", BlockedBy: []string{"001"}}, // cycle
	}
	phases := BuildPhases(tasks, Options{MinTasksPerPhase: 1, MaxPhases: 4})
	if len(phases) != 1 {
		t.Fatalf("cycle should dump into one phase, got %+v", phases)
	}
	if len(phases[0].Tasks) != 2 {
		t.Fatalf("cycle phase tasks = %v, want both", phases[0].Tasks)
	}
}

// TestBuildPhasesLongChainNoDrop guards the removed safety valve: a strict
// dependency chain longer than MaxPhases*3 layers (e.g. 15 tasks) must
// still place every task — the old break discarded the tail silently.
func TestBuildPhasesLongChainNoDrop(t *testing.T) {
	tasks := make([]TaskInfo, 15)
	for i := range tasks {
		tasks[i].ID = fmtID(i + 1)
		if i > 0 {
			tasks[i].BlockedBy = []string{fmtID(i)} // 1→2→…→15
		}
	}
	phases := BuildPhases(tasks, Options{MinTasksPerPhase: 1, MaxPhases: 4})
	placed := 0
	for _, p := range phases {
		placed += len(p.Tasks)
	}
	if placed != 15 {
		t.Fatalf("placed %d of 15 tasks — chain tail dropped", placed)
	}
}

func TestBuildPhasesEmpty(t *testing.T) {
	if phases := BuildPhases(nil, Options{}); phases != nil {
		t.Fatalf("nil input should yield nil phases, got %+v", phases)
	}
	allStaged := []TaskInfo{{ID: "001", Stage: "P1"}}
	if phases := BuildPhases(allStaged, Options{}); phases != nil {
		t.Fatalf("all-staged input should yield nil phases, got %+v", phases)
	}
}

func TestDeriveNameUsesEpic(t *testing.T) {
	tasks := []TaskInfo{
		{ID: "001", Title: "a", Epic: "web"},
		{ID: "002", Title: "b", Epic: "web"},
		{ID: "003", Title: "c", Epic: "core"},
	}
	if got := deriveName([]string{"001", "002", "003"}, tasks); got != "web" {
		t.Fatalf("deriveName = %q, want web (majority epic)", got)
	}
}

func fmtID(n int) string {
	return fmt.Sprintf("%03d", n)
}
