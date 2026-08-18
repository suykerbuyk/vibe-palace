# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0

BINARY  := vp
CMD     := ./cmd/$(BINARY)
PREFIX  ?= $(HOME)/.local

BASE_VERSION := 0.1.0
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# DIRTY is derived from VERSION, never from a second git invocation. VERSION
# above is the ONE place the working tree is inspected; `git diff --quiet` here
# would be a second derivation that can disagree with the first, which is the
# failure mode that produced this bug (the Makefile computed --dirty and then
# threw it away). If VERSION is overridden from the environment, DIRTY follows
# that override rather than re-deriving behind it.
DIRTY        := $(if $(findstring -dirty,$(VERSION)),true,false)

LDFLAGS      := -X main.version=$(BASE_VERSION) \
                -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) \
                -X main.dirty=$(DIRTY) \
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
	@$(MAKE) --no-print-directory live-canary

# The live-vault canaries measure a vault that lives OUTSIDE this module, so `go
# test` cannot observe its contents changing and will serve a CACHED verdict — an
# instrument confidently describing a vault it did not look at. Measured: a
# workflow.md grown 13.5 KB -> 19.5 KB still reported `ok (cached)`. They must run
# uncached or they are not a gate.
#
# It runs LAST in `test`, not as a prerequisite: as a prerequisite a red canary
# aborts make before the unit suite runs at all.
#
# -v IS LOAD-BEARING: `go test` prints a bare `ok` for a package whose tests all
# SKIPPED, so without it a skipped canary is visually identical to a passing one.
.PHONY: live-canary
live-canary: ## Run the live-vault bootstrap canary uncached and verbose (SKIP is printed, not hidden)
	go test -count=1 -v -run TestBootstrapLiveVaultStillRestoresASession ./internal/tools/

.PHONY: test-full
test-full: build vet ## Run full test suite including ONNX integration tests
	go test -count=1 -cover ./...

.PHONY: integration
integration: build ## Run integration tests only (requires ONNX model)
	go test -count=1 -run TestIntegration -v ./...

.PHONY: init-e2e
init-e2e: ## Run bash end-to-end harness for `vp init` (sandboxed HOME, builds its own binary)
	bash test/e2e/init/run.sh

.PHONY: dispatch-e2e
dispatch-e2e: ## Run bash e2e for CLI dispatch (parent-bare help, unknown-subcommand exit codes)
	bash test/e2e/dispatch/run.sh

.PHONY: walkthrough-e2e
walkthrough-e2e: ## Run canonical walkthrough (prints transcript on pass)
	bash test/e2e/walkthrough/run.sh

.PHONY: workflows-e2e
workflows-e2e: ## Run multi-iteration workflow measurement rig
	bash test/e2e/workflows/run.sh

.PHONY: e2e
e2e: init-e2e dispatch-e2e walkthrough-e2e workflows-e2e ## Run all e2e tiers

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

.PHONY: uninstall
uninstall: ## Remove installed binary and man pages from PREFIX
	rm -f $(PREFIX)/bin/$(BINARY)
	rm -f $(PREFIX)/share/man/man1/vp*.1

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
