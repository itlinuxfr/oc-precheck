BINARY   := oc-precheck
MODULE   := github.com/itlinuxfr/oc-precheck
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
PREFIX   ?= $(HOME)/.local/bin

# Static binaries: they run on RHEL 8/9 and any Linux without dependency.
export CGO_ENABLED := 0

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build bin/oc-precheck for the current platform
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/oc-precheck

.PHONY: plugin-install
plugin-install: build ## Install as oc / kubectl plugin into $(PREFIX)
	install -d $(PREFIX)
	install -m 0755 bin/$(BINARY) $(PREFIX)/$(BINARY)
	ln -sf $(PREFIX)/$(BINARY) $(PREFIX)/kubectl-precheck
	@echo "Installed: try 'oc precheck --help' (make sure $(PREFIX) is in your PATH)"

.PHONY: cross
cross: ## Build binaries for every platform into dist/
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/$(BINARY)_$${os}_$${arch}$$ext ./cmd/oc-precheck || exit 1; \
	done

.PHONY: test
test: ## Run the unit tests
	go test ./...

.PHONY: cover
cover: ## Run the tests with the race detector and coverage
	CGO_ENABLED=1 go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format the code
	gofmt -s -w .

.PHONY: lint
lint: ## Run golangci-lint (https://golangci-lint.run)
	golangci-lint run

.PHONY: docs
docs: ## Regenerate docs/cli, docs/rules.md and docs/default-config.yaml
	rm -rf docs/cli
	go run ./cmd/oc-precheck gen-docs --dir docs

.PHONY: docs-check
docs-check: docs ## Fail when the generated documentation is not up to date
	git diff --exit-code -- docs

.PHONY: release-snapshot
release-snapshot: ## Build release archives locally with GoReleaser (no publish)
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out
