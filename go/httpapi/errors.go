// Package httpapi is the single Go HTTP surface for the Monid Finance
// server: Financial Datasets-identical REST routes, the MCP transport
// mounted at /mcp and /api, and the edge concerns (auth, cache, rate limit,
// static site) that used to live in a separate proxy. Everything runs
// in-process; there is no proxy hop.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/belazy/monid-finance/monid"
	"github.com/belazy/monid-finance/providers"
)

// fdErrorBody is the Financial Datasets ErrorResponse shape, emitted by
// every error path in this package: {"error": code, "message": text}.
type fdErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// writeFDError writes an FD-shaped error body with the given HTTP status.
func writeFDError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(fdErrorBody{Error: code, Message: message})
}

// classifyError maps an error returned by Caller.Call to an HTTP status and
// FD error code. Classification is always by type (errors.As / errors.Is),
// never by matching an error string, so wrapping does not break routing.
func classifyError(err error) (status int, code string, message string) {
	var inputErr *providers.InputError
	if errors.As(err, &inputErr) {
		return http.StatusBadRequest, "bad_request", inputErr.Error()
	}

	var unsupportedErr *providers.UnsupportedError
	if errors.As(err, &unsupportedErr) {
		return http.StatusBadRequest, "unsupported", unsupportedErr.Error()
	}

	var schemaErr *providers.SchemaDriftError
	if errors.As(err, &schemaErr) {
		return http.StatusBadGateway, "upstream_schema_changed", schemaErr.Error()
	}

	var runErr *monid.RunError
	if errors.As(err, &runErr) {
		switch {
		case errors.Is(runErr, monid.ErrUnauthorized):
			return http.StatusUnauthorized, "unauthorized", "Invalid Monid API key."
		case errors.Is(runErr, monid.ErrBlocked):
			return http.StatusPaymentRequired, "payment_required", runErr.Error()
		case errors.Is(runErr, monid.ErrTimeout):
			return http.StatusGatewayTimeout, "upstream_timeout", runErr.Error()
		default:
			return http.StatusBadGateway, "upstream_error", runErr.Error()
		}
	}

	// Anything else (including an unwrapped monid error, which should not
	// happen in practice) is an honest, non-specific upstream failure.
	return http.StatusBadGateway, "upstream_error", err.Error()
}

// writeServiceError classifies err and writes the matching FD error body.
func writeServiceError(w http.ResponseWriter, err error) {
	status, code, message := classifyError(err)
	writeFDError(w, status, code, message)
}
