SHELL := /usr/bin/env bash

BUILD_DIR := $(CURDIR)/build
BUILD_OUTPUT := $(BUILD_DIR)/ginkgo-cli
BIN_SYMLINK := $(HOME)/.local/bin/ginkgo-cli
BIN_DAEMON_SYMLINK := $(HOME)/.local/bin/ginkgod
SERVICE_DIR := $(HOME)/.config/systemd/user
SERVICE_FILE := $(CURDIR)/systemd/ginkgo.service

# --- Docs / man ---
PREFIX ?= $(HOME)/.local
MANPREFIX ?= $(PREFIX)/share/man
MANDIR := $(MANPREFIX)/man1
DOCDIR := $(CURDIR)/docs
MARKDOWNDIR := $(DOCDIR)/markdown
MANOUT := $(DOCDIR)/man

GO_TAGS ?=

.PHONY: build install-binary install-service reload-service run dev setup-precommit \
        docs install-man uninstall-man install

# Generate Markdown + man pages from cmd/ginkgo-cli/doc_gen.go (build-tagged //go:build ignore)
docs:
	mkdir -p "$(MARKDOWNDIR)" "$(MANOUT)"
	@if ! go list -m -f '{{.Path}}' github.com/cpuguy83/go-md2man/v2 >/dev/null 2>&1; then \
		go get github.com/cpuguy83/go-md2man/v2@latest && go mod tidy; \
	fi
	go run ./cmd/ginkgo-cli/doc_gen.go
	@echo "Docs generated into $(MARKDOWNDIR) and $(MANOUT)"

build:
	mkdir -p $(BUILD_DIR)
	@if [ -n "$(GO_TAGS)" ]; then \
		go build -tags '$(GO_TAGS)' -o $(BUILD_OUTPUT) ./cmd/ginkgo-cli; \
	else \
		go build -o $(BUILD_OUTPUT) ./cmd/ginkgo-cli; \
	fi

install-man: docs
	mkdir -p "$(MANDIR)"
	install -m644 $(MANOUT)/*.1 "$(MANDIR)/"
	gzip -f "$(MANDIR)"/*.1
	-@command -v mandb >/dev/null 2>&1 && mandb -q || true
	@echo "Man pages installed to $(MANDIR)"

uninstall-man:
	@rm -f "$(MANDIR)"/ginkgo-cli*.1.gz || true
	@echo "Removed man pages from $(MANDIR)"

install-binary:
	@echo "Creating $(HOME)/.local/bin if it doesn't exist..."
	mkdir -p $(HOME)/.local/bin
	@echo "Symlinking binary from $(BUILD_OUTPUT) to $(BIN_SYMLINK)..."
	ln -sf $(BUILD_OUTPUT) $(BIN_SYMLINK)
	@echo "Symlinking binary from $(BUILD_OUTPUT) to $(BIN_DAEMON_SYMLINK)..."
	ln -sf $(BUILD_OUTPUT) $(BIN_DAEMON_SYMLINK)
	@echo "Binary symlinked to $(BIN_SYMLINK)"
	@echo "Binary symlinked to $(BIN_DAEMON_SYMLINK)"

install-service:
	@echo "Creating systemd user service directory..."
	mkdir -p $(SERVICE_DIR)
	@echo "Symlinking service file..."
	ln -sf $(SERVICE_FILE) $(SERVICE_DIR)/ginkgo.service
	@echo "Service file symlinked to $(SERVICE_DIR)/ginkgo.service"

reload-service:
	@echo "Reloading systemd user daemon..."
	systemctl --user daemon-reload
	@echo "Restarting ginkgo service..."
	systemctl --user restart ginkgo.service

# one-shot local install: binary + man
install: build install-binary install-man

run: build install-binary install-service reload-service

dev: run

setup-precommit:
	@if command -v pre-commit >/dev/null 2>&1; then \
		echo "pre-commit already installed."; \
	elif command -v pacman >/dev/null 2>&1; then \
		echo "Installing pre-commit via pacman..."; \
		sudo pacman -S --needed --noconfirm pre-commit; \
	elif command -v apt-get >/dev/null 2>&1; then \
		echo "Installing pre-commit via apt..."; \
		sudo apt-get update && sudo apt-get install -y pre-commit; \
	elif command -v brew >/dev/null 2>&1; then \
		echo "Installing pre-commit via Homebrew..."; \
		brew install pre-commit; \
	else \
		echo "No supported package manager found, falling back to pip..."; \
		pip install --user pre-commit; \
	fi
	pre-commit install

# --- Protobuf ---
.PHONY: proto
proto:
	@command -v protoc >/dev/null 2>&1 || { echo "protoc not found"; exit 1; }
	@command -v protoc-gen-go >/dev/null 2>&1 || { echo "protoc-gen-go not found"; exit 1; }
	protoc --go_out=paths=source_relative:. internal/ipc/pb/ipc.proto
