# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0

BINARY  := vp
CMD     := ./cmd/$(BINARY)
PREFIX  ?= $(HOME)/.local

BASE_VERSION := 0.1.0
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      := -X main.version=$(BASE_VERSION) \
                -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) \
                -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.DEFAULT_GOAL := help

##@ General
.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
	@echo ""
	@echo "Quick start:  make build && make test"

##@ Build
.PHONY: build
build: man ## Build all packages and generate man pages
	go build ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

##@ Test
.PHONY: test
test: build vet ## Run unit tests — fast, no model download
	go test -race -short -cover ./...

.PHONY: test-full
test-full: build vet ## Run full test suite including ONNX integration tests
	go test -count=1 -cover ./...

.PHONY: integration
integration: build ## Run integration tests only (requires ONNX model)
	go test -count=1 -run TestIntegration -v ./...

.PHONY: cover
cover: ## Generate HTML coverage report (short mode)
	go test -race -short -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

.PHONY: cover-full
cover-full: ## Generate HTML coverage report (full suite)
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

##@ Install
.PHONY: install
install: build ## Build and install binary + man pages to PREFIX (default: ~/.local)
	go build -ldflags "$(LDFLAGS)" -o $(PREFIX)/bin/$(BINARY) $(CMD)
	@mkdir -p $(PREFIX)/share/man/man1
	@cp doc/man/man1/*.1 $(PREFIX)/share/man/man1/

##@ Release
.PHONY: release
release: ## Tag-based release — build and publish to GitHub Releases
	goreleaser release --clean

.PHONY: snapshot
snapshot: ## Local release dry-run — build release binaries without publishing
	goreleaser release --snapshot --clean

##@ Documentation
.PHONY: man
man: ## Generate man pages from command metadata
	@mkdir -p doc/man/man1
	VP_GEN_MAN=1 go test -run TestGenerateManPages ./cmd/vp/

##@ Clean
.PHONY: clean
clean: ## Remove build artifacts (preserves model cache)
	go clean ./...
	rm -f coverage.out coverage.html
	rm -rf doc/man/

.PHONY: dist-clean
dist-clean: clean ## Remove all artifacts including model cache
	rm -rf .cache/
