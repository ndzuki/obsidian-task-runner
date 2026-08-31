package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
)

// agentServerEnv returns the environment for the managed agent-server
// subprocess. It carries the global shared knowledge-base root so general
// interactive sessions (/agent/chat) can do a KB-first precompute
// (`otg kb search --json`) even when the session is not tied to a project,
// plus the vault that holds the registered projects (Projects/) so a
// workspace-scoped session can also resolve and consult that project's own
// Notes/CONTEXT.md, Notes/adr/ and PROJECT-CONVENTIONS.md instead of
// reasoning from scratch. OTR_KB_VAULT falls back to the daemon's
// ObsidianVault; OTR_OTG_PATH pins the daemon's own otg binary for the
// precompute subprocess (systemd PATH may not include it). Empty KB vault on
// both sides disables the injection.
func (r *Runner) agentServerEnv() []string {
	kbVault := r.cfg.KBVault
	if kbVault == "" {
		kbVault = r.cfg.ObsidianVault
	}
	otgPath := ""
	if exe, err := os.Executable(); err == nil {
		otgPath = exe
	}
	return append(os.Environ(),
		"OTR_KB_VAULT="+kbVault,
		"OTR_KB_DB="+knowledge.KBPath(r.cfg.ObsidianVault, r.cfg.KBDb),
		"OTR_PROJECT_VAULT="+r.cfg.ObsidianVault,
		"OTR_MAP_FILE="+r.cfg.ConfigPath,
		"OTR_OTG_PATH="+otgPath,
	)
}

// startAgentServer 拉起长驻 agent-server（`dsh --profile headless-agent-server`）
// 并等待健康检查通过。executor != "dsh-embed" 时 no-op。子进程日志写入 daemon
// 的 logWriter；生命周期由 stopAgentServer 收口。
// 当 AgentServerManaged=false 时，不拉起子进程，只等待外部 systemd 管理的
// agent-server 健康检查通过。
func (r *Runner) startAgentServer(ctx context.Context) error {
	if r.cfg.Executor != "dsh-embed" {
		return nil
	}
	if r.cfg.AgentServerManaged {
		cmd := exec.CommandContext(ctx, r.cfg.DSHCmd, "--profile", "headless-agent-server")
		cmd.Stdout = r.logWriter
		cmd.Stderr = r.logWriter
		cmd.Env = r.agentServerEnv()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start agent-server: %w", err)
		}
		r.agentServerCmd = cmd
		r.logger.Printf("agent-server starting (pid=%d, addr=%s)", cmd.Process.Pid, r.cfg.AgentServerAddr)
	} else {
		r.logger.Printf("agent-server managed externally; waiting for health at %s", r.cfg.AgentServerAddr)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + r.cfg.AgentServerAddr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				r.logger.Printf("agent-server healthy (%s)", r.cfg.AgentServerAddr)
				// 防御：健康检查通过后，确认响应 8799 的是我们自管的子进程，
				// 而非外部残留（如 systemd dsh-agent-server）占着端口。自管子
				// 进程若因 bind 失败已退出（如 8799 被占），Signal(0) 探测失败
				// ——此时 "healthy" 其实是外部进程在服务，必须告警（2026-08-31：
				// systemd 占 8799、daemon 自管子进程 bind 失败死亡，081 审计发到
				// 卡死的外部实例而冻结、任务永久 skipping）。
				if r.cfg.AgentServerManaged && r.agentServerCmd != nil && r.agentServerCmd.Process != nil {
					if err := r.agentServerCmd.Process.Signal(syscall.Signal(0)); err != nil {
						r.logger.Printf("⚠ agent-server 自管子进程(pid=%d)已退出：8799 可能被外部进程占用，实际服务该端口的并非 daemon 自管实例", r.agentServerCmd.Process.Pid)
					}
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent-server health check cancelled: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("agent-server health check timeout after 30s")
}

// stopAgentServer 优雅关闭 agent-server（SIGTERM → 10s 宽限 → SIGKILL）。
// AgentServerManaged=false 时不关闭外部服务，systemd 负责其生命周期。
func (r *Runner) stopAgentServer() {
	if !r.cfg.AgentServerManaged {
		r.logger.Printf("agent-server managed externally; skipping stop")
		return
	}
	if r.agentServerCmd == nil || r.agentServerCmd.Process == nil {
		return
	}
	r.logger.Printf("stopping agent-server (pid=%d)", r.agentServerCmd.Process.Pid)
	_ = r.agentServerCmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = r.agentServerCmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = r.agentServerCmd.Process.Kill()
	}
	r.agentServerCmd = nil
}
