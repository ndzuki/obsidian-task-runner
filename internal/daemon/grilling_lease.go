package daemon

import (
	"fmt"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func AcquireGrillLease(path, owner string, now time.Time) (bool, error) {
	acquired := false
	err := yamlfrontmatter.WithLockedFrontmatter(path, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		if fm.GrillOwner != "" && fm.GrillOwner != owner && !grillLeaseExpired(fm, now) {
			return nil, nil
		}
		updates := map[string]interface{}{
			"grill_owner":        owner,
			"grill_heartbeat_at": now.Format(time.RFC3339),
		}
		if fm.GrillOwner == "" || fm.GrillOwner != owner {
			updates["grill_started_at"] = now.Format(time.RFC3339)
		}
		acquired = true
		return updates, nil
	})
	return acquired, err
}

func RefreshGrillLease(path, owner string, now time.Time) error {
	return yamlfrontmatter.WithLockedFrontmatter(path, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		if fm.GrillOwner != owner {
			return nil, fmt.Errorf("grill lease owned by %q", fm.GrillOwner)
		}
		return map[string]interface{}{"grill_heartbeat_at": now.Format(time.RFC3339)}, nil
	})
}

func PreemptExpiredGrillLease(path string, now time.Time) (bool, error) {
	preempted := false
	err := yamlfrontmatter.WithLockedFrontmatter(path, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		if fm.GrillOwner == "" || !grillLeaseExpired(fm, now) {
			return nil, nil
		}
		preempted = true
		return map[string]interface{}{
			"grill_owner":        "",
			"grill_started_at":   "",
			"grill_heartbeat_at": "",
		}, nil
	})
	return preempted, err
}

func grillLeaseExpired(fm *yamlfrontmatter.Frontmatter, now time.Time) bool {
	if fm == nil || fm.GrillOwner == "" {
		return false
	}
	heartbeat := fm.GrillHeartbeatAt
	if heartbeat == "" {
		heartbeat = fm.GrillStartedAt
	}
	lastActive, err := time.Parse(time.RFC3339, heartbeat)
	if err != nil {
		return true
	}
	timeout := time.Duration(fm.GrillTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return now.Sub(lastActive) >= timeout
}
