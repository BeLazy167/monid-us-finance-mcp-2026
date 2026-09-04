# syntax=docker/dockerfile:1

# ---- Stage 1: build the Go edge gateway -----------------------------------
FROM golang:1.22-alpine AS gateway-build
WORKDIR /src/gateway
COPY gateway/go.mod ./
RUN go mod download 2>/dev/null || true
COPY gateway/ ./
RUN CGO_ENABLED=0 go build -o /out/gateway .

# ---- Stage 2: Python service + gateway binary + monid CLI ------------------
FROM python:3.12-slim AS runtime

# ca-certificates: outbound HTTPS from uvicorn/httpx and the monid CLI.
# curl: container-local health probing in entrypoint.sh.
# nodejs/npm: the monid CLI is a Node package (@monid-ai/cli).
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl nodejs npm \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir uv

WORKDIR /app

# Python dependency install, cached separately from source changes.
COPY pyproject.toml uv.lock README.md ./
COPY src/ ./src
RUN uv sync --frozen --no-dev

COPY docs/ ./docs
COPY website/ ./website

# --- monid CLI -------------------------------------------------------------
# The CLI ships as the public scoped npm package @monid-ai/cli (MIT, bin
# "monid"). MONID_CLI_NPM_PACKAGE lets a deploy pin a different version or
# swap the source without editing this file.
ARG MONID_CLI_NPM_PACKAGE="@monid-ai/cli@0.1.7"
RUN npm i -g "$MONID_CLI_NPM_PACKAGE" && monid --version

# Gateway binary from stage 1.
COPY --from=gateway-build /out/gateway /app/gateway

COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

ENV PORT=8080
ENV UPSTREAM=http://127.0.0.1:8000
ENV WEBSITE_ROOT=/app/website
ENV MONID_ALLOWLIST_PATH=/app/docs/monid_finance_discovery.json

EXPOSE 8080

ENTRYPOINT ["/app/entrypoint.sh"]
