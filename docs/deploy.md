# Deploy

This repo ships as one Go binary (Fly.io) plus a static Vercel deployment
for `website/`.

## Architecture

- `go/cmd/server` builds a single static binary that is the only publicly
  reachable process: it serves the Financial Datasets-identical REST
  routes, the MCP transport (mounted at both `/mcp` and `/api`), `/healthz`,
  and the static site — all in-process, with no reverse-proxy hop to a
  separate backend.
- Every call runs on the **caller's own Monid API key** (`X-API-KEY`),
  forwarded straight to `monid.Client.Run`: the caller's Monid wallet pays
  for the caller's own usage. The server holds no funded Monid key of its
  own (except the optional keyless demo key, below) and never logs or
  stores a caller's key.
- `go/service` implements the 27 Financial Datasets tools once and shares
  them between REST and MCP; `go/httpapi` owns routing, auth, response
  caching, rate limiting, CORS, and the static site.

## Fly.io

### 1. Build and deploy

```bash
fly launch --no-deploy   # first time only; creates the app, keep fly.toml as-is
fly deploy
```

The Dockerfile builds `go/cmd/server` with `CGO_ENABLED=0` into a
distroless image; there is no separate runtime dependency (no Python, no
external CLI) to install.

### 2. Configure (optional — sane defaults ship in `fly.toml`)

```bash
fly secrets set \
  API_KEYS="key1,key2" \
  DEMO_MONID_API_KEY="monid_live_..." \
  CORS_ALLOWED_ORIGINS="https://your-own-frontend.example"
```

- `API_KEYS`: optional comma-separated restriction on which caller-supplied
  `X-API-KEY` values may call this server at all. This is **not** a shared
  backend key — every accepted key is still the caller's own Monid key,
  used to bill that caller's wallet. Leave unset to accept any well-formed,
  non-empty key (the normal bring-your-own-key mode).
- `DEMO_MONID_API_KEY`: optional. When set, keyless `GET` requests for the
  instant-tryout tickers (`AAPL`, `MSFT`, `NVDA`) are served using this
  operator-funded key, under a separate, stricter rate-limit bucket. Leave
  unset to require a key for every request (keyless requests get 401).
- `CORS_ALLOWED_ORIGINS`: set to your Vercel domain(s) once known. Leave
  unset during initial testing to reflect any origin.
- `PORT` (default `8080`), `STATIC_DIR` (default `website`),
  `ALLOWLIST_PATH` (default `docs/monid_finance_discovery.json`), and
  `RATE_LIMIT_PER_MINUTE` (default `60`) are already set in `fly.toml`.

`fly deploy` restarts machines to pick up new secrets; no build args are
needed since there is nothing to inject at build time anymore.

### 3. Verify

```bash
fly status
curl -s https://monid-finance-api.fly.dev/healthz
# {"status":"ok"}

# Bring your own Monid API key — get one at https://monid.ai:
curl -s -H "X-API-KEY: <your-monid-api-key>" \
  "https://monid-finance-api.fly.dev/financials/income-statements?ticker=AAPL"

# Keyless demo route (only answers if DEMO_MONID_API_KEY is set; demo
# tickers are AAPL, MSFT, NVDA):
curl -s "https://monid-finance-api.fly.dev/prices?ticker=AAPL"

# MCP JSON-RPC initialize:
curl -s -X POST https://monid-finance-api.fly.dev/mcp \
  -H "Content-Type: application/json" \
  -H "X-API-KEY: <your-monid-api-key>" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

## Static pages

`website/` holds two files and is served by the Go binary itself from
`STATIC_DIR`: the root page and the head-to-head comparison at `/kill.html`,
with its dataset. There is no separate static host and no build step.

Documentation lives at https://ripfinancialdatasets.mintlify.app, built by
Mintlify from `docs-site/` on every push to `main`.

## Local equivalents

```bash
cd go && go build ./... && go vet ./... && go test ./...   # everything compiles and passes
go run ./cmd/server                                          # serve locally on :8080
docker build -t monid-finance-api .                          # full image (needs a running daemon)
```
