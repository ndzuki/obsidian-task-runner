.PHONY: _archive-guard build test test-node test-cover bench lint clean deploy deploy-dryrun deploy-status rollback sync-docs install-standalone sync-plugins sync-registry verify-install check-routes

BINARY := otg
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

# ---------------------------------------------------------------------------
# 归档守卫（2026-09-14）
#
# 本仓库已归档：daemon 与阶段流水线不再使用。但下列目标会把它们**整套装回来**——
# deploy 会 restart otg-task-watcher.service、sync-docs 装回 9 个阶段 skill、
# sync-plugins 装回 agent-server.mjs / agent-monitor.html、deploy 还会重建 service
# drop-in 与 reload。换言之：**一次 `make deploy` 就能撤销归档**。
#
# 所以这些目标默认拒绝执行。确需在本地重建旧流水线时显式覆盖：
#     make deploy FORCE_ARCHIVED=1
# 归档后仍受支持的目标：`make install-standalone`（只装交互式 skill）、
# build / test / lint / clean（不受影响）。
# ---------------------------------------------------------------------------
# ⚠️ otg daemon 与阶段流水线已于 2026-09-14 归档、2026-09-29 彻底删除代码。
# 本 Makefile 只保留构建 / 测试 / skill 同步 / otg CLI 安装检查相关目标；
# install / install-force / daemon-recover 已随 daemon 删除。

_archive-guard:
	@if [ "$${FORCE_ARCHIVED:-0}" != "1" ]; then \
		echo "本仓库已归档（2026-09-14）：daemon 与阶段流水线已停用。"; \
		echo "  install / deploy / sync-docs / sync-plugins / daemon-recover 会重新启用服务"; \
		echo "  并装回阶段 skill，因此默认拒绝执行。"; \
		echo "  · 只安装交互式 skill：make install-standalone"; \
		echo "  · 确需重建旧流水线：make <目标> FORCE_ARCHIVED=1"; \
		exit 1; \
	fi

build:
	go build -tags sqlite_fts5 $(LDFLAGS) -o $(BINARY) ./cmd/otg/

# test/test-cover/bench：加 GIT_TERMINAL_PROMPT=0（任何泄漏到网络 git 的调用
# 快速失败而非在终端卡死）与 -timeout 5m（单个包测试挂起时自行超时并打印
# 卡住的测试名 + goroutine dump，而不是让 make 无限等待）。-p 4 限制并行包数，
# 避免与运行中的 otg daemon / dsh-agent-server 抢资源导致机器卡顿。
# test 顺带跑 agent-server 的 node 单测（node 不可用则跳过，不阻塞 Go 测试）。
test:
	GIT_TERMINAL_PROMPT=0 go test -race -tags sqlite_fts5 -cover -p 4 -timeout 5m ./...
	@$(MAKE) test-node

# test-node: （已无 JS 单测）
# 2026-09-23：otg 项目归档，daemon 侧 DSH 插件全部移出 deploy/ ——
#   kb-preflight.mjs / kb-distill 随 KB 面归档；agent-server.mjs + agent-server.kb.test.mjs
#   随 daemon 组件归档（服务 dsh-agent-server 已 inactive+disabled、8799 未监听、插件从未部署）。
#   均见 ~/.dsh/archive/kb-20260922-1709/otg-repo-source/。deploy/dsh-plugins/ 现仅剩
#   daemon 时代的监控 HTML（无 JS 单测）。
test-node:
	@echo "  (no JS unit tests left — daemon-era DSH plugins archived 2026-09-23)"

test-cover:
	GIT_TERMINAL_PROMPT=0 go test -race -tags sqlite_fts5 -coverprofile=coverage.out -p 4 -timeout 5m ./...
	go tool cover -html=coverage.out -o coverage.html

bench:
	GIT_TERMINAL_PROMPT=0 go test -race -tags sqlite_fts5 -bench=. -benchmem -timeout 5m ./...

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY) coverage.out coverage.html


