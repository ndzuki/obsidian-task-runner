.PHONY: build test test-node test-cover bench lint clean install install-force deploy deploy-status rollback sync-docs sync-plugins sync-registry

BINARY := otg
GRILL  := kitty-grill
GOBIN  := $(or $(shell go env GOBIN 2>/dev/null),$(HOME)/go/bin)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)"

build:
	go build -tags sqlite_fts5 $(LDFLAGS) -o $(BINARY) ./cmd/otg/
	go build -o $(GRILL) ./cmd/kitty-grill/

# test/test-cover/bench：加 GIT_TERMINAL_PROMPT=0（任何泄漏到网络 git 的调用
# 快速失败而非在终端卡死）与 -timeout 5m（单个包测试挂起时自行超时并打印
# 卡住的测试名 + goroutine dump，而不是让 make 无限等待）。-p 4 限制并行包数，
# 避免与运行中的 otg daemon / dsh-agent-server 抢资源导致机器卡顿。
# test 顺带跑 agent-server 的 node 单测（node 不可用则跳过，不阻塞 Go 测试）。
test:
	GIT_TERMINAL_PROMPT=0 go test -race -tags sqlite_fts5 -cover -p 4 -timeout 5m ./...
	@$(MAKE) test-node

# test-node: agent-server.mjs 的纯函数单测（KB-first 摘要/查询词推导/项目上下文）。
# node 不可用时静默跳过——agent-server 本身由 dsh 的 node 运行时驱动，测试只是辅助。
test-node:
	@if command -v node >/dev/null 2>&1; then \
		node deploy/dsh-plugins/agent-server.kb.test.mjs; \
	else \
		echo "  (node not found — skipping agent-server JS unit tests)"; \
	fi

test-cover:
	GIT_TERMINAL_PROMPT=0 go test -race -tags sqlite_fts5 -coverprofile=coverage.out -p 4 -timeout 5m ./...
	go tool cover -html=coverage.out -o coverage.html

bench:
	GIT_TERMINAL_PROMPT=0 go test -race -tags sqlite_fts5 -bench=. -benchmem -timeout 5m ./...

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY) $(GRILL) coverage.out coverage.html

# busy-safe install: 运行中的二进制（otg daemon / grilling tab 的
# kitty-grill）不允许原地覆盖（Text file busy），先 mv 到 .old 再 cp。
install: build sync-docs
	@echo "=== Installing $(BINARY) + $(GRILL) ==="
	mkdir -p $(HOME)/.local/bin $(GOBIN)
	@for b in $(BINARY) $(GRILL); do \
		-rm -f $(HOME)/.local/bin/$$b.old $(GOBIN)/$$b.old 2>/dev/null || true; \
		-mv $(HOME)/.local/bin/$$b $(HOME)/.local/bin/$$b.old 2>/dev/null || true; \
		cp $$b $(HOME)/.local/bin/$$b; \
		cp $$b $(GOBIN)/$$b; \
	done
	@echo "Installed to $(HOME)/.local/bin/$(BINARY) $(HOME)/.local/bin/$(GRILL)"

