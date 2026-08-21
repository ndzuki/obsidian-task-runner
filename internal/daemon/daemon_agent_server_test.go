package daemon

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

func TestStartAgentServerExternalManagedSkipsSpawn(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()

	cfg := config.Defaults()
	cfg.Executor = "dsh-embed"
	cfg.AgentServerManaged = false
	cfg.AgentServerAddr = health.Listener.Addr().String()

	r := &Runner{
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
	}

	if err := r.startAgentServer(context.Background()); err != nil {
		t.Fatalf("startAgentServer with external agent-server: %v", err)
	}
	if r.agentServerCmd != nil {
		t.Fatalf("external agent-server must not be spawned by daemon, got pid %d", r.agentServerCmd.Process.Pid)
	}

	// stopAgentServer must be a no-op for externally managed servers.
	r.stopAgentServer()
	if r.agentServerCmd != nil {
		t.Fatalf("stopAgentServer must not retain an external agent-server command")
	}
}
