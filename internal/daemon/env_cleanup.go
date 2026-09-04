package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ---------------------------------------------------------------------------
// Daemon-side environment teardown for the merge/terminal phase AND for
// dead-end states where a task stops implementing without merging.
//
// Background (2026-08-28 TASK-065): the implementing session's smoke test
// built 5 k3d clusters + 1 k3d registry and left them running after merge.
// The completion audit saw the leftovers, classified them as "in-flight
// audit/merge residual" (not implementation residual), and passed — so the
// merged task kept its disposable staging environment alive forever. The
// audit gate can *report* residuals but cannot delete them (read-only
// session), and cleanupTaskArtifacts only removes files/worktrees.
//
// Background (2026-08-28 TASK-066): an implementing session can also be cut
// short by a requirement-driven block (status=blocked / needs-grilling /
// closed) before any merge — the k3d clusters it created then keep running
// indefinitely because the merge-only teardown never fires. So the teardown
// also runs on those dead-end transitions (EnvCleanup.OnBlock).
//
// The operation is bounded by EnvCleanup.Exclude (user red line: never touch
// anything excluded) and EnvCleanup.DryRun
// (audit-only mode).
// ---------------------------------------------------------------------------

// listK3dRegistries returns the names of every k3d registry. Overridable in
// tests (no real k3d needed). `k3d registry list -o json` emits the same
// shape as `k3d cluster list -o json`, so the cluster-name parser is reused.
var listK3dRegistries = func() ([]string, error) {
	out, err := exec.Command("k3d", "registry", "list", "-o", "json").Output()
	if err != nil {
		return nil, err
	}
	return parseK3dClusterNames(out)
}

// deleteK3dCluster deletes a k3d cluster (containers + network + kubeconfig
// entries). Overridable in tests.
var deleteK3dCluster = func(name string) error {
	return exec.Command("k3d", "cluster", "delete", name).Run()
}

// deleteK3dRegistry deletes a k3d registry and disconnects it from every
// cluster network it joined. Overridable in tests.
var deleteK3dRegistry = func(name string) error {
	return exec.Command("k3d", "registry", "delete", name).Run()
}

// removeDockerNetwork force-removes a docker network. "not found" is treated
// as success — the network was already gone, which is the desired end state.
// Overridable in tests.
var removeDockerNetwork = func(name string) error {
	out, err := exec.Command("docker", "network", "rm", name).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "not found") || strings.Contains(msg, "No such network") {
			return nil
		}
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}

// cleanupMergeEnv tears down disposable k3d clusters, registries, and their
// leftover docker networks after a task merges (gated on EnvCleanup.OnMerge).
func (r *Runner) cleanupMergeEnv(taskID, taskTitle string) {
	if r.cfg.EnvCleanup == nil || !r.cfg.EnvCleanup.OnMerge {
		return
	}
	r.cleanupTaskK3dEnv(taskID, taskTitle)
}

// blockedEnvEpisode returns a stable signature for a task's current
// dead-end state (blocked / needs-grilling / closed). It is used to run
// env teardown at most once per episode instead of once per scan.
func blockedEnvEpisode(fm *yamlfrontmatter.Frontmatter) string {
	switch fm.Status {
	case "blocked":
		// blocked_at changes on every re-block (daemon-maintained), so a
		// task that blocks → resumes → blocks again re-runs teardown. Legacy
		// tasks without blocked_at fall back to the empty signature (cleaned
		// once per daemon run).
		return "blocked:" + fm.BlockedAt
	case "needs-grilling":
		return "needs-grilling:" + fm.GrillOwner + ":" + fmt.Sprintf("%t", fm.GrillParked)
	case "closed":
		return "closed"
	default:
		return ""
	}
}

// cleanupBlockedEnv tears down disposable k3d clusters, registries, and
// networks when a task stops implementing without merging — blocked by a
// phase failure, blocked by a requirement change / pending_req replan, held
// in needs-grilling, or closed (gated on EnvCleanup.OnBlock). Debounced per
// blocked episode via envCleanupSeen so a long-lived blocked task does not
// re-enumerate k3d on every scan.
func (r *Runner) cleanupBlockedEnv(taskPath, taskID, taskTitle string) {
	if r.cfg.EnvCleanup == nil || !r.cfg.EnvCleanup.OnBlock {
		return
	}
	fm, err := readFrontmatter(taskPath)
	if err != nil || fm == nil {
		return
	}
	episode := blockedEnvEpisode(fm)
	if episode == "" {
		return
	}
	if prev, ok := r.envCleanupSeen.Load(taskPath); ok && prev == episode {
		return
	}
	r.cleanupTaskK3dEnv(taskID, taskTitle)
	r.envCleanupSeen.Store(taskPath, episode)
}