sync-docs:
	@echo "=== Syncing skill docs to ~/.dsh/skills/obsidian-task-runner/ ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		mkdir -p "$$SKILL_DIR"; \
		cp -r obsidian-task-runner/*.md "$$SKILL_DIR/"; \
		cp -r obsidian-task-runner/skills/ "$$SKILL_DIR/"
	@# workflow.md is the repo design doc; not shipped with the runtime skill
	@# package (runtime = SKILL.md + reference.md; full spec stays in repo).
	@# knowledge-base is an external skill; repo copy is the versioned source
	@# for rollback. Only the installed file is overwritten, never vault data.
	cp obsidian-task-runner/skills/knowledge-base/SKILL.md $(HOME)/.dsh/skills/knowledge-base/SKILL.md
	@# kulala-http is a self-authored general skill; repo copy is the versioned
	@# source for rollback, synced like knowledge-base.
	mkdir -p $(HOME)/.dsh/skills/kulala-http
	cp obsidian-task-runner/skills/kulala-http/SKILL.md $(HOME)/.dsh/skills/kulala-http/SKILL.md
	@for s in $$(grep -v '^#' obsidian-task-runner/skills/manifest | grep -v '^$$'); do \
		mkdir -p $(HOME)/.dsh/skills/obsidian-task-runner-$$s; \
		cp obsidian-task-runner/skills/$$s/SKILL.md $(HOME)/.dsh/skills/obsidian-task-runner-$$s/SKILL.md; \
	done
	@echo "=== Pruning stale managed docs/skills (repo 已删除的运行时文件) ==="
	@for d in $(HOME)/.dsh/skills/obsidian-task-runner/skills/*; do \
		[ -d "$$d" ] || continue; \
		b=$$(basename "$$d"); \
		[ -d "obsidian-task-runner/skills/$$b" ] || { echo "  prune stale skill: $$b"; rm -rf "$$d"; }; \
	done
	@for f in $(HOME)/.dsh/skills/obsidian-task-runner/*.md; do \
		[ -f "$$f" ] || continue; \
		b=$$(basename "$$f"); \
		[ -f "obsidian-task-runner/$$b" ] || { echo "  prune stale doc: $$b"; rm -f "$$f"; }; \
	done
	@for d in $(HOME)/.dsh/skills/obsidian-task-runner-*; do \
		[ -d "$$d" ] || continue; \
		b=$${d##*obsidian-task-runner-}; \
		grep -qx "$$b" obsidian-task-runner/skills/manifest 2>/dev/null || { echo "  prune stale phase-skill: $$b"; rm -rf "$$d"; }; \
	done
	@find $(HOME)/.dsh/skills/obsidian-task-runner* -name '*.old' -delete 2>/dev/null || true
	@echo "=== Done ==="

# sync-plugins: 把 deploy/dsh-plugins/ 下的 DSH 插件同步到 ~/.dsh/plugins/
#（dsh profile 按绝对路径加载）。busy-safe 替换；agent-server.mjs 变更需
# 重启 dsh-agent-server 才生效（install-force 末尾有提示）。同步后清理
# repo 已删除的残留插件（~/.dsh/plugins 是受管目录）。
sync-plugins:
	@echo "=== Syncing dsh plugins to ~/.dsh/plugins/ ==="
	mkdir -p $(HOME)/.dsh/plugins
	@for f in deploy/dsh-plugins/*; do \
		b=$$(basename $$f); \
		-rm -f $(HOME)/.dsh/plugins/$$b.old 2>/dev/null || true; \
		-mv $(HOME)/.dsh/plugins/$$b $(HOME)/.dsh/plugins/$$b.old 2>/dev/null || true; \
		cp $$f $(HOME)/.dsh/plugins/$$b; \
		chmod 600 $(HOME)/.dsh/plugins/$$b; \
	done
	@echo "=== Pruning stale dsh plugins (repo 已删除的残留) ==="
	@for f in $(HOME)/.dsh/plugins/*; do \
		[ -f "$$f" ] || continue; \
		b=$$(basename "$$f"); \
		case "$$b" in *.old) continue;; esac; \
		[ -f "deploy/dsh-plugins/$$b" ] || { echo "  prune stale plugin: $$b"; rm -f "$$f"; }; \
	done
	@echo "=== Done ==="

# sync-registry: 把 skill-registry.json（技能安装源清单）同步到 ~/.dsh/config/。
# 只有 otg install 会写它，make deploy 此前漏了 —— 导致仓库 v2 清单与运行时
# v1 长期漂移（skill-doctor 依据它判断缺失依赖）。
sync-registry:
	@echo "=== Syncing skill-registry.json to ~/.dsh/config/ ==="
	@mkdir -p $(HOME)/.dsh/config
	@-rm -f $(HOME)/.dsh/config/skill-registry.json.old 2>/dev/null || true
	@-mv $(HOME)/.dsh/config/skill-registry.json $(HOME)/.dsh/config/skill-registry.json.old 2>/dev/null || true
	@cp config/skill-registry.json $(HOME)/.dsh/config/skill-registry.json
	@echo "=== Done ==="

# ===========================================================================
# deploy — 唯一部署入口（替代 install-force）。
#   构建 → 单测 → busy-safe 安装 → 同步 skill/插件/技能清单
#   → vault-map 补默认字段 → 写 drop-in override
#   → agent-server 所有权收敛（managed=true 停 systemd/清孤儿，防 8799 冲突）
#   → daemon-reload → 重启 watcher
#   → managed=false 时按 checksum 判断是否重启 dsh-agent-server。
# 幂等、可随时重跑；日常用 `make deploy` 即可，不再需要 install-force。
# ===========================================================================
deploy: build test
	@echo "=== [1/6] busy-safe install $(BINARY) + $(GRILL) ==="
	mkdir -p $(HOME)/.local/bin $(GOBIN)
	@for b in $(BINARY) $(GRILL); do \
		-rm -f $(HOME)/.local/bin/$$b.old $(GOBIN)/$$b.old 2>/dev/null || true; \
		-mv $(HOME)/.local/bin/$$b $(HOME)/.local/bin/$$b.old 2>/dev/null || true; \
		cp $$b $(HOME)/.local/bin/$$b; \
		cp $$b $(GOBIN)/$$b; \
	done
	@echo "=== [2/6] sync skill docs + plugins + skill-registry ==="
	@$(MAKE) sync-docs
	@$(MAKE) sync-plugins
	@$(MAKE) sync-registry
	@echo "=== [2b/6] append missing vault-map.json default fields (safe merge) ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		CFG="$$SKILL_DIR/config/vault-map.json"; \
		mkdir -p "$$SKILL_DIR/config"; \
		if [ -f "$$CFG" ]; then \
			./otg config migrate --map-file "$$CFG" --write && echo "  vault-map.json merged with new defaults (kb_vault/env_cleanup etc.)" || echo "  (config migrate skipped — check ./otg)"; \
		else \
			echo "  (no vault-map.json yet — run \`otg install\` or create from obsidian-task-runner/config/vault-map.example.json)"; \
		fi
	@echo "=== [3/6] systemd drop-in override (daemon -> repo otg) ==="
	@mkdir -p $(HOME)/.config/systemd/user/otg-task-watcher.service.d
	@printf '[Service]\n# deploy: daemon loads the latest repo-built otg on every restart.\nExecStart=\nExecStart=%s/otg daemon\n' "$$(pwd)" > $(HOME)/.config/systemd/user/otg-task-watcher.service.d/deploy-override.conf
	@echo "=== [4/6] agent-server ownership reconcile (before daemon restart) ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		CFG="$$SKILL_DIR/config/vault-map.json"; \
		managed=$$(python3 -c 'import json,sys;print("true" if json.load(open(sys.argv[1])).get("agent_server_managed", True) else "false")' "$$CFG" 2>/dev/null || echo true); \
		if [ "$$managed" = "true" ]; then \
			echo "  agent_server_managed=true → daemon 自管 agent-server；停掉 systemd 实例并清理孤儿，避免 8799 冲突"; \
			systemctl --user disable --now dsh-agent-server 2>/dev/null || true; \
			pkill -f "headless-agent-server" 2>/dev/null || true; \
			sleep 1; \
			echo "  (重启 daemon 后由其拉起全新 agent-server，插件/技能变更即生效)"; \
		else \
			echo "  agent_server_managed=false → 由 systemd 管理 agent-server，deploy 不干预其生命周期"; \
		fi
	@echo "=== [5/6] daemon-reload + restart watcher ==="
	systemctl --user daemon-reload
	-systemctl --user reset-failed otg-task-watcher.service 2>/dev/null || true
	-systemctl --user restart otg-task-watcher.service 2>/dev/null || true
	@sleep 2
	@if ! systemctl --user -q is-active otg-task-watcher.service; then \
		echo "  Watcher didn't start — retrying..."; \
		systemctl --user reset-failed otg-task-watcher.service 2>/dev/null || true; \
		systemctl --user start otg-task-watcher.service 2>/dev/null || true; \
	fi
	@echo "=== [5b/6] externally-managed agent-server: restart if plugin changed ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		CFG="$$SKILL_DIR/config/vault-map.json"; \
		managed=$$(python3 -c 'import json,sys;print("true" if json.load(open(sys.argv[1])).get("agent_server_managed", True) else "false")' "$$CFG" 2>/dev/null || echo true); \
		if [ "$$managed" = "false" ]; then \
			changed=""; \
			for f in agent-server.mjs agent-monitor.html; do \
				cmp -s "deploy/dsh-plugins/$$f" "$(HOME)/.dsh/plugins/$$f" 2>/dev/null || changed="yes"; \
			done; \
			if [ -n "$$changed" ]; then \
				echo "  agent-server plugin/monitor changed — restarting dsh-agent-server"; \
				systemctl --user restart dsh-agent-server 2>/dev/null || echo "  (dsh-agent-server not running as user service; restart manually if needed)"; \
			else \
				echo "  agent-server plugin/monitor unchanged — no restart needed"; \
			fi; \
		else \
			echo "  (agent_server_managed=true — daemon 已拉起新 agent-server，无需 systemd 重启)"; \
		fi
	@echo "=== [6/6] done (daemon now runs repo otg) ==="
	@echo "  verify:   make deploy-status"
	@echo "  rollback: make rollback"

# deploy-status: 展示仓库 → 运行时的同步差异（代码 / skill / 插件），
# 一眼看出“改了但没同步”的东西。
deploy-status:
	@echo "=== otg binary: repo vs ~/.local/bin ==="
	@if [ -f $(BINARY) ] && [ -f $(HOME)/.local/bin/$(BINARY) ]; then \
		a=$$(sha256sum $(BINARY) | cut -d' ' -f1); b=$$(sha256sum $(HOME)/.local/bin/$(BINARY) | cut -d' ' -f1); \
		if [ "$$a" = "$$b" ]; then echo "  SAME ($${a:0:12})"; else echo "  DIFF repo=$${a:0:12} installed=$${b:0:12} → 跑 make deploy"; fi; \
	fi
	@echo "=== skill sync status ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
	for f in obsidian-task-runner/skills/*/SKILL.md obsidian-task-runner/SKILL.md obsidian-task-runner/reference.md; do \
		rel=$${f#obsidian-task-runner/}; \
		if [ -f "$$SKILL_DIR/$$rel" ]; then \
			diff -q "$$f" "$$SKILL_DIR/$$rel" >/dev/null 2>&1 && st=SAME || st=DIFF; \
		else st=MISSING; fi; \
		[ "$$st" != "SAME" ] && echo "  $$st  $$rel"; \
	done; \
	for s in $$(grep -v '^#' obsidian-task-runner/skills/manifest | grep -v '^$$'); do \
		if [ -f "$(HOME)/.dsh/skills/obsidian-task-runner-$$s/SKILL.md" ]; then \
			diff -q obsidian-task-runner/skills/$$s/SKILL.md "$(HOME)/.dsh/skills/obsidian-task-runner-$$s/SKILL.md" >/dev/null 2>&1 && st=SAME || st=DIFF; \
		else st=MISSING; fi; \
		[ "$$st" != "SAME" ] && echo "  $$st  obsidian-task-runner-$$s/SKILL.md"; \
	done; \
	echo "  (仅列出 DIFF/MISSING；无输出 = 全部已同步)"
	@echo "=== plugin sync status ==="
	@for f in deploy/dsh-plugins/*; do \
		b=$$(basename $$f); \
		if [ -f $(HOME)/.dsh/plugins/$$b ]; then \
			diff -q "$$f" "$(HOME)/.dsh/plugins/$$b" >/dev/null 2>&1 && st=SAME || st=DIFF; \
		else st=MISSING; fi; \
		[ "$$st" != "SAME" ] && echo "  $$st  plugins/$$b"; \
	done; \
	echo "  (仅列出 DIFF/MISSING；无输出 = 全部已同步)"
	@echo "=== skill-registry sync status ==="
	@if [ -f $(HOME)/.dsh/config/skill-registry.json ]; then \
		diff -q config/skill-registry.json $(HOME)/.dsh/config/skill-registry.json >/dev/null 2>&1 && echo "  SAME" || echo "  DIFF → 跑 make deploy"; \
	else echo "  MISSING → 跑 make deploy"; fi

# rollback: 撤掉 drop-in override，daemon 回固定安装路径 ~/.local/bin/otg。
rollback:
	@echo "=== Removing deploy drop-in (daemon -> ~/.local/bin/otg) ==="
	rm -f $(HOME)/.config/systemd/user/otg-task-watcher.service.d/deploy-override.conf
	systemctl --user daemon-reload
	-systemctl --user restart otg-task-watcher.service 2>/dev/null || true
	@echo "=== Rolled back (daemon now uses $(HOME)/.local/bin/otg) ==="

# install-force 保留为 deploy 的别名（旧肌肉记忆兼容），不再有独立逻辑。
install-force: deploy
