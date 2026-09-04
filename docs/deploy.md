# Deploy

This repo ships as one Fly.io container (Go gateway + Python API) plus a
static Vercel deployment for `website/`.

## Architecture

- The Go gateway (`gateway/`) listens on `$PORT` (default `8080`), is the
  only publicly reachable process, and does auth, rate limiting, CORS,
  response caching, `/healthz`, and serving `website/`.
- It reverse-proxies API and MCP paths to the Python FD-compatible service
  (`uvicorn`), which listens on `127.0.0.1:8000` only.
- `entrypoint.sh` starts uvicorn in the background, waits for it to answer,
  then `exec`s the gateway in the foreground so container signals
  (`SIGTERM`) reach it directly.
- The Python service shells out to the `monid` CLI for every live call
  (see `src/monid_finance_mcp/client.py`).

## The monid CLI has no verified public package

`npm view monid version` returns 404 as of this writing, and no other
public package name was found. The Dockerfile does **not** guess a package
name. Instead, supply one of these at build time:

- `--build-arg MONID_CLI_NPM_PACKAGE=<https URL to a single linux/amd64 executable>`
  (e.g. a GitHub Releases asset), downloaded with `curl` and chmod +x'd to
  `/usr/local/bin/monid`.
- `--build-arg MONID_CLI_NPM_PACKAGE=<real npm package name>`, once known,
  installed with `npm i -g`.

If neither is set, the image builds successfully but ships without a
working `monid` binary. `entrypoint.sh` detects this at container start:
if `MONID_API_KEY` is set but `monid` is missing, it exits immediately with
a clear error instead of serving an API that cannot make live calls. Update
this file with the real install command once the CLI's distribution
channel is confirmed.

## Fly.io

### 1. Build with the real monid CLI source

```bash
fly launch --no-deploy   # first time only; creates the app, keep fly.toml as-is
fly deploy --build-arg MONID_CLI_NPM_PACKAGE="https://example.com/path/to/monid-linux-amd64"
# or, once published:
fly deploy --build-arg MONID_CLI_NPM_PACKAGE="@monid/cli"
```

### 2. Set secrets (never commit real values)

```bash
fly secrets set \
  MONID_API_KEY="monid_live_..." \
  GATEWAY_API_KEYS="key1,key2" \
  UPSTREAM_API_KEY="a-shared-secret-between-gateway-and-uvicorn" \
  CORS_ALLOWED_ORIGINS="https://your-project.vercel.app"
```

- `MONID_API_KEY`: registered with the `monid` CLI by `entrypoint.sh` on
  every boot (tolerates "already registered").
- `GATEWAY_API_KEYS`: comma-separated keys the gateway accepts on
  `X-API-KEY`. Leave unset to accept any non-empty key (demo mode).
- `UPSTREAM_API_KEY`: the key the gateway presents to uvicorn on behalf of
  callers, including keyless demo traffic. Must match what
  `_api_keys_from_env()` in `rest_api.py` allows (or leave both default to
  `gateway`).
- `CORS_ALLOWED_ORIGINS`: set to your Vercel domain(s) once known (see
  below). Leave unset during initial testing to reflect any origin.

Re-run `fly deploy` (with the same `--build-arg` as before, since build
args are not remembered across deploys) after changing secrets that affect
the image, or `fly deploy` alone if only runtime secrets changed — Fly
restarts machines to pick up new secrets.

### 3. Verify

```bash
fly status
curl -s https://monid-finance-api.fly.dev/healthz
# {"status":"ok"}

# Keyless demo route (DEMO_TICKERS defaults to AAPL,MSFT,NVDA):
curl -s "https://monid-finance-api.fly.dev/prices?ticker=AAPL"

# Keyed route:
curl -s -H "X-API-KEY: key1" \
  "https://monid-finance-api.fly.dev/financials/income-statements?ticker=AAPL"

# MCP JSON-RPC initialize:
curl -s -X POST https://monid-finance-api.fly.dev/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

## Vercel (static site)

Deploys `website/` only, with no build step. `vercel.json` (repo root) sets
`outputDirectory: "website"` and `buildCommand: ""`.

```bash
cd /path/to/repo/root   # vercel.json must be at the project root
vercel                  # first run: link/create the project, do NOT let it
                         # auto-detect a framework or build command
vercel --prod            # or: vercel deploy --prod
```

Once the Vercel domain is known, set it as `CORS_ALLOWED_ORIGINS` on the
Fly app (step 2 above) so browser calls from the marketing site to the API
are allowed, and re-run `fly deploy` or `fly secrets set` to apply it.

## Local equivalents

```bash
sh -n entrypoint.sh                 # shell syntax check
(cd gateway && go build ./...)      # gateway compiles
docker build -t monid-finance-api . # full image (needs a running daemon)
```
