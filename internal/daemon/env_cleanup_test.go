package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

func newEnvCleanupTestRunner(t *testing.T, cleanup *config.EnvCleanupConfig) *Runner {
	t.Helper()
	cfg := config.Defaults()
	cfg.EnvCleanup = cleanup
	cfg.Notifications.Desktop = false // no real desktop notifications in tests
	r := New(cfg)
	r.logger = log.New(io.Discard, "", 0)
	return r
}

func TestCleanupMergeEnvDeletesRegistriesThenClustersThenNetworks(t *testing.T) {
	var calls []string
	listK3dRegistries = func() ([]string, error) {
		return []string{"k3d-release-manager-registry"}, nil
	}
	listK3dClusters = func() ([]string, error) {
		return []string{"dev-customer-b-mixed", "dev-customer-a-cache"}, nil
	}
	deleteK3dRegistry = func(name string) error {
		calls = append(calls, "registry:"+name)
		return nil
	}
	deleteK3dCluster = func(name string) error {
		calls = append(calls, "cluster:"+name)
		return nil
	}
	removeDockerNetwork = func(name string) error {
		calls = append(calls, "network:"+name)
		return nil
	}
	t.Cleanup(func() {
		listK3dRegistries = func() ([]string, error) { return nil, nil }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		deleteK3dRegistry = func(string) error { return nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	})

	r := newEnvCleanupTestRunner(t, &config.EnvCleanupConfig{OnMerge: true, Exclude: []string{"kb-reranker", "ollama-sycl"}})
	r.cleanupMergeEnv("065", "一键开发环境")

	// Registries detach first, then clusters in deterministic sorted order,
	// each followed by its docker-network fallback.
	want := []string{
		"registry:k3d-release-manager-registry",
		"cluster:dev-customer-a-cache",
		"network:k3d-dev-customer-a-cache",
		"cluster:dev-customer-b-mixed",
		"network:k3d-dev-customer-b-mixed",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %q, want %q (full: %v)", i, calls[i], want[i], calls)
		}
	}
}