# verify-install: 逐字节比对 repo 二进制与两处安装目标（~/.local/bin、
# $(GOBIN)）。任一处是旧版本/缺失 → 打印原因与修复命令并 exit 1，
# 让 deploy/install 在静默降级前叫停。
verify-install:
	@fail=""; \
	for b in $(BINARY); do \
		cmp -s "$$b" "$(HOME)/.local/bin/$$b" || fail="$$fail $(HOME)/.local/bin/$$b"; \
		cmp -s "$$b" "$(GOBIN)/$$b" || fail="$$fail $(GOBIN)/$$b"; \
	done; \
	if [ -n "$$fail" ]; then \
		echo "ERROR: 二进制安装不一致（旧版本残留或缺失）：$$fail"; \
		ls -l $(HOME)/.local/bin/$(BINARY) $(GOBIN)/$(BINARY)  2>/dev/null || true; \
		echo "修复："; \
		echo "  1) 目标文件属主异常（历史容器以 nobody 写入）→"; \
		echo "     sudo chown $$(id -un):$$(id -gn) $(HOME)/.local/bin/$(BINARY) $(GOBIN)/$(BINARY)"; \
		echo "     然后重跑 make deploy"; \
		echo "  2) ~/.local 或 ~/go/bin 只读挂载 → mount | grep -i '\.local\|go/bin' 检查并恢复可写"; \
		echo "  3) 目录不可写 → chmod u+w $(HOME)/.local/bin"; \
		exit 1; \
	fi; \
	echo "verified: $(HOME)/.local/bin/$(BINARY) $(GOBIN)/$(BINARY)"

sync-docs: _archive-guard
	@echo "=== Syncing skill docs to ~/.dsh/skills/obsidian-task-runner/ ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		mkdir -p "$$SKILL_DIR"; \
		cp -r obsidian-task-runner/*.md "$$SKILL_DIR/"
	@# workflow.md is the repo design doc; not shipped with the runtime skill
	@# package (runtime = SKILL.md + reference.md; full spec stays in repo).
	@# 2026-09-29：阶段 skill（manifest 曾列 9 个）已彻底退役，不再安装。
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

