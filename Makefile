# Moorpost — top-level Makefile
# All real work happens in cli/. This is just convenience wrappers.

.PHONY: build test test-race e2e e2e-autostop smoke cover install clean lint release \
        extension-install extension-build extension-package help

# Default GCP project for E2E tests; override with: make e2e-autostop GCP_PROJECT=...
GCP_PROJECT ?= example-project

CLI := cli
BIN := moorpost
EXT := extension
VERSION_PKG := github.com/latent-advisory/moorpost/cli/internal/version

# Build-time version metadata. `git describe` returns the most recent
# tag if HEAD is on it (e.g. v0.1.0); otherwise <tag>-<n>-g<sha>.
VERSION ?= $(shell git -C $(CLI)/.. describe --tags --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git -C $(CLI)/.. rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) \
           -X $(VERSION_PKG).Commit=$(COMMIT) \
           -X $(VERSION_PKG).Date=$(DATE)

help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nMoorpost dev commands:\n\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the moorpost binary into cli/moorpost (with version injected)
	cd $(CLI) && go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test: ## Run unit tests
	cd $(CLI) && go test ./...

test-race: ## Run unit tests with -race
	cd $(CLI) && go test -race ./...

cover: ## Show test coverage per package
	cd $(CLI) && go test -cover ./...

e2e: ## Run real-GCP E2E tests (creates VMs; cost guardrails apply)
	cd $(CLI) && go test -v -tags=gcp_e2e -timeout=18m ./internal/provider/gcp/...

smoke: build test-race lint extension-build ## Pre-tag gate (no GCP): build + test-race + lint + extension-build
	@echo "✓ smoke gate passed"

e2e-autostop: ## Run only the persistent-mode auto-stop E2E with pre-flight orphan check (~$0.005)
	@echo "Pre-flight: checking for orphan moorpost-test VMs in $(GCP_PROJECT)..."
	@orphans="$$(gcloud compute instances list --project=$(GCP_PROJECT) \
	    --filter=tags.items:moorpost-test --format='value(name)')"; \
	if [ -n "$$orphans" ]; then \
	    echo "ABORT: orphan moorpost-test VMs found:"; echo "$$orphans"; \
	    echo "Destroy them first with: gcloud compute instances delete <name> --project=$(GCP_PROJECT) --zone=<zone> --quiet"; \
	    exit 1; \
	fi
	@echo "Pre-flight ok. Running auto-stop E2E (15-25 min wall-clock; ~\$$0.005)..."
	cd $(CLI) && MOORPOST_E2E_PROJECT=$(GCP_PROJECT) go test -v -tags=gcp_e2e \
	    -run=TestGCPPersistentAutoStop_E2E -timeout=30m \
	    ./internal/provider/gcp/...

install: build ## Install the binary to /usr/local/bin
	install -m 0755 $(CLI)/$(BIN) /usr/local/bin/$(BIN)
	@echo "Installed: $$(/usr/local/bin/$(BIN) --version)"

lint: ## Run go vet
	cd $(CLI) && go vet ./...

extension-install: ## Install the VSCode extension's npm deps
	cd $(EXT) && npm install

extension-build: ## Bundle the VSCode extension into dist/extension.js
	cd $(EXT) && npm run build

extension-package: extension-build ## Produce a .vsix package
	cd $(EXT) && npm run package

clean: ## Remove built binary and test artifacts
	rm -f $(CLI)/$(BIN)
	rm -rf dist/
	rm -rf $(EXT)/dist $(EXT)/out $(EXT)/*.vsix
	cd $(CLI) && go clean ./...

release: ## Cross-compile binaries for darwin/linux × arm64/amd64 into dist/
	@mkdir -p dist
	@echo "Building $(VERSION) for 4 targets..."
	@for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do \
		os=$${target%-*}; arch=$${target#*-}; \
		out=dist/$(BIN)-$$os-$$arch; \
		echo "  → $$out"; \
		(cd $(CLI) && GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o ../$$out .) || exit 1; \
	done
	@echo "Computing SHA256SUMS..."
	@cd dist && shasum -a 256 $(BIN)-* > SHA256SUMS && cat SHA256SUMS
	@echo "Done. Artifacts in dist/."
