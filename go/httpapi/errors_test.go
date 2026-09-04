package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/belazy/monid-finance/monid"
	"github.com/belazy/monid-finance/providers"
)

// TestClassifyError_MappingTable exercises every row of the contract's
// error-mapping table, plus the always-a-two-key-body guarantee.
func TestClassifyError_MappingTable(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "InputError",
			err:        &providers.InputError{Msg: "ticker is required"},
			wantStatus: http.StatusBadRequest,
			wantCode:   "bad_request",
			wantMsg:    "ticker is required",
		},
		{
			name:       "UnsupportedError",
			err:        &providers.UnsupportedError{Msg: "as_reported is not supported"},
			wantStatus: http.StatusBadRequest,
			wantCode:   "unsupported",
			wantMsg:    "as_reported is not supported",
		},
		{
			name:       "SchemaDriftError",
			err:        &providers.SchemaDriftError{Msg: "unexpected shape"},
			wantStatus: http.StatusBadGateway,
			wantCode:   "upstream_schema_changed",
			wantMsg:    "unexpected shape",
		},
		{
			name:       "RunError wrapping ErrUnauthorized",
			err:        &monid.RunError{Kind: monid.ErrUnauthorized, Message: "monid: invalid or unauthorized API key"},
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
			wantMsg:    "Invalid Monid API key.",
		},
		{
			name:       "RunError wrapping ErrBlocked",
			err:        &monid.RunError{Kind: monid.ErrBlocked, Message: "monid: wallet balance is insufficient for this run"},
			wantStatus: http.StatusPaymentRequired,
			wantCode:   "payment_required",
			wantMsg:    "monid: wallet balance is insufficient for this run",
		},
		{
			name:       "RunError wrapping ErrTimeout",
			err:        &monid.RunError{Kind: monid.ErrTimeout, Message: "monid: run timed out"},
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "upstream_timeout",
			wantMsg:    "monid: run timed out",
		},
		{
			name:       "RunError wrapping some other kind",
			err:        &monid.RunError{Kind: monid.ErrSchema, Message: "monid: unexpected response shape"},
			wantStatus: http.StatusBadGateway,
			wantCode:   "upstream_error",
			wantMsg:    "monid: unexpected response shape",
		},
		{
			name:       "unclassified error",
			err:        errors.New("boom"),
			wantStatus: http.StatusBadGateway,
			wantCode:   "upstream_error",
			wantMsg:    "boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, message := classifyError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if message != tc.wantMsg {
				t.Errorf("message = %q, want %q", message, tc.wantMsg)
			}
		})
	}
}

// TestClassifyError_WrappedErrorsStillClassify proves classification uses
// errors.As/errors.Is, not string matching: a *monid.RunError wrapped by
// fmt.Errorf("%w", ...) still maps correctly.
func TestClassifyError_WrappedErrorsStillClassify(t *testing.T) {
	inner := &monid.RunError{Kind: monid.ErrBlocked, Message: "blocked"}
	wrapped := errorsJoinWrap(inner)
	status, code, _ := classifyError(wrapped)
	if status != http.StatusPaymentRequired || code != "payment_required" {
		t.Fatalf("wrapped RunError classified as (%d, %q), want (402, payment_required)", status, code)
	}
}

func errorsJoinWrap(err error) error {
	return &wrapError{err: err}
}

type wrapError struct{ err error }

func (w *wrapError) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapError) Unwrap() error { return w.err }

// TestWriteServiceError_AlwaysTwoKeyBody proves every error path emits the
// FD ErrorResponse shape ({"error", "message"}) on the wire.
func TestWriteServiceError_AlwaysTwoKeyBody(t *testing.T) {
	rec := httptest.NewRecorder()
	writeServiceError(rec, errors.New("network exploded"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("body has %d keys, want exactly 2 (error, message): %#v", len(body), body)
	}
	if body["error"] != "upstream_error" {
		t.Fatalf("error = %v, want upstream_error", body["error"])
	}
}
