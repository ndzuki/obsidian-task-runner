package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"syscall"
	"time"
)

// startAgentServer 拉起长驻 agent-server（`dsh --profile headless-agent-server`）
// 并等待健康检查通过。executor != "dsh-embed" 时 no-op。子进程日志写入 daemon
// 的 logWriter；生命周期由 stopAgentServer 收口。
func (r *Runner) startAgentServer(ctx context.Context) error {
	if r.cfg.Executor != "dsh-embed" {
		return nil
	}
	cmd := exec.CommandContext(ctx, r.cfg.DSHCmd, "--profile", "headless-agent-server")
	cmd.Stdout = r.logWriter
	cmd.Stderr = r.logWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent-server: %w", err)
	}
	r.agentServerCmd = cmd
	r.logger.Printf("agent-server starting (pid=%d, addr=%s)", cmd.Process.Pid, r.cfg.AgentServerAddr)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + r.cfg.AgentServerAddr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				r.logger.Printf("agent-server healthy (%s)", r.cfg.AgentServerAddr)
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
func (r *Runner) stopAgentServer() {
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
