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

# GO_SOURCES is the formatting gate's file list, and the two prunes are both
# load-bearing.
#
# 🔴 `find`, NEVER `git ls-files`. ls-files lists only TRACKED files, so a gate
# run before `git add` silently skips the new files just written and reports
# CLEAN BY OMISSION — the standing rule in the project's resume, and a
# formatting gate is exactly where it bites: new files are the ones most likely
# to be unformatted.
#
# The .claude prune keeps a subagent worktree's checked-out copy of this module
# out of the count; without it the gate reports the same file twice and can fail
# on a tree the developer cannot edit.
GO_SOURCES = $(shell find . -name '*.go' -not -path './.git/*' -not -path './.claude/*')

.PHONY: fmt
fmt: ## Rewrite every Go source in place with gofmt
	@gofmt -w $(GO_SOURCES)

# gofmt -l PRINTS drifted files and exits 0, so a bare `gofmt -l` in CI is a
# reporter, not a gate — which is how a583440 and 076975e landed unformatted and
# sat at HEAD unnoticed. Nothing in this Makefile and nothing in
# .github/workflows ran gofmt at all before this target existed. The non-empty
# check is what turns the report into a failure.
.PHONY: fmt-check
fmt-check: ## FAIL if any Go source is not gofmt-clean
	@drift="$$(gofmt -l $(GO_SOURCES))"; \
	if [ -n "$$drift" ]; then \
		echo "gofmt drift — run 'make fmt':" >&2; \
		echo "$$drift" | sed 's/^/  /' >&2; \
		exit 1; \
	fi

##@ Test
.PHONY: test
test: build fmt-check vet ## Run unit tests — fast, no model download
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

# THE ONLY THING THAT RUNS THE DERIVED-GATE RULE. That rule type-checks the whole
# module (go/packages + SSA + a VTA call graph) to derive which commands and tools
# reach a vault-write sink, and it self-skips under -short — which `make test` and
# every CI job except `source-audit` pass. So if this target and its CI job go
# away, the rule runs NOWHERE and is green by never looking.
#
# No -run filter: the whole package runs, because a filter is one rename away from
# silently matching nothing. No -race either: this is single-goroutine analysis
# over an immutable source tree, and -race costs ~5x for nothing. The `test`
# target keeps -race for the code that actually has concurrency.
.PHONY: source-audit
source-audit: ## Run the source audit INCLUDING the type-checked derived-gate rule (no -short)
	go test -count=1 ./internal/sourceaudit/

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
