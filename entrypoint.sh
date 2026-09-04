#!/bin/sh
# Boots both processes in one container:
#   1. Optionally registers MONID_API_KEY with the monid CLI.
#   2. Starts uvicorn (Python FD-compatible API) in the background on
#      127.0.0.1:8000.
#   3. Waits for it to answer.
#   4. execs the Go gateway in the foreground on $PORT so it receives signals
#      (SIGTERM/SIGINT) directly from the container runtime.
set -eu

log() { printf '[entrypoint] %s\n' "$1" >&2; }

PORT="${PORT:-8080}"
UPSTREAM_HOST=127.0.0.1
UPSTREAM_PORT=8000
READY_URL="http://${UPSTREAM_HOST}:${UPSTREAM_PORT}/openapi.json"
READY_TIMEOUT_SECONDS=30

# ---- monid CLI key registration -------------------------------------------
if [ -n "${MONID_API_KEY:-}" ]; then
    if ! command -v monid >/dev/null 2>&1; then
        log "FATAL: MONID_API_KEY is set but the 'monid' CLI is not installed in this image."
        log "Rebuild the image so the CLI is installed:"
        log "  --build-arg MONID_CLI_NPM_PACKAGE=@monid-ai/cli@0.1.7"
        log "See docs/deploy.md for details."
        exit 1
    fi

    log "registering MONID_API_KEY with the monid CLI"
    add_out=$(monid keys add --key "$MONID_API_KEY" --label fly 2>&1) && add_rc=0 || add_rc=$?
    if [ "$add_rc" -ne 0 ]; then
        case "$add_out" in
            *already*exist*|*already*registered*|*duplicate*)
                log "monid keys add: key/label already registered, continuing"
                ;;
            *)
                log "FATAL: 'monid keys add' failed:"
                printf '%s\n' "$add_out" >&2
                exit 1
                ;;
        esac
    fi

    # Some monid CLI versions activate the key automatically on add; others
    # require an explicit activate step. Try it, but do not fail startup if
    # the subcommand is unsupported or the key is already active.
    if activate_out=$(monid keys activate fly 2>&1); then
        log "activated monid key label 'fly'"
    else
        case "$activate_out" in
            *already*active*|*unknown*command*|"*no such*"|*not*found*)
                log "monid keys activate: skipped (${activate_out})"
                ;;
            *)
                log "WARNING: 'monid keys activate fly' failed, continuing anyway:"
                printf '%s\n' "$activate_out" >&2
                ;;
        esac
    fi
elif ! command -v monid >/dev/null 2>&1; then
    log "WARNING: the 'monid' CLI is not installed and MONID_API_KEY is unset."
    log "Live calls will fail once the Python service shells out to 'monid'."
fi

# ---- start the Python API --------------------------------------------------
log "starting uvicorn on ${UPSTREAM_HOST}:${UPSTREAM_PORT}"
uv run uvicorn monid_finance_mcp.rest_api:app --host "$UPSTREAM_HOST" --port "$UPSTREAM_PORT" &
uvicorn_pid=$!

cleanup() {
    kill "$uvicorn_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# ---- wait for readiness -----------------------------------------------------
log "waiting for the Python API to answer at ${READY_URL} (timeout ${READY_TIMEOUT_SECONDS}s)"
elapsed=0
until curl -fsS -o /dev/null "$READY_URL" 2>/dev/null; do
    if ! kill -0 "$uvicorn_pid" 2>/dev/null; then
        log "FATAL: uvicorn exited before becoming ready."
        wait "$uvicorn_pid" || true
        exit 1
    fi
    if [ "$elapsed" -ge "$READY_TIMEOUT_SECONDS" ]; then
        log "FATAL: Python API did not answer within ${READY_TIMEOUT_SECONDS}s."
        exit 1
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done
log "Python API is ready"

# ---- run the gateway in the foreground -------------------------------------
# Undo the EXIT trap's uvicorn-only cleanup: once the gateway execs into this
# shell's PID, the shell process is replaced, so the trap never has to
# re-fire for the gateway itself. uvicorn keeps running as a background child
# of the resulting process and receives the same signals via the process
# group when the container is stopped.
trap - EXIT
log "starting gateway on :${PORT}"
exec /app/gateway
