# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0

BINARY  := vp
CMD     := ./cmd/$(BINARY)
PREFIX  ?= $(HOME)/.local

.DEFAULT_GOAL := help

##@ General
.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
	@echo ""
	@echo "Quick start:  make build && make test"

##@ Build
.PHONY: build
build: ## Build all packages
	go build ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

##@ Test
.PHONY: test
test: build vet ## Run unit tests (with race detector)
	go test -race -cover ./...

.PHONY: integration
integration: build ## Run integration tests
	go test -race -tags=integration ./...

.PHONY: cover
cover: ## Generate HTML coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

##@ Install
.PHONY: install
install: build ## Build and install to PREFIX (default: ~/.local)
	go build -o $(PREFIX)/bin/$(BINARY) $(CMD)

##@ Clean
.PHONY: clean
clean: ## Remove build artifacts
	go clean ./...
	rm -f coverage.out coverage.html
