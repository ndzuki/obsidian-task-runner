.PHONY: build test test-cover bench lint clean install install-force deploy deploy-status rollback sync-docs sync-plugins

BINARY := otg
GRILL  := kitty-grill
GOBIN  := $(or $(shell go env GOBIN 2>/dev/null),$(HOME)/go/bin)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)"

build:
	go build -tags sqlite_fts5 $(LDFLAGS) -o $(BINARY) ./cmd/otg/
	go build -o $(GRILL) ./cmd/kitty-grill/

test:
	go test -race -tags sqlite_fts5 -cover ./...

test-cover:
	go test -race -tags sqlite_fts5 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

bench:
	go test -race -tags sqlite_fts5 -bench=. -benchmem ./...

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
	@echo "=== Done ==="

# sync-plugins: 把 deploy/dsh-plugins/ 下的 DSH 插件同步到 ~/.dsh/plugins/
#（dsh profile 按绝对路径加载）。busy-safe 替换；agent-server.mjs 变更需
# 重启 dsh-agent-server 才生效（install-force 末尾有提示）。
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
	@echo "=== Done ==="

# ===========================================================================
# deploy — 唯一部署入口（替代 install-force）。
#   构建 → 单测 → busy-safe 安装 → 同步 skill/插件 → 写 drop-in override
#   → daemon-reload → 重启 watcher → 插件变更时顺带重启 agent-server。
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
	@echo "=== [2/6] sync skill docs + plugins ==="
	@$(MAKE) sync-docs
	@$(MAKE) sync-plugins
	@echo "=== [3/6] systemd drop-in override (daemon -> repo otg) ==="
	@mkdir -p $(HOME)/.config/systemd/user/otg-task-watcher.service.d
	@printf '[Service]\n# deploy: daemon loads the latest repo-built otg on every restart.\nExecStart=\nExecStart=%s/otg daemon\n' "$$(pwd)" > $(HOME)/.config/systemd/user/otg-task-watcher.service.d/deploy-override.conf
	@echo "=== [4/6] daemon-reload + restart watcher ==="
	systemctl --user daemon-reload
	-systemctl --user reset-failed otg-task-watcher.service 2>/dev/null || true
	-systemctl --user restart otg-task-watcher.service 2>/dev/null || true
	@sleep 2
	@if ! systemctl --user -q is-active otg-task-watcher.service; then \
		echo "  Watcher didn't start — retrying..."; \
		systemctl --user reset-failed otg-task-watcher.service 2>/dev/null || true; \
		systemctl --user start otg-task-watcher.service 2>/dev/null || true; \
	fi
	@echo "=== [5/6] agent-server (restart only if plugin changed) ==="
	@if [ -n "$$(git diff --name-only -- deploy/dsh-plugins/agent-server.mjs 2>/dev/null | head -1)" ]; then \
		echo "  agent-server.mjs changed — restarting dsh-agent-server"; \
		systemctl --user restart dsh-agent-server 2>/dev/null || echo "  (dsh-agent-server not running as user service; restart manually if needed)"; \
	else \
		echo "  agent-server.mjs unchanged — no restart needed"; \
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

# rollback: 撤掉 drop-in override，daemon 回固定安装路径 ~/.local/bin/otg。
rollback:
	@echo "=== Removing deploy drop-in (daemon -> ~/.local/bin/otg) ==="
	rm -f $(HOME)/.config/systemd/user/otg-task-watcher.service.d/deploy-override.conf
	systemctl --user daemon-reload
	-systemctl --user restart otg-task-watcher.service 2>/dev/null || true
	@echo "=== Rolled back (daemon now uses $(HOME)/.local/bin/otg) ==="

# install-force 保留为 deploy 的别名（旧肌肉记忆兼容），不再有独立逻辑。
install-force: deploy