func TestCleanupMergeEnvRespectsExclude(t *testing.T) {
	var calls []string
	listK3dRegistries = func() ([]string, error) {
		return []string{"k3d-release-manager-registry"}, nil
	}
	listK3dClusters = func() ([]string, error) {
		return []string{"deployd-customer", "dev-customer-a-cache"}, nil
	}
	deleteK3dRegistry = func(name string) error {
		calls = append(calls, "registry:"+name)
		return nil
	}
	deleteK3dCluster = func(name string) error {
		calls = append(calls, "cluster:"+name)
		return nil
	}
	removeDockerNetwork = func(name string) error {
		calls = append(calls, "network:"+name)
		return nil
	}
	t.Cleanup(func() {
		listK3dRegistries = func() ([]string, error) { return nil, nil }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		deleteK3dRegistry = func(string) error { return nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	})

	r := newEnvCleanupTestRunner(t, &config.EnvCleanupConfig{OnMerge: true, Exclude: []string{"deployd-customer"}})
	r.cleanupMergeEnv("065", "一键开发环境")

	for _, call := range calls {
		if containsAny(call, "deployd-customer", "k3d-deployd-customer") {
			t.Fatalf("excluded resource was touched: %v", calls)
		}
	}
	if len(calls) != 3 { // registry + cluster + network for the non-excluded cluster
		t.Fatalf("calls = %v, want 3 entries (registry, cluster, network)", calls)
	}
}

func TestCleanupMergeEnvDisabledSkipsEverything(t *testing.T) {
	called := false
	deleteK3dRegistry = func(string) error { called = true; return nil }
	deleteK3dCluster = func(string) error { called = true; return nil }
	removeDockerNetwork = func(string) error { called = true; return nil }
	t.Cleanup(func() {
		deleteK3dRegistry = func(string) error { return nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	})

	r := newEnvCleanupTestRunner(t, &config.EnvCleanupConfig{OnMerge: false})
	r.cleanupMergeEnv("065", "一键开发环境")
	if called {
		t.Fatal("cleanup ran despite OnMerge=false")
	}
}

func TestCleanupMergeEnvDryRunDoesNotDelete(t *testing.T) {
	called := false
	listK3dRegistries = func() ([]string, error) { return []string{"k3d-release-manager-registry"}, nil }
	listK3dClusters = func() ([]string, error) { return []string{"dev-customer-a-cache"}, nil }
	deleteK3dRegistry = func(string) error { called = true; return nil }
	deleteK3dCluster = func(string) error { called = true; return nil }
	removeDockerNetwork = func(string) error { called = true; return nil }
	t.Cleanup(func() {
		listK3dRegistries = func() ([]string, error) { return nil, nil }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		deleteK3dRegistry = func(string) error { return nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	})

	r := newEnvCleanupTestRunner(t, &config.EnvCleanupConfig{OnMerge: true, DryRun: true})
	r.cleanupMergeEnv("065", "一键开发环境")
	if called {
		t.Fatal("dry-run must not invoke delete commands")
	}
}

func TestCleanupMergeEnvNetworkFallbackAfterClusterDeleteFailure(t *testing.T) {
	var calls []string
	listK3dRegistries = func() ([]string, error) { return nil, nil }
	listK3dClusters = func() ([]string, error) { return []string{"dev-customer-a-cache"}, nil }
	deleteK3dCluster = func(name string) error {
		calls = append(calls, "cluster:"+name)
		return os.ErrPermission // simulate `k3d cluster delete` failing mid-way
	}
	removeDockerNetwork = func(name string) error {
		calls = append(calls, "network:"+name)
		return nil
	}
	t.Cleanup(func() {
		listK3dRegistries = func() ([]string, error) { return nil, nil }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	})

	r := newEnvCleanupTestRunner(t, &config.EnvCleanupConfig{OnMerge: true})
	r.cleanupMergeEnv("065", "一键开发环境")

	if len(calls) != 2 || calls[0] != "cluster:dev-customer-a-cache" || calls[1] != "network:k3d-dev-customer-a-cache" {
		t.Fatalf("calls = %v, want network fallback after failed cluster delete", calls)
	}
}

func TestRemoveDockerNetworkTreatsNotFoundAsSuccess(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho 'Error response from daemon: network k3d-dev-customer-a-cache not found' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := removeDockerNetwork("k3d-dev-customer-a-cache"); err != nil {
		t.Fatalf("removeDockerNetwork(not found) = %v, want nil", err)
	}
}

// writeCleanupTask writes a minimal TASK file with the given status and
// blocked_at and returns its path.
func writeCleanupTask(t *testing.T, status, blockedAt string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "TASK-066-x.md")
	content := "---\nid: \"066\"\ntitle: T\nstatus: " + status + "\nblocked_at: \"" + blockedAt + "\"\n---\n# TASK-066\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// newCleanupSpy installs counting k3d delete spies and returns the runner
// plus a reset function.
func newCleanupSpy(t *testing.T, cleanup *config.EnvCleanupConfig) (*Runner, *int, func()) {
	t.Helper()
	deletes := 0
	listK3dRegistries = func() ([]string, error) { return []string{"k3d-release-manager-registry"}, nil }
	listK3dClusters = func() ([]string, error) { return []string{"dev-customer-a-cache"}, nil }
	deleteK3dRegistry = func(string) error { deletes++; return nil }
	deleteK3dCluster = func(string) error { deletes++; return nil }
	removeDockerNetwork = func(string) error { deletes++; return nil }
	reset := func() {
		listK3dRegistries = func() ([]string, error) { return nil, nil }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		deleteK3dRegistry = func(string) error { return nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	}
	t.Cleanup(reset)
	return newEnvCleanupTestRunner(t, cleanup), &deletes, reset
}

func TestCleanupBlockedEnvDeletesK3dOnBlock(t *testing.T) {
	r, deletes, _ := newCleanupSpy(t, &config.EnvCleanupConfig{OnMerge: true, OnBlock: true, Exclude: []string{"kb-reranker", "ollama-sycl"}})
	path := writeCleanupTask(t, "blocked", "2026-08-28T10:00:00+08:00")

	r.cleanupBlockedEnv(path, "066", "T")

	// registry + cluster + network fallback, all touched.
	if *deletes != 3 {
		t.Fatalf("deletes = %d, want 3 (registry, cluster, network)", *deletes)
	}
}

func TestCleanupBlockedEnvRunsOncePerBlockedEpisode(t *testing.T) {
	r, deletes, _ := newCleanupSpy(t, &config.EnvCleanupConfig{OnMerge: true, OnBlock: true, Exclude: []string{"kb-reranker", "ollama-sycl"}})
	path := writeCleanupTask(t, "blocked", "2026-08-28T10:00:00+08:00")

	r.cleanupBlockedEnv(path, "066", "T")
	r.cleanupBlockedEnv(path, "066", "T")

	if *deletes != 3 {
		t.Fatalf("deletes = %d, want 3 (second call on same blocked episode must be a no-op)", *deletes)
	}
}

func TestCleanupBlockedEnvRerunsOnNewBlockedEpisode(t *testing.T) {
	r, deletes, _ := newCleanupSpy(t, &config.EnvCleanupConfig{OnMerge: true, OnBlock: true, Exclude: []string{"kb-reranker", "ollama-sycl"}})
	path := writeCleanupTask(t, "blocked", "2026-08-28T10:00:00+08:00")

	r.cleanupBlockedEnv(path, "066", "T")
	// Task resumed implementing and blocked again with a new blocked_at on
	// the SAME task file — a new episode must re-run teardown.
	if err := os.WriteFile(path, []byte("---\nid: \"066\"\ntitle: T\nstatus: blocked\nblocked_at: \"2026-08-28T11:00:00+08:00\"\n---\n# TASK-066\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.cleanupBlockedEnv(path, "066", "T")

	if *deletes != 6 {
		t.Fatalf("deletes = %d, want 6 (new blocked episode on same file must re-run teardown)", *deletes)
	}
}

func TestCleanupBlockedEnvCoversNeedsGrillingAndClosed(t *testing.T) {
	for _, status := range []string{"needs-grilling", "closed"} {
		r, deletes, _ := newCleanupSpy(t, &config.EnvCleanupConfig{OnMerge: true, OnBlock: true, Exclude: []string{"kb-reranker", "ollama-sycl"}})
		path := writeCleanupTask(t, status, "")

		r.cleanupBlockedEnv(path, "066", "T")
		if *deletes != 3 {
			t.Fatalf("status %s: deletes = %d, want 3", status, *deletes)
		}
	}
}

func TestCleanupBlockedEnvDisabledSkipsEverything(t *testing.T) {
	r, deletes, _ := newCleanupSpy(t, &config.EnvCleanupConfig{OnMerge: true, OnBlock: false, Exclude: []string{"kb-reranker", "ollama-sycl"}})
	path := writeCleanupTask(t, "blocked", "2026-08-28T10:00:00+08:00")

	r.cleanupBlockedEnv(path, "066", "T")
	if *deletes != 0 {
		t.Fatalf("deletes = %d, want 0 (OnBlock=false must skip teardown)", *deletes)
	}
}

// TestProcessBatchSequentialCleansK3dOnBlockedTask wires cleanupBlockedEnv
// into the real dispatch loop: a blocked task entering the batch must trigger
// the k3d teardown (TASK-066: k3d containers left after a requirement-driven
// block).
func TestProcessBatchSequentialCleansK3dOnBlockedTask(t *testing.T) {
	deletes := 0
	listK3dRegistries = func() ([]string, error) { return []string{"k3d-release-manager-registry"}, nil }
	listK3dClusters = func() ([]string, error) { return []string{"dev-customer-a-cache"}, nil }
	deleteK3dRegistry = func(string) error { deletes++; return nil }
	deleteK3dCluster = func(string) error { deletes++; return nil }
	removeDockerNetwork = func(string) error { deletes++; return nil }
	t.Cleanup(func() {
		listK3dRegistries = func() ([]string, error) { return nil, nil }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		deleteK3dRegistry = func(string) error { return nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-066-blocked.md")
	content := "---\nid: \"066\"\ntitle: T\nproject: test\nassignee: default\nstatus: blocked\npending_req: true\nblocked_at: \"2026-08-28T10:00:00+08:00\"\n---\n# TASK-066\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults() // EnvCleanup.OnBlock defaults true
	cfg.Notifications.Desktop = false
	r := New(cfg)
	r.logger = log.New(io.Discard, "", 0)

	tasks, err := task.FindReadyTaskForFile("", path)
	if err != nil || tasks == nil {
		t.Fatalf("find ready task: err=%v tasks=%v", err, tasks)
	}
	r.processBatchSequential([]task.ReadyTask{*tasks}, dir)

	if deletes != 3 {
		t.Fatalf("deletes = %d, want 3 (blocked task must trigger k3d teardown in the dispatch loop)", deletes)
	}
}

// TestCleanupDeadEndTaskEnvsCoversBlockedNeedsGrillingClosed verifies the
// every-scan sweep reaches the dead-end states that task.IsReady filters out
// of the dispatch batch (blocked-with-phase-failure and closed) — the
// TASK-066 blocked/pending_req gap.
func TestCleanupDeadEndTaskEnvsCoversBlockedNeedsGrillingClosed(t *testing.T) {
	deletes := 0
	listK3dRegistries = func() ([]string, error) { return []string{"k3d-release-manager-registry"}, nil }
	listK3dClusters = func() ([]string, error) { return []string{"dev-customer-a-cache"}, nil }
	deleteK3dRegistry = func(string) error { deletes++; return nil }
	deleteK3dCluster = func(string) error { deletes++; return nil }
	removeDockerNetwork = func(string) error { deletes++; return nil }
	t.Cleanup(func() {
		listK3dRegistries = func() ([]string, error) { return nil, nil }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		deleteK3dRegistry = func(string) error { return nil }
		deleteK3dCluster = func(string) error { return nil }
		removeDockerNetwork = func(string) error { return nil }
	})

	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-release-manager", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, status string) {
		content := "---\nid: \"066\"\ntitle: T\nstatus: " + status + "\nblocked_at: \"2026-08-28T10:00:00+08:00\"\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("TASK-066-blocked.md", "blocked")
	write("TASK-066-grill.md", "needs-grilling")
	write("TASK-066-closed.md", "closed")

	cfg := config.Defaults() // EnvCleanup.OnBlock defaults true
	cfg.Notifications.Desktop = false
	cfg.ObsidianVault = vault
	r := New(cfg)
	r.logger = log.New(io.Discard, "", 0)

	r.cleanupDeadEndTaskEnvs()

	// 3 dead-end tasks; the episode dedup is per task path, so all three
	// trigger teardown (registry + cluster + network each = 9 deletes).
	if deletes != 9 {
		t.Fatalf("deletes = %d, want 9 (3 dead-end tasks x registry+cluster+network)", deletes)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
