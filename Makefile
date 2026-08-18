.PHONY: build test test-cover bench lint clean install install-force

BINARY := otg
GOBIN  := $(or $(shell go env GOBIN 2>/dev/null),$(HOME)/go/bin)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)"

build:
	go build -tags sqlite_fts5 $(LDFLAGS) -o $(BINARY) ./cmd/otg/

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
	rm -f $(BINARY) coverage.out coverage.html

install: build sync-docs
	mkdir -p $(HOME)/.local/bin $(GOBIN)
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)
	cp $(BINARY) $(GOBIN)/$(BINARY)
	@echo "Installed to $(HOME)/.local/bin/$(BINARY)"

install-force: build
	@echo "=== Stopping timer ==="
	-systemctl --user stop --no-block omp-task-runner.timer 2>/dev/null || true
	@echo "=== Stopping daemon (OMP processes survive) ==="
	-systemctl --user stop --no-block omp-task-watcher.service 2>/dev/null || true
	-pkill -TERM -U "$(id -u)" -f "otg daemon" 2>/dev/null || true
	@sleep 2
	-pkill -9 -U "$(id -u)" -f "otg daemon" 2>/dev/null || true
	@sleep 1
	@echo "=== Installing new binary ==="
	-rm -f $(HOME)/.local/bin/$(BINARY).old $(GOBIN)/$(BINARY).old 2>/dev/null || true
	mkdir -p $(HOME)/.local/bin $(GOBIN)
	-mv $(HOME)/.local/bin/$(BINARY) $(HOME)/.local/bin/$(BINARY).old 2>/dev/null || true
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)
	cp $(BINARY) $(GOBIN)/$(BINARY)
	@echo "=== Installing systemd units ==="
	$(HOME)/.local/bin/$(BINARY) install-systemd
	@echo "=== Ensuring services are running ==="
	-systemctl --user reset-failed omp-task-watcher.service omp-task-runner.service 2>/dev/null || true
	systemctl --user start omp-task-runner.timer 2>/dev/null || true
	-systemctl --user start omp-task-watcher.service 2>/dev/null || true
	@sleep 2
	@if ! systemctl --user -q is-active omp-task-watcher.service; then \
		echo "  Watcher didn't start (lock may be held) — retrying..."; \
		systemctl --user reset-failed omp-task-watcher.service 2>/dev/null || true; \
		systemctl --user start omp-task-watcher.service 2>/dev/null || true; \
	fi
	@echo "=== Done ==="
	@$(MAKE) sync-docs

.PHONY: sync-docs
sync-docs:
	@echo "=== Syncing skill docs to ~/.omp/ ==="
	cp -r obsidian-task-runner/*.md $(HOME)/.omp/skills/obsidian-task-runner/
	cp -r obsidian-task-runner/skills/ $(HOME)/.omp/skills/obsidian-task-runner/
	@# workflow.md 是仓库 docs/ 的设计文档，不再随技能包安装（处理 5：
	@# 运行时包只含执行契约 SKILL.md + reference.md；完整规范留在仓库）。
	@# knowledge-base is an external skill; the repo copy is the versioned
	@# source for rollback. Only overwrite the installed file, never the
	@# vault data it reads.
	cp obsidian-task-runner/skills/knowledge-base/SKILL.md $(HOME)/.omp/skills/knowledge-base/SKILL.md
	@# kulala-http is a self-authored general skill; repo copy is the versioned
	@# source for rollback, synced like knowledge-base.
	mkdir -p $(HOME)/.omp/skills/kulala-http
	cp obsidian-task-runner/skills/kulala-http/SKILL.md $(HOME)/.omp/skills/kulala-http/SKILL.md
	@for s in $$(grep -v '^#' obsidian-task-runner/skills/manifest | grep -v '^$$'); do \
		mkdir -p $(HOME)/.omp/skills/obsidian-task-runner-$$s; \
		cp obsidian-task-runner/skills/$$s/SKILL.md $(HOME)/.omp/skills/obsidian-task-runner-$$s/SKILL.md; \
	done
	@echo "=== Done ==="
