# Self-deploy targets. Every path below is one command from a fresh clone.
#
#   make run       serve locally on :8080
#   make deploy    build on Fly's builders and ship (no local Docker needed)
#   make verify    confirm the deployment answers
#   make connect   print the MCP connector config for Claude, Cursor, etc.
#
# Callers bring their own Monid API key in X-API-KEY, so the server holds
# no secret of its own; a deploy needs a Fly account and nothing else.

APP     ?= monid-finance-api
URL     ?= https://$(APP).fly.dev
PORT    ?= 8080
# The short commit sha, stamped into the binary and reported by /healthz,
# so `make verify` can tell whether the deployment is the current HEAD.
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
GOFLAGS ?= -trimpath -ldflags="-s -w -X main.version=$(VERSION)"

.PHONY: help build test vet fmt run docker deploy verify connect clean

help: ## list targets
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## compile the server binary to bin/server
	cd go && CGO_ENABLED=0 go build $(GOFLAGS) -o ../bin/server ./cmd/server

test: ## run the full suite under the race detector
	cd go && go test ./... -race -count=1

vet: ## gofmt check, go vet
	cd go && test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run: make fmt"; exit 1; }
	cd go && go vet ./...

fmt: ## gofmt every file
	cd go && gofmt -w .

run: ## serve locally on :$(PORT); pass your key as X-API-KEY on each request
	cd go && PORT=$(PORT) STATIC_DIR=../website ALLOWLIST_PATH=../docs/monid_finance_discovery.json go run ./cmd/server

docker: ## build the distroless image locally (needs a running Docker daemon)
	docker build -t $(APP) .

deploy: ## deploy to Fly.io, building remotely; first run creates the app
	@command -v flyctl >/dev/null || { echo "install flyctl: https://fly.io/docs/flyctl/install/"; exit 1; }
	@flyctl status -a $(APP) >/dev/null 2>&1 || flyctl launch --name $(APP) --copy-config --no-deploy --yes
	flyctl deploy --remote-only -a $(APP) --build-arg VERSION=$(VERSION)
	@$(MAKE) --no-print-directory verify

verify: ## check the deployment is up and built from the current commit
	@body=$$(curl -sf $(URL)/healthz) || { echo "healthz  unreachable at $(URL)"; exit 1; }; \
	live=$$(printf '%s' "$$body" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'); \
	printf 'healthz  %s\n' "$$body"; \
	if [ "$$live" = "$(VERSION)" ]; then echo "version  $$live matches HEAD"; \
	else echo "version  $$live, HEAD is $(VERSION): run make deploy"; exit 1; fi

connect: ## print MCP connector config for the deployed server
	@echo ""
	@echo "Claude Code:"
	@echo "  claude mcp add --transport http monid-finance $(URL)/mcp --header \"X-API-KEY: <your-monid-key>\""
	@echo ""
	@echo "Any HTTP MCP client (Cursor, Windsurf, claude.ai custom connector):"
	@echo '  { "mcpServers": { "monid-finance": {'
	@echo '      "url": "$(URL)/mcp",'
	@echo '      "headers": { "X-API-KEY": "<your-monid-key>" } } } }'
	@echo ""
	@echo "REST, same key:"
	@echo "  curl -H 'X-API-KEY: <your-monid-key>' '$(URL)/financials/income-statements?ticker=AAPL&period=annual&limit=1'"
	@echo ""
	@echo "Get a key at https://monid.ai. Usage bills your own wallet; the server stores nothing."

clean: ## remove build output
	rm -rf bin
