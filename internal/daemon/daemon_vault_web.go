package daemon

import (
	"context"
	"net/http"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/vaultweb"
)

// startVaultWeb 在 daemon 进程内启动只读 vault 看板 HTTP API（Phase 4），供
// DSH web 的 vault-dashboard 插件使用。cfg.VaultWebAddr 为空或 vault 未配置时
// no-op；生命周期由 stopVaultWeb 收口（与 agent-server 同属 daemon 托管）。
func (r *Runner) startVaultWeb() error {
	addr := r.cfg.VaultWebAddr
	if addr == "" || r.cfg.ObsidianVault == "" {
		return nil
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           vaultweb.NewWithAgentServer(r.cfg.ObsidianVault, r.cfg.AgentServerAddr).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	r.vaultWebServer = srv
	go func() {
		r.logger.Printf("vault dashboard API listening on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			r.logger.Printf("vault web serve: %v", err)
		}
	}()
	return nil
}

// stopVaultWeb 优雅关闭内嵌 vault 看板服务。
func (r *Runner) stopVaultWeb() {
	if r.vaultWebServer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.vaultWebServer.Shutdown(ctx); err != nil {
		r.logger.Printf("vault web shutdown: %v", err)
	}
	r.vaultWebServer = nil
}