// cleanupDeadEndTaskEnvs is the every-scan safety net for env teardown on
// dead-end tasks. blocked / needs-grilling / closed tasks are not all part
// of the ready batch (task.IsReady filters out blocked-with-phase-failure and
// closed), so the dispatch-loop hooks alone would never reach them — e.g. a
// task blocked by a requirement change (pending_req=true, TASK-066) is routed
// to refining by recoverBlockedPendingReq without ever passing through
// processBatchSequential. This sweep enumerates every task file each scan and
// lets cleanupBlockedEnv run once per blocked episode.
func (r *Runner) cleanupDeadEndTaskEnvs() {
	if r.cfg.EnvCleanup == nil || !r.cfg.EnvCleanup.OnBlock {
		return
	}
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			taskPath := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(taskPath)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				continue
			}
			switch fm.Status {
			case "blocked", "needs-grilling", "closed":
				r.cleanupBlockedEnv(taskPath, fm.ID, fm.Title)
			}
		}
	}
}

// cleanupTaskK3dEnv deletes disposable k3d clusters, registries, and their
// leftover docker networks. Order matters: registries are deleted first so
// they detach from cluster networks, letting `k3d cluster delete` (and the
// network fallback) remove those networks.
func (r *Runner) cleanupTaskK3dEnv(taskID, taskTitle string) {
	excludes := append([]string{}, memoryGateExcludeDefault...)
	excludes = append(excludes, r.cfg.EnvCleanup.Exclude...)
	dryRun := r.cfg.EnvCleanup.DryRun

	var deletedRegistries, deletedClusters, deletedNetworks []string

	// 1) Registries first: detach registry endpoints from cluster networks.
	if registries, err := listK3dRegistries(); err == nil {
		for _, name := range registries {
			if excludedCluster(name, excludes) {
				continue
			}
			if dryRun {
				deletedRegistries = append(deletedRegistries, name)
				continue
			}
			if err := deleteK3dRegistry(name); err != nil {
				r.logger.Printf("task %s: env cleanup: delete registry %s: %v", taskID, name, err)
				continue
			}
			deletedRegistries = append(deletedRegistries, name)
			r.logger.Printf("task %s: env cleanup: deleted k3d registry %s", taskID, name)
		}
	} else {
		r.logger.Printf("task %s: env cleanup: list k3d registries: %v", taskID, err)
	}

	// 2) Clusters, then 3) a docker-network fallback per cluster. Even after
	// registries detach, `k3d cluster delete` can leave the bridge network
	// behind (k3d 5.x warns "network has active endpoints"); the fallback
	// removes the leftover deterministically.
	if clusters, err := listK3dClusters(); err == nil {
		sort.Strings(clusters)
		for _, name := range clusters {
			if excludedCluster(name, excludes) {
				continue
			}
			net := "k3d-" + name
			if dryRun {
				deletedClusters = append(deletedClusters, name)
				deletedNetworks = append(deletedNetworks, net)
				continue
			}
			if err := deleteK3dCluster(name); err != nil {
				r.logger.Printf("task %s: env cleanup: delete cluster %s: %v", taskID, name, err)
			} else {
				deletedClusters = append(deletedClusters, name)
				r.logger.Printf("task %s: env cleanup: deleted k3d cluster %s", taskID, name)
			}
			if err := removeDockerNetwork(net); err != nil {
				r.logger.Printf("task %s: env cleanup: remove network %s: %v", taskID, net, err)
			} else {
				deletedNetworks = append(deletedNetworks, net)
			}
		}
	} else {
		r.logger.Printf("task %s: env cleanup: list k3d clusters: %v", taskID, err)
	}

	if len(deletedRegistries)+len(deletedClusters)+len(deletedNetworks) == 0 {
		return
	}
	verb := "已清理"
	if dryRun {
		verb = "将清理"
	}
	desc := fmt.Sprintf(
		"%s 任务自建冒烟环境（k3d 集群 %d / registry %d / 网络 %d）：%s",
		verb, len(deletedClusters), len(deletedRegistries), len(deletedNetworks),
		strings.Join(append(append(append([]string{}, deletedClusters...), deletedRegistries...), deletedNetworks...), ", "))
	notify.SendTaskAction(taskID, taskTitle, "🧹", "环境收尾", desc, r.cfg.Notifications.Desktop)
}