# install-standalone: 把 skills-standalone/ 下的通用 skill 装到 ~/.dsh/skills/<name>。
#
# 与 sync-docs 的分工：sync-docs 装 obsidian-task-runner 阶段 skill（服务任务流水线，
# 需要 daemon/TASK frontmatter 语境）；本目标只装**与流水线无关**的日常 skill
# （项目基线 / 风险感知计划 / 逐 AC 交付 / 设计两次），它们在任何交互会话里都能用。
#
# 显式 opt-in：**不**挂在 install / deploy / sync-docs 上——跑不跑、什么时候跑由用户决定，
# 停止自动化后这几个 skill 依然可用。
#
# 安全边界（遵循 CONTRIBUTING「用户资产所有权」）：
#   1. 只写 skills-standalone/<name>/SKILL.md 对应的 ~/.dsh/skills/<name>/SKILL.md；
#   2. 只覆盖本目录清单内的这几个名字，用户自装 skill 与其它通道的 skill 一律不碰；
#   3. 不删除、不清理——卸载由用户显式 `rm -rf ~/.dsh/skills/<name>`。
#
# 用法：make install-standalone            正式安装
#       make install-standalone DRY_RUN=1  只打印将写哪些文件
install-standalone:
	@echo "=== Installing standalone skills to $(HOME)/.dsh/skills/ ==="
	@installed=0; \
	for d in skills-standalone/*/; do \
		[ -f "$$d/SKILL.md" ] || continue; \
		name=$$(basename "$$d"); \
		dest="$(HOME)/.dsh/skills/$$name"; \
		if [ "$${DRY_RUN:-0}" = "1" ]; then \
			echo "  [dry-run] would install: $$name → $$dest/SKILL.md"; \
			continue; \
		fi; \
		mkdir -p "$$dest"; \
		cp "$$d/SKILL.md" "$$dest/SKILL.md"; \
		echo "  install: $$name → $$dest/SKILL.md"; \
		installed=$$((installed+1)); \
	done; \
	if [ "$${DRY_RUN:-0}" != "1" ]; then \
		echo "  → 已安装 $$installed 个（chezmoi 若纳管 ~/.dsh/skills 会自动收编）"; \
		echo "  → 卸载：rm -rf $(HOME)/.dsh/skills/<name>"; \
	fi
	@echo "=== Done ==="

# sync-plugins: 把 deploy/dsh-plugins/ 下的 DSH 插件同步到 ~/.dsh/plugins/
#（dsh profile 按绝对路径加载）。busy-safe 替换；agent-server.mjs 变更需
# 重启 agent-server 才生效（managed=true 由重启后的 daemon 拉起）。*.test.mjs
# 是 repo 单测（make test-node），不复制到运行时目录。同步后清理 repo 已删除
# 的残留插件（~/.dsh/plugins 是受管目录）。
sync-plugins: _archive-guard
	@echo "=== Syncing dsh plugins to ~/.dsh/plugins/ ==="
	mkdir -p $(HOME)/.dsh/plugins
	@for f in deploy/dsh-plugins/*; do \
		b=$$(basename $$f); \
		case "$$b" in *.test.mjs) echo "  skip test file: $$b"; continue;; \
		              *.classic.html) echo "  skip rollback copy: $$b"; continue;; esac; \
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
	@echo "    （fallback/dsh-commands 等）——它们不在受管清单，deploy 绝不删除。"


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
#   → （已退役）kb-preflight 变更重启 dsh-web：KB 面归档后此步不再执行
# 幂等、可随时重跑；日常用 `make deploy` 即可，不再需要 install-force。
# ===========================================================================
deploy: _archive-guard build test
	@echo "=== [1/6] busy-safe install $(BINARY) ==="
	mkdir -p $(HOME)/.local/bin $(GOBIN)
	@for b in $(BINARY); do \
		-rm -f $(HOME)/.local/bin/$$b.old $(GOBIN)/$$b.old 2>/dev/null || true; \
		-mv $(HOME)/.local/bin/$$b $(HOME)/.local/bin/$$b.old 2>/dev/null || true; \
		-rm -f $(HOME)/.local/bin/$$b $(GOBIN)/$$b 2>/dev/null || true; \
		cp $$b $(HOME)/.local/bin/$$b; \
		chmod 755 $(HOME)/.local/bin/$$b; \
		cp $$b $(GOBIN)/$$b; \
		chmod 755 $(GOBIN)/$$b; \
	done
	@$(MAKE) verify-install
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
	@echo "=== [4a/6] (grilling writeback wait removed: kitty-grill retired) ==="
	@echo "=== [4/6] agent-server ownership reconcile (detect-only) ==="
	@SKILL_DIR="$${SKILL_INSTALL_DIR:-$(HOME)/.dsh/skills/obsidian-task-runner}"; \
		CFG="$$SKILL_DIR/config/vault-map.json"; \
		managed=$$(python3 -c 'import json,sys;print("true" if json.load(open(sys.argv[1])).get("agent_server_managed", True) else "false")' "$$CFG" 2>/dev/null || echo true); \
		rm -f $(HOME)/.config/systemd/user/otg-task-watcher.service.d/deploy-agent-managed.conf; \
		if [ "$$managed" = "true" ]; then \
			if grep -qE '^(After|Requires)=dsh-agent-server\.service' $(HOME)/.config/systemd/user/otg-task-watcher.service 2>/dev/null; then \
				echo "  ⚠ legacy watcher unit pins dsh-agent-server (older install). deploy NEVER edits your systemd unit files."; \
				echo "    → run: otg install-systemd   (regenerates managed-aware units), then make deploy again."; \
			else \
				echo "  agent_server_managed=true — watcher unit is managed-aware, nothing to reconcile."; \
			fi; \
		else \
			echo "  agent_server_managed=false → systemd 管理 agent-server，deploy 不干预其生命周期"; \
		fi
	@echo "=== [5/6] daemon-reload + restart watcher ==="
	@if grep -qE '^(After|Requires)=dsh-agent-server\.service' $(HOME)/.config/systemd/user/otg-task-watcher.service 2>/dev/null; then \
		echo "  ⚠ skipping watcher restart: legacy unit pins dsh-agent-server (never modify user units)."; \
		echo "    → run: otg install-systemd, then make deploy again."; \
	else \
		$(SCTL) daemon-reload; \
		-$(SCTL) reset-failed otg-task-watcher.service 2>/dev/null || true; \
		-$(SCTL) restart otg-task-watcher.service 2>/dev/null || true; \
		sleep 2; \
		if ! $(SCTL) -q is-active otg-task-watcher.service; then \
			echo "  Watcher didn't start — retrying..."; \
			$(SCTL) reset-failed otg-task-watcher.service 2>/dev/null || true; \
			$(SCTL) start otg-task-watcher.service 2>/dev/null || true; \
		fi; \
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
	@echo "=== [5c/6] dsh-web: 已退役（kb-preflight 随 KB 面归档移除） ==="
	@echo "  KB 注入插件已归档，dsh-web 不再因其变更而重启。"
	@echo "  若改了 ~/.dsh/plugins/ 下其它插件需重启，手工执行：systemctl --user restart dsh-web"
	@echo "  ⚠️ 2026-09-23 修正：原实现用「文件不存在 ⇒ changed=yes」的兜底，导致"
	@echo "     kb-preflight 归档后**每次 make deploy 都无条件重启 dsh-web**。"
	@echo "=== [5d/6] 会话提炼扩展：已退役（旧执行器时代结束） ==="
	@echo "  会话蒸馏原由 kb-distill.mjs 承载，该插件已随 KB 面归档移除。"
	@echo "  （~/.dsh/plugins/，非本仓库部署——deploy 只同步仓库自有插件，不触碰它）"
	@echo "=== [6/6] done (daemon now runs repo otg) ==="
	@echo "  verify:   make deploy-status"
	@echo "  rollback: make rollback"
	@echo ""
	@echo "=== 需要用户手动操作（deploy 不代替你完成） ==="
	@echo "  • 若 daemon 未自动重启：systemctl --user restart otg-task-watcher.service"
	@echo "  • 若提示 legacy unit pins dsh-agent-server：先 otg install-systemd，再重启"
	@echo "  • 验证：systemctl --user status otg-task-watcher.service"
	@echo "  • 查看 daemon 日志：tail -f ~/.dsh/logs/otg-daemon.log"

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


# deploy-dryrun: 安全预演——只打印 make deploy 会覆盖/清理哪些文件，不实际改动。
# 误删兜底的第一道防线：跑它确认没有意外删除目标，再跑真正 deploy。
# 用法：make deploy-dryrun
deploy-dryrun: _archive-guard
	@echo "=== [dry-run] sync-docs 将清理的受管残留 ==="
	@DRY_RUN=1 $(MAKE) -s sync-docs 2>&1 | grep -E "\[dry-run\]|无受管残留|prune stale|回收" || true
	@echo "=== [dry-run] sync-plugins 将清理的 .old ==="
	@DRY_RUN=1 $(MAKE) -s sync-plugins 2>&1 | grep -E "\[dry-run\]|无旧版|回收" || true
	@echo "=== [dry-run] 完成：以上即会删除/回收的内容；无输出=无删除。正式运行：make deploy ==="


# ---------------------------------------------------------------------------
# check-routes —— ROUTES.md 体积门禁（M11 其二，2026-09-25）
#
# `project-context.mjs` 把每个项目的 Notes/ROUTES.md 当作常驻指针注入，硬上限
# PROJECT_ROUTES_MAX（UTF-8 字节）。超限 ⇒ routesDigest() 按行边界截断 ⇒ **超出的指针
# 从注入面消失**（实测：001 的 ## 基线三件套曾被整段吞掉）。故 ROUTES 改动前后都应跑本门禁。
#
# 上限不硬编码：由脚本从插件源码读出（单一事实源），插件改上限这里自动跟随。
# 不受 _archive-guard 约束：它只读 vault、不装任何东西。
# 用法：make check-routes
# ---------------------------------------------------------------------------
check-routes:
	@python3 scripts/check-routes.py
