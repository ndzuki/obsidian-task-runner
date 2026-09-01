package daemon

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestAgentServerEnvCarriesKbVault guards the KB-first wiring: the managed
// agent-server subprocess must receive the global shared knowledge-base root
// (config KBVault, falling back to ObsidianVault) plus the kb store path via
// env, so /agent/chat can consult References/INDEX even for non-vault
// interactive sessions.
func TestAgentServerEnvCarriesKbVault(t *testing.T) {
	cfg := config.Defaults()
	cfg.KBVault = "/kb/shared"
	cfg.ObsidianVault = "/vault/main"
	cfg.KBDb = "/custom/kb.sqlite"
	cfg.ConfigPath = "/custom/vault-map.json"

	r := &Runner{cfg: cfg}
	env := r.agentServerEnv()

	get := func(key string) string {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, key+"="); ok {
				return v
			}
		}
		return ""
	}
	if got := get("OTR_KB_VAULT"); got != "/kb/shared" {
		t.Fatalf("OTR_KB_VAULT = %q, want %q (KBVault wins over ObsidianVault)", got, "/kb/shared")
	}
	if got := get("OTR_KB_DB"); got != "/custom/kb.sqlite" {
		t.Fatalf("OTR_KB_DB = %q, want %q", got, "/custom/kb.sqlite")
	}
	if got := get("OTR_PROJECT_VAULT"); got != "/vault/main" {
		t.Fatalf("OTR_PROJECT_VAULT = %q, want %q (project workspace vault must be the ObsidianVault)", got, "/vault/main")
	}
	if got := get("OTR_MAP_FILE"); got != "/custom/vault-map.json" {
		t.Fatalf("OTR_MAP_FILE = %q, want %q (daemon's resolved map path so otg reads kb_embedding)", got, "/custom/vault-map.json")
	}
	if got := get("OTR_OTG_PATH"); got == "" {
		t.Fatal("OTR_OTG_PATH must pin the daemon's own otg binary for the precompute subprocess")
	}
	if got := get("OTR_KB_HTTP"); got != "http://"+cfg.VaultWebAddr {
		t.Fatalf("OTR_KB_HTTP = %q, want %q (B2 in-process search endpoint)", got, "http://"+cfg.VaultWebAddr)
	}
}

func TestKBHTTPBaseEmptyWhenVaultWebDisabled(t *testing.T) {
	if got := kbHTTPBase(""); got != "" {
		t.Fatalf("kbHTTPBase(\"\") = %q, want \"\" (spawn-only fallback)", got)
	}
	if got := kbHTTPBase("127.0.0.1:8787"); got != "http://127.0.0.1:8787" {
		t.Fatalf("kbHTTPBase = %q, want http://127.0.0.1:8787", got)
	}
}

func TestAgentServerEnvFallsBackToObsidianVault(t *testing.T) {
	cfg := config.Defaults()
	cfg.KBVault = ""
	cfg.ObsidianVault = "/vault/main"

	r := &Runner{cfg: cfg}
	env := r.agentServerEnv()

	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "OTR_KB_VAULT="); ok {
			if v != "/vault/main" {
				t.Fatalf("OTR_KB_VAULT = %q, want %q (fallback to ObsidianVault)", v, "/vault/main")
			}
			return
		}
	}
	t.Fatal("OTR_KB_VAULT not present in agent-server env")
}

// TestAgentServerEnvPreservesProcessEnv guards that the managed subprocess
// still inherits the daemon's own environment (PATH etc.) alongside the KB
// overrides.
func TestAgentServerEnvPreservesProcessEnv(t *testing.T) {
	t.Setenv("OTR_TEST_KEEP_ME", "kept")
	cfg := config.Defaults()
	cfg.KBVault = "/kb"
	r := &Runner{cfg: cfg}

	env := r.agentServerEnv()
	if !strings.Contains(strings.Join(env, "\n"), "OTR_TEST_KEEP_ME=kept") {
		t.Fatal("agent-server env must inherit the daemon process environment")
	}
	if !strings.Contains(strings.Join(env, "\n"), "PATH="+os.Getenv("PATH")) {
		t.Fatal("agent-server env must keep PATH")
	}
}
