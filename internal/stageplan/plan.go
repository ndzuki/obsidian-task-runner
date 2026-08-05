// Package stageplan deterministically derives delivery phases from task
// dependency topology — no LLM round-trip (release-manager first staging
// took hours of PM sessions; this runs in milliseconds). The core invariant
// is acyclic ordering: a phase-N task may only depend on tasks in phases
// ≤ N-1 (or already-done tasks), which is what kills the TASK-066 deadlock
// (e2e scenarios always land after the features they exercise).
package stageplan

import (
	"fmt"
	"sort"
	"strings"
)

// TaskInfo is the minimal frontmatter projection needed for phasing.
type TaskInfo struct {
	ID        string   // task numeric id, e.g. "018"
	Title     string
	Epic      string
	Priority  string
	BlockedBy []string // in-project task ids this task depends on
	Stage     string   // existing stage ("P1"); already-staged tasks are kept
}

// Phase is one delivery stage.
type Phase struct {
	Number int
	Name   string   // derived from member titles/epics, e.g. "发布核心收敛"
	Tasks  []string // task ids in topological order
}

// Options tunes phase derivation.
type Options struct {
	MinTasksPerPhase int // floor for merging layers (default 3)
	MaxPhases        int // ceiling (default 4)
}

func (o Options) withDefaults() Options {
	if o.MinTasksPerPhase <= 0 {
		o.MinTasksPerPhase = 3
	}
	if o.MaxPhases <= 0 {
		o.MaxPhases = 4
	}
	return o
}

// BuildPhases partitions unstaged in-flight tasks into phases.
//
// Algorithm:
//  1. Topological layering — repeatedly peel tasks whose dependencies are
//     all done (not in the working set) or already placed in an earlier
//     layer; residual tasks (cycles / external blockers) form a final layer.
//  2. Layer merging — merge consecutive layers into phases of
//     MinTasksPerPhase..~2x, capped at MaxPhases, so a 22-task project
//     becomes 3-4 phases instead of 10 layers.
//
// Already-staged tasks are excluded from the working set (they keep their
// stage); their ids still count as satisfied dependencies when layering.
func BuildPhases(tasks []TaskInfo, opts Options) []Phase {
	opts = opts.withDefaults()

	working := make(map[string]TaskInfo)
	for _, t := range tasks {
		if t.Stage == "" {
			working[t.ID] = t
		}
	}
	if len(working) == 0 {
		return nil
	}

	// Layering.
	var layers [][]string
	placed := make(map[string]bool)
	for len(working) > 0 {
		var layer []string
		for id, t := range working {
			if depsSatisfied(t, placed, working) {
				layer = append(layer, id)
			}
		}
		if len(layer) == 0 {
			// Cycle or unresolvable external dependency: dump the remainder
			// into a final layer rather than looping forever.
			for id := range working {
				layer = append(layer, id)
			}
		}
		sort.Strings(layer)
		for _, id := range layer {
			placed[id] = true
			delete(working, id)
		}
		layers = append(layers, layer)
		// No safety valve needed: every iteration deletes at least one task
		// from working (the cycle branch dumps the whole remainder), so the
		// loop terminates with working empty by construction.
	}

	// Merge layers into phases (balanced, capped).
	var phases []Phase
	var current []string
	flush := func(number int) {
		if len(current) == 0 {
			return
		}
		phases = append(phases, Phase{Number: number, Tasks: current, Name: deriveName(current, tasks)})
		current = nil
	}
	for _, layer := range layers {
		if len(phases) == opts.MaxPhases-1 {
			// Everything left joins the final phase.
			current = append(current, layer...)
			continue
		}
		current = append(current, layer...)
		if len(current) >= opts.MinTasksPerPhase {
			flush(len(phases) + 1)
		}
	}
	flush(len(phases) + 1)
	return phases
}

// depsSatisfied reports whether every in-project dependency of t is either
// already placed in an earlier layer or not in the working set (done /
// closed / staged / external).
func depsSatisfied(t TaskInfo, placed map[string]bool, working map[string]TaskInfo) bool {
	for _, dep := range t.BlockedBy {
		if placed[dep] {
			continue
		}
		if _, inWork := working[dep]; inWork {
			return false
		}
	}
	return true
}

// deriveName picks a human label for a phase from its member tasks: the
// most common epic prefix, else the first task's title keyword.
func deriveName(ids []string, tasks []TaskInfo) string {
	byID := make(map[string]TaskInfo, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	epicCount := make(map[string]int)
	for _, id := range ids {
		if epic := byID[id].Epic; epic != "" {
			epicCount[epic]++
		}
	}
	if len(epicCount) > 0 {
		best := ""
		bestN := 0
		for epic, n := range epicCount {
			if n > bestN {
				best, bestN = epic, n
			}
		}
		return best
	}
	// Fall back to a compressed member list: "TASK-018/019 等".
	compact := make([]string, 0, len(ids))
	for _, id := range ids {
		compact = append(compact, id)
	}
	return fmt.Sprintf("TASK-%s 等", strings.Join(compact, "/"))
}
