package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

// writeMapFile 在 skillDir/config 下写入自定义 vault-map.json（模拟用户配置）。
func writeMapFile(t *testing.T, skillDir string, content map[string]interface{}) string {
	t.Helper()
	configDir := filepath.Join(skillDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	mapFile := filepath.Join(configDir, "vault-map.json")
	data, _ := json.MarshalIndent(withDesktopNotificationsDisabled(content), "", "  ")
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	return mapFile
}

// TestVaultMapConfigReachesOMPArgs 模拟真实启动链路：vault-map.json →
// config.Load → Runner 派发 → OMP argv。验证：
//  1. 用户配置的 models 原样到达 --model（用户值覆盖默认值）
//  2. 未知 assignee 回退到 default 模型
//  3. --thinking 独立按阶段指定，模型标识不带推理强度后缀
func TestVaultMapConfigReachesOMPArgs(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skill")
	mapFile := writeMapFile(t, skillDir, map[string]interface{}{
		"models": map[string]string{
			"default": "gateway/gpt-5.4-mini",
			"gpt":     "gateway/gpt-5.6-sol",
			"sonnet":  "anthropic/claude-sonnet-4-20250514", // 自定义 key 应生效
		},
		"fallback_models": map[string]string{"gpt": "deepseek/deepseek-v4-flash"},
	})

	// ── 第 1 层：配置解析（真实 Load 链路：Defaults → merge → validate）──
	cfg, err := config.Load(mapFile)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := cfg.Model("default"); got != "gateway/gpt-5.4-mini" {
		t.Fatalf("Model(default) = %q, want gateway/gpt-5.4-mini", got)
	}
	if got := cfg.Model("sonnet"); got != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("Model(sonnet) = %q, want 自定义 key 生效", got)
	}
	if got := cfg.Model("unknown"); got != "gateway/gpt-5.4-mini" {
		t.Fatalf("Model(unknown) = %q, want 回退 default", got)
	}
	if got := cfg.FallbackModelFor("gpt"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("FallbackModelFor(gpt) = %q, want deepseek/deepseek-v4-flash", got)
	}

	// ── 第 2 层：行为链路（Runner 用加载的 cfg 派发，fake OMP 捕获 argv）──
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	argsDir := filepath.Join(dir, "args")
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)
	t.Setenv("ARGS_DIR", argsDir)

	cfg.OMPCmd = omp
	cfg.SkillInstallDir = skillDir
	cfg.LogDir = filepath.Join(dir, "logs")
	runner := New(cfg)
	runner.logger = log.New(io.Discard, "", 0)

	taskPath := writeTaskFile(t, dir, "TASK-000.md", "ready")
	done := runBatch(runner, []task.ReadyTask{{
		ID: "000", Title: "Ready task", FilePath: taskPath, Status: "ready", Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1)
	waitForArgsFile(t, argsDir)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	entries, err := os.ReadDir(argsDir)
	if err != nil {
		t.Fatalf("read OMP args dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("OMP invocation count = %d, want 1", len(entries))
	}
	args, err := os.ReadFile(filepath.Join(argsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read OMP args: %v", err)
	}
	argStr := string(args)

	// 用户配置的模型标识原样到达 --model
	if !strings.Contains(argStr, "--model gateway/gpt-5.4-mini") {
		t.Fatalf("OMP args = %q, want --model gateway/gpt-5.4-mini", argStr)
	}
	// 推理强度由 --thinking 独立指定（refining 阶段 = low）
	if !strings.Contains(argStr, "--thinking low") {
		t.Fatalf("OMP args = %q, want --thinking low", argStr)
	}
	// 模型标识不得携带推理强度后缀——档位由 --thinking 独立指定。
	// 覆盖全部档位枚举（off/low/high/max），只检查 --model 参数值本身，
	// 避免误伤 prompt/路径中的子串。
	fields := strings.Fields(argStr)
	for i, f := range fields {
		if f == "--model" && i+1 < len(fields) {
			model := fields[i+1]
			for _, suffix := range []string{":off", ":low", ":high", ":max"} {
				if strings.HasSuffix(model, suffix) {
					t.Fatalf("--model %q 不得带推理强度后缀 %q", model, suffix)
				}
			}
			break
		}
	}
	// prompt 正确路由到 refining
	if !strings.Contains(argStr, "/obsidian-task-runner-refining "+taskPath) {
		t.Fatalf("OMP args = %q, want refining prompt", argStr)
	}
}

// TestVaultMapFallbackModelUsedOnRetry 模拟主模型失败后的兜底链路：
// vault-map.json 的 fallback_models 值必须成为重试时的 --model。
// fake OMP 第一次调用 exit 1（主模型失败），第二次成功并捕获 argv。
func TestVaultMapFallbackModelUsedOnRetry(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skill")
	mapFile := writeMapFile(t, skillDir, map[string]interface{}{
		"models": map[string]string{
			"default": "gateway/gpt-5.4-mini",
		},
		"fallback_models": map[string]string{
			"default": "deepseek/deepseek-v4-flash",
		},
	})
	cfg, err := config.Load(mapFile)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	// 失败一次后成功的 fake OMP：argv 落到 ARGS_DIR/attempt-<n>
	argsDir := filepath.Join(dir, "args")
	countFile := filepath.Join(dir, "count")
	omp := filepath.Join(dir, "fake-omp-fail")
	script := `#!/bin/bash
if [ ! -f "` + countFile + `" ]; then printf '0\n' > "` + countFile + `"; fi
n=$(cat "` + countFile + `")
printf '%d\n' "$((n+1))" > "` + countFile + `"

if [ "$n" -eq 0 ]; then exit 1; fi
mkdir -p "` + argsDir + `"
printf '%s\n' "$*" > "` + argsDir + `/attempt-$n"
exit 0
`
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake omp: %v", err)
	}

	cfg.OMPCmd = omp
	cfg.SkillInstallDir = skillDir
	cfg.LogDir = filepath.Join(dir, "logs")
	runner := New(cfg)
	runner.logger = log.New(io.Discard, "", 0)

	taskPath := writeTaskFile(t, dir, "TASK-001.md", "ready")
	done := runBatch(runner, []task.ReadyTask{{
		ID: "001", Title: "Fallback task", FilePath: taskPath, Status: "ready", Assignee: "default",
	}})
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	// processBatch 是纯 dispatch：主调用失败与 fallback 重试都在后台 runTask
	// 执行，轮询等待第二次调用把 argv 写入 argsDir。
	deadline := time.Now().Add(5 * time.Second)
	var entries []os.DirEntry
	for time.Now().Before(deadline) {
		entries, err = os.ReadDir(argsDir)
		if err == nil && len(entries) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || len(entries) != 1 {
		t.Fatalf("fallback invocation args = %v entries, want 1 (err=%v)", len(entries), err)
	}
	args, err := os.ReadFile(filepath.Join(argsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read fallback args: %v", err)
	}
	argStr := string(args)
	if !strings.Contains(argStr, "--model deepseek/deepseek-v4-flash") {
		t.Fatalf("fallback args = %q, want --model deepseek/deepseek-v4-flash（vault-map.json fallback_models 值）", argStr)
	}
	if !strings.Contains(argStr, "--thinking low") {
		t.Fatalf("fallback args = %q, want 保留 --thinking low", argStr)
	}
}
