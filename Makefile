.PHONY: build test test-node test-cover bench lint clean install install-force deploy deploy-dryrun deploy-status rollback daemon-recover sync-docs sync-plugins sync-registry

BINARY := otg
GRILL  := kitty-grill
GOBIN  := $(or $(shell go env GOBIN 2>/dev/null),$(HOME)/go/bin)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)"

# $(SCTL) 需要 user bus 环境。make deploy / daemon-recover 可能在
# 无该环境的 shell 运行（cron、非登录 SSH、脚本），裸调 $(SCTL) 会
# 静默失败被 || true 吞掉——2026-08-31 事故：systemd dsh-agent-server 停不掉、
# 8799 被占用、daemon 自管 agent-server 永远起不来。显式注入环境，让这些
# 目标在任何 shell 下都能正确操控 user systemd。
USER_BUS_ENV := XDG_RUNTIME_DIR=/run/user/$(shell id -u) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(shell id -u)/bus
SCTL := $(USER_BUS_ENV) systemctl --user

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

# test-node: agent-server / kb-preflight 纯函数单测。
test-node:
	@if command -v node >/dev/null 2>&1; then \
		node deploy/dsh-plugins/agent-server.kb.test.mjs && \
		node deploy/dsh-plugins/kb-preflight.test.mjs; \
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
# 强制覆盖兜底：历史容器以 nobody 属主写入的二进制会让 cp 直接 EACCES
#（非属主无写权限），部署静默留下旧版本（2026-09-02 ~/.local/bin/otg 停在
# v0.44.0）。mv 备份后 rm -f 兜底再 cp，确保 install/deploy 不会静默失败。
install: build sync-docs
	@echo "=== Installing $(BINARY) + $(GRILL) ==="
	mkdir -p $(HOME)/.local/bin $(GOBIN)
	@for b in $(BINARY) $(GRILL); do \
		-rm -f $(HOME)/.local/bin/$$b.old $(GOBIN)/$$b.old 2>/dev/null || true; \
		-mv $(HOME)/.local/bin/$$b $(HOME)/.local/bin/$$b.old 2>/dev/null || true; \
		-rm -f $(HOME)/.local/bin/$$b $(GOBIN)/$$b 2>/dev/null || true; \
		cp $$b $(HOME)/.local/bin/$$b; \
		chmod 755 $(HOME)/.local/bin/$$b; \
		cp $$b $(GOBIN)/$$b; \
		chmod 755 $(GOBIN)/$$b; \
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
	@echo "=== Pruning stale managed docs/skills (仅清单内、且 repo 已移除的) ==="
	@# 安全兜底（2026-08-31 误删 dsh home patch 插件教训）：
	@#   1. 只清理受管目录内、且 repo 曾经管理、现已在清单中消失的文件；
	@#   2. 不直接 rm——先 mv 到 ~/.dsh/trash/<ts>/ 可恢复；
	@#   3. 清单外文件（dsh home patch 插件、用户自装）一律保留。
	@TRASH="$${TRASH_DIR:-$(HOME)/.dsh/trash}/$$(date +%Y%m%d-%H%M%S)"; \
	SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
	pruned=0; \
	for d in "$$SKILL_DIR/skills"/*; do \
		[ -d "$$d" ] || continue; \
		b=$$(basename "$$d"); \
		[ -d "obsidian-task-runner/skills/$$b" ] || { \
			if [ "$${DRY_RUN:-0}" = "1" ]; then echo "  [dry-run] would prune skill: $$b"; \
			else echo "  prune stale skill: $$b"; mkdir -p "$$TRASH"; mv "$$d" "$$TRASH/skill-$$b"; pruned=$$((pruned+1)); fi; \
		}; \
	done; \
	for f in "$$SKILL_DIR"/*.md; do \
		[ -f "$$f" ] || continue; \
		b=$$(basename "$$f"); \
		[ -f "obsidian-task-runner/$$b" ] || { \
			if [ "$${DRY_RUN:-0}" = "1" ]; then echo "  [dry-run] would prune doc: $$b"; \
			else echo "  prune stale doc: $$b"; mkdir -p "$$TRASH"; mv "$$f" "$$TRASH/doc-$$b"; pruned=$$((pruned+1)); fi; \
		}; \
	done; \
	for d in $(HOME)/.dsh/skills/obsidian-task-runner-*; do \
		[ -d "$$d" ] || continue; \
		b=$${d##*obsidian-task-runner-}; \
		grep -qx "$$b" obsidian-task-runner/skills/manifest 2>/dev/null || { \
			if [ "$${DRY_RUN:-0}" = "1" ]; then echo "  [dry-run] would prune phase-skill: $$b"; \
			else echo "  prune stale phase-skill: $$b"; mkdir -p "$$TRASH"; mv "$$d" "$$TRASH/phase-$$b"; pruned=$$((pruned+1)); fi; \
		}; \
	done; \
	[ "$$pruned" -gt 0 ] && echo "  → 已回收 $$pruned 项到 $$TRASH（可手动恢复）" || echo "  无受管残留需要清理"
	@find $(HOME)/.dsh/skills/obsidian-task-runner* -name '*.old' -delete 2>/dev/null || true
	@echo "=== Done ==="

# sync-plugins: 把 deploy/dsh-plugins/ 下的 DSH 插件同步到 ~/.dsh/plugins/
#（dsh profile 按绝对路径加载）。busy-safe 替换；agent-server.mjs 变更需
# 重启 agent-server 才生效（managed=true 由重启后的 daemon 拉起）。*.test.mjs
# 是 repo 单测（make test-node），不复制到运行时目录。同步后清理 repo 已删除
# 的残留插件（~/.dsh/plugins 是受管目录）。
sync-plugins:
	@echo "=== Syncing dsh plugins to ~/.dsh/plugins/ ==="
	mkdir -p $(HOME)/.dsh/plugins
	@for f in deploy/dsh-plugins/*; do \
		b=$$(basename $$f); \
		case "$$b" in *.test.mjs) echo "  skip test file: $$b"; continue;; esac; \
		-rm -f $(HOME)/.dsh/plugins/$$b.old 2>/dev/null || true; \
		-mv $(HOME)/.dsh/plugins/$$b $(HOME)/.dsh/plugins/$$b.old 2>/dev/null || true; \
		cp $$f $(HOME)/.dsh/plugins/$$b; \
		chmod 600 $(HOME)/.dsh/plugins/$$b; \
	done
	@echo "=== 清理 repo 自身旧版 .old（仅清 repo 会覆盖的那批，进回收站） ==="
	@TRASH="$${TRASH_DIR:-$(HOME)/.dsh/trash}/$$(date +%Y%m%d-%H%M%S)"; \
	cleaned=0; \
	for b in $$(ls deploy/dsh-plugins/ 2>/dev/null); do \
		if [ -f "$(HOME)/.dsh/plugins/$$b.old" ]; then \
			if [ "$${DRY_RUN:-0}" = "1" ]; then echo "  [dry-run] would clear old: $$b.old"; \
			else mkdir -p "$$TRASH"; mv "$(HOME)/.dsh/plugins/$$b.old" "$$TRASH/$$b.old"; cleaned=$$((cleaned+1)); fi; \
		fi; \
	done; \
	[ "$$cleaned" -gt 0 ] && echo "  → 已回收 $$cleaned 个 .old 到 $$TRASH" || echo "  无旧版 .old 需要清理"
	@echo "=== Done ==="
	@echo "  ⚠ ~/.dsh/plugins/ 还可能有 dsh home patch（cordis.patch.yml）手工引用的插件"
	@echo "    （fallback/dsh-commands/kb-distill/vault 等）——它们不在受管清单，deploy 绝不删除。"


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
#   → managed=false 时按 checksum 判断是否重启 dsh-agent-server
#   → kb-preflight 变更且 dsh-web 在跑时重启 dsh-web（[5c/6]）
# 幂等、可随时重跑；日常用 `make deploy` 即可，不再需要 install-force。
# ===========================================================================
deploy: build test
	@echo "=== [1/6] busy-safe install $(BINARY) + $(GRILL) ==="
	mkdir -p $(HOME)/.local/bin $(GOBIN)
	@for b in $(BINARY) $(GRILL); do \
		-rm -f $(HOME)/.local/bin/$$b.old $(GOBIN)/$$b.old 2>/dev/null || true; \
		-mv $(HOME)/.local/bin/$$b $(HOME)/.local/bin/$$b.old 2>/dev/null || true; \
		-rm -f $(HOME)/.local/bin/$$b $(GOBIN)/$$b 2>/dev/null || true; \
		cp $$b $(HOME)/.local/bin/$$b; \
		chmod 755 $(HOME)/.local/bin/$$b; \
		cp $$b $(GOBIN)/$$b; \
		chmod 755 $(GOBIN)/$$b; \
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
			echo "  agent_server_managed=true → daemon 自管 agent-server；移除 watcher 对 dsh-agent-server 的 Requires/After 依赖 + 停 systemd 实例，根治 8799 冲突"; \
			if ! $(SCTL) disable --now dsh-agent-server 2>/dev/null; then echo "  ⚠ 无法停用 systemd dsh-agent-server（bus 或服务状态异常），8799 可能仍被占用，请检查 systemctl --user status dsh-agent-server"; fi; \
			# 关键：systemd 的 drop-in 用空 After=/Requires= 不会清除依赖（追加语义）， \
			# 必须改基础 unit。旧 install 生成的 otg-task-watcher.service 无条件 \
			# Requires=dsh-agent-server（为 managed=false 设计），导致每次 restart watcher \
			# 都强制拉起 dsh-agent-server 抢占 8799（2026-08-31 死锁）。用 sed 移除 \
			# 这两行（保留 PATH/env/其余配置），再 daemon-reload。 \
			rm -f $(HOME)/.config/systemd/user/otg-task-watcher.service.d/deploy-agent-managed.conf; \
			sed -i -e '/^After=dsh-agent-server.service$$/d' -e '/^Requires=dsh-agent-server.service$$/d' $(HOME)/.config/systemd/user/otg-task-watcher.service; \
			$(SCTL) daemon-reload; \
			pkill -f "headless-agent[-]server" 2>/dev/null || true; \
			i=0; \
			while pgrep -f "headless-agent[-]server" >/dev/null 2>&1 && [ "$$i" -lt 30 ]; do sleep 0.5; i=$$((i+1)); done; \
			[ "$$i" -lt 30 ] && echo "  旧 agent-server 已退出（等待 $$i × 0.5s）" || echo "  ⚠ 旧 agent-server 30s 内未退出，daemon 可能误连旧实例，建议手动 $(SCTL) restart dsh-agent-server"; \
			if ss -tln | grep -q ':8799 '; then echo "  ⚠ 8799 仍被占用，daemon 启动可能误连旧实例"; else echo "  8799 已释放"; fi; \
			echo "  (重启 daemon 后由其拉起全新 agent-server，插件/技能变更即生效)"; \
		else \
			echo "  agent_server_managed=false → 由 systemd 管理 agent-server，deploy 不干预其生命周期"; \
		fi
	@echo "=== [5/6] daemon-reload + restart watcher ==="
	$(SCTL) daemon-reload
	-$(SCTL) reset-failed otg-task-watcher.service 2>/dev/null || true
	-$(SCTL) restart otg-task-watcher.service 2>/dev/null || true
	@sleep 2
	@if ! $(SCTL) -q is-active otg-task-watcher.service; then \
		echo "  Watcher didn't start — retrying..."; \
		$(SCTL) reset-failed otg-task-watcher.service 2>/dev/null || true; \
		$(SCTL) start otg-task-watcher.service 2>/dev/null || true; \
	fi
	@echo "=== [5b/6] externally-managed agent-server: restart if plugin changed ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		CFG="$$SKILL_DIR/config/vault-map.json"; \
		managed=$$(python3 -c 'import json,sys;print("true" if json.load(open(sys.argv[1])).get("agent_server_managed", True) else "false")' "$$CFG" 2>/dev/null || echo true); \
		if [ "$$managed" = "false" ]; then \
			changed=""; \
			for f in agent-server.mjs agent-monitor.html; do \
				cmp -s "$(HOME)/.dsh/plugins/$$f" "$(HOME)/.dsh/plugins/$$f.old" 2>/dev/null || changed="yes"; \
			done; \
			if [ -n "$$changed" ]; then \
				echo "  agent-server plugin/monitor changed — restarting dsh-agent-server"; \
				$(SCTL) restart dsh-agent-server 2>/dev/null || echo "  (dsh-agent-server not running as user service; restart manually if needed)"; \
			else \
				echo "  agent-server plugin/monitor unchanged — no restart needed"; \
			fi; \
		else \
			echo "  (agent_server_managed=true — daemon 已拉起新 agent-server，无需 systemd 重启)"; \
		fi
	@echo "=== [5c/6] dsh-web: restart if kb-preflight changed ==="
	@if [ -f "$(HOME)/.dsh/plugins/kb-preflight.mjs" ] && [ -f "$(HOME)/.dsh/plugins/kb-preflight.mjs.old" ]; then \
		cmp -s "$(HOME)/.dsh/plugins/kb-preflight.mjs" "$(HOME)/.dsh/plugins/kb-preflight.mjs.old" 2>/dev/null && changed="" || changed="yes"; \
	else \
		changed="yes"; \
	fi; \
	if [ -n "$$changed" ]; then \
		if $(SCTL) -q is-active dsh-web.service 2>/dev/null; then \
			echo "  kb-preflight changed — restarting dsh-web"; \
			$(SCTL) restart dsh-web 2>/dev/null || echo "  (dsh-web restart failed — run: systemctl --user restart dsh-web)"; \
		else \
			echo "  (dsh-web not active — kb-preflight 将在下次启动时加载)"; \
		fi; \
	else \
		echo "  kb-preflight unchanged — dsh-web 无需重启"; \
	fi
	@echo "=== [5d/6] omp 会话提炼扩展：已退役（omp 时代结束，~/.omp 不再存在） ==="
	@echo "  会话蒸馏现由独立工作区维护的 dsh 插件 kb-distill.mjs 承载"
	@echo "  （~/.dsh/plugins/，非本仓库部署——deploy 只同步仓库自有插件，不触碰它）"
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
	$(SCTL) daemon-reload
	-$(SCTL) restart otg-task-watcher.service 2>/dev/null || true
	@echo "=== Rolled back (daemon now uses $(HOME)/.local/bin/otg) ==="

# daemon-recover: 流水线停摆恢复。停掉 systemd 版 agent-server（收回 8799，
# 避免与 daemon 自管实例冲突）、清孤儿进程（括号断匹配防自误杀）、等端口释放，
# 再拉起 otg-task-watcher。仅在有 agent_server_managed=true 时执行——daemon
# 重启后自己拉起全新 agent-server。2026-08-31 事故现场：旧 Makefile 的
# pkill 把 daemon 连带杀掉、systemd agent-server 残留占 8799。
daemon-recover:
	@echo "=== daemon-recover: agent-server 所有权收敛 ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		CFG="$$SKILL_DIR/config/vault-map.json"; \
		managed=$$(python3 -c 'import json,sys;print("true" if json.load(open(sys.argv[1])).get("agent_server_managed", True) else "false")' "$$CFG" 2>/dev/null || echo true); \
		if [ "$$managed" = "true" ]; then \
			echo "  agent_server_managed=true → 停 systemd 实例 + 移除 watcher Requires + 清孤儿 + 等端口释放"; \
			if ! $(SCTL) disable --now dsh-agent-server 2>/dev/null; then echo "  ⚠ 无法停用 systemd dsh-agent-server，8799 可能仍被占用"; fi; \
			rm -f $(HOME)/.config/systemd/user/otg-task-watcher.service.d/deploy-agent-managed.conf; \
			sed -i -e '/^After=dsh-agent-server.service$$/d' -e '/^Requires=dsh-agent-server.service$$/d' $(HOME)/.config/systemd/user/otg-task-watcher.service; \
			$(SCTL) daemon-reload; \
			pkill -f "headless-agent[-]server" 2>/dev/null || true; \
			i=0; \
			while pgrep -f "headless-agent[-]server" >/dev/null 2>&1 && [ "$$i" -lt 30 ]; do sleep 0.5; i=$$((i+1)); done; \
			[ "$$i" -lt 30 ] && echo "  旧 agent-server 已退出（等待 $$i × 0.5s）" || echo "  ⚠ 旧 agent-server 30s 内未退出"; \
		else \
			echo "  agent_server_managed=false → 由 systemd 管理，跳过所有权收敛"; \
		fi
	@echo "=== 拉起 otg-task-watcher ==="
	@$(SCTL) daemon-reload
	@-$(SCTL) reset-failed otg-task-watcher.service 2>/dev/null || true
	@-$(SCTL) start otg-task-watcher.service 2>/dev/null || true
	@sleep 2
	@if ! $(SCTL) -q is-active otg-task-watcher.service; then \
		echo "  watcher 未启动，重试一次..."; \
		$(SCTL) reset-failed otg-task-watcher.service 2>/dev/null || true; \
		$(SCTL) start otg-task-watcher.service 2>/dev/null || true; \
	fi
	@echo "=== 验证 ==="
	@echo "  tail -20 ~/.dsh/logs/otg-daemon.log   # 应看到 agent-server starting → healthy → daemon started"
	@echo "  ss -tlnp | grep 8799                  # daemon 自管的 agent-server"

# deploy-dryrun: 安全预演——只打印 make deploy 会覆盖/清理哪些文件，不实际改动。
# 误删兜底的第一道防线：跑它确认没有意外删除目标，再跑真正 deploy。
# 用法：make deploy-dryrun
deploy-dryrun:
	@echo "=== [dry-run] sync-docs 将清理的受管残留 ==="
	@DRY_RUN=1 $(MAKE) -s sync-docs 2>&1 | grep -E "\[dry-run\]|无受管残留|prune stale|回收" || true
	@echo "=== [dry-run] sync-plugins 将清理的 .old ==="
	@DRY_RUN=1 $(MAKE) -s sync-plugins 2>&1 | grep -E "\[dry-run\]|无旧版|回收" || true
	@echo "=== [dry-run] 完成：以上即会删除/回收的内容；无输出=无删除。正式运行：make deploy ==="

# install-force 保留为 deploy 的别名（旧肌肉记忆兼容），不再有独立逻辑。
install-force: deploy
