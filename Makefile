# Moorpost — top-level Makefile
# All real work happens in cli/. This is just convenience wrappers.

.PHONY: build test test-race e2e cover install clean lint help

CLI := cli
BIN := moorpost

help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nMoorpost dev commands:\n\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the moorpost binary into cli/moorpost
	cd $(CLI) && go build -o $(BIN) .

test: ## Run unit tests
	cd $(CLI) && go test ./...

test-race: ## Run unit tests with -race
	cd $(CLI) && go test -race ./...

cover: ## Show test coverage per package
	cd $(CLI) && go test -cover ./...

e2e: ## Run real-GCP E2E tests (creates VMs; cost guardrails apply)
	cd $(CLI) && go test -v -tags=gcp_e2e -timeout=18m ./internal/provider/gcp/...

install: build ## Install the binary to /usr/local/bin
	install -m 0755 $(CLI)/$(BIN) /usr/local/bin/$(BIN)
	@echo "Installed: $$(/usr/local/bin/$(BIN) --version)"

lint: ## Run go vet
	cd $(CLI) && go vet ./...

clean: ## Remove built binary and test artifacts
	rm -f $(CLI)/$(BIN)
	cd $(CLI) && go clean ./...
