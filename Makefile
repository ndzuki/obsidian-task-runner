.PHONY: build test lint install clean

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
	@echo "=== Stopping timer and services ==="
	-systemctl --user stop omp-task-runner.timer 2>/dev/null || true
	-systemctl --user stop omp-task-watcher.service omp-task-runner.service 2>/dev/null || true
	@echo "=== Killing old processes ==="
	-pkill -9 -f "obsidian-task-runner" 2>/dev/null || true
	-pkill -9 -f "otg daemon" 2>/dev/null || true
	@sleep 1
	@echo "=== Installing new binary ==="
	mkdir -p $(HOME)/.local/bin
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "Installed to $(HOME)/.local/bin/$(BINARY)"
	@echo "=== Restarting services ==="
	-systemctl --user reset-failed omp-task-watcher.service omp-task-runner.service 2>/dev/null || true
	systemctl --user start omp-task-runner.timer
	systemctl --user start omp-task-watcher.service
	@echo "=== Done ==="

run: build
	./$(BINARY)
