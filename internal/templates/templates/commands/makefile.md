Audit or create a Makefile facade for this project's native build system.

## Rules

1. **Discover** the native build system by checking for config files:
   - CMakeLists.txt -> CMake
   - Cargo.toml -> Cargo
   - go.mod -> Go
   - package.json -> Node (npm/yarn/pnpm)
   - meson.build -> Meson

2. **Read** any existing Makefile to preserve project-specific targets.

3. **Create or update** a Makefile with these standard targets:

   | Target           | Purpose                          | Safety    |
   | ---------------- | -------------------------------- | --------- |
   | make             | Print help (.DEFAULT_GOAL)       | read-only |
   | make build       | Default build                    | mutates   |
   | make test        | Unit tests (builds first)        | mutates   |
   | make integration | Integration tests (builds first) | mutates   |
   | make install     | Install to PREFIX=~/.local       | mutates   |
   | make clean       | Remove build artifacts           | mutates   |

   Key constraint: **bare make MUST show help and NEVER mutate.** Use .DEFAULT_GOAL := help.

4. **Validate** by running make and confirming it only prints help (exit 0, no build side effects).

## Self-documenting help target

Use the `##` comment + awk pattern for self-documenting targets. Every target
gets a `## description` comment. Use `##@` lines for section headers:

```makefile
.DEFAULT_GOAL := help

##@ General
.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
	@echo ""
	@echo "Quick start:  make build && make test"
```

Then annotate every target:

```makefile
##@ Build
build: ## Build the binary
	...

##@ Test
test: ## Run unit tests
	...

integration: ## Run integration tests
	...

##@ Install
install: build ## Build and install to PREFIX
	...

##@ Clean
clean: ## Remove build artifacts
	...
```

This produces organized, colored output automatically — no manual echo
statements to maintain. New targets are self-documenting just by adding
`## description` to the target line.

## Adaptation patterns

### CMake

```makefile
BUILD_DIR  ?= build
BUILD_TYPE ?= Release
PREFIX     ?= $(HOME)/.local
build:
	cmake -B $(BUILD_DIR) -G Ninja -DCMAKE_BUILD_TYPE=$(BUILD_TYPE) -DCMAKE_INSTALL_PREFIX=$(PREFIX)
	ninja -C $(BUILD_DIR)
test: build
	./$(BUILD_DIR)/*_tests "~[integration]~[benchmark]"
integration: build
	./$(BUILD_DIR)/*_tests "[integration]"
install: build
	cmake --install $(BUILD_DIR)
clean:
	rm -rf $(BUILD_DIR)
```

### Cargo

```makefile
build:
	cargo build --release
test:
	cargo test
integration:
	cargo test -- --ignored
install:
	cargo install --path .
clean:
	cargo clean
```

### Go

```makefile
build:
	go build ./...
test:
	go test ./...
integration:
	go test -tags=integration ./...
install:
	go install ./...
clean:
	go clean
```

### Node (npm)

```makefile
build:
	npm run build
test:
	npm test
install:
	npm ci
clean:
	rm -rf node_modules dist
```

### Meson

```makefile
BUILD_DIR ?= builddir
build:
	meson setup $(BUILD_DIR) --buildtype=release
	ninja -C $(BUILD_DIR)
test: build
	meson test -C $(BUILD_DIR)
install: build
	meson install -C $(BUILD_DIR)
clean:
	rm -rf $(BUILD_DIR)
```
