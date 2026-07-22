.PHONY: build test test-cover bench lint clean install install-force

BINARY := otg
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/otg/

test:
	go test -race -cover ./...

test-cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

bench:
	go test -bench=. -benchmem ./...

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY) coverage.out coverage.html

install: build
	mkdir -p $(HOME)/.local/bin
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)
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
	mkdir -p $(HOME)/.local/bin
	-mv $(HOME)/.local/bin/$(BINARY) $(HOME)/.local/bin/$(BINARY).old 2>/dev/null || true
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)
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
