package httpapi

import (
	"net/http"
	"testing"
)

// Financial Datasets answers a path with or without its trailing slash,
// and clients written against them use the slashed form: ai-hedge-fund
// calls /prices/, /financial-metrics/, /news/, /insider-trades/ and
// /earnings/. Every one of those used to 404 here.
func TestTrailingSlashRoutesTheSameAsBare(t *testing.T) {
	for _, path := range []string{"/news", "/earnings", "/insider-trades", "/financial-metrics", "/filings/types"} {
		caller := newFakeCaller()
		rt := newTestRouter(caller, nil)
		bare := doGet(t, rt, path+"?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
		slashed := doGet(t, rt, path+"/?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
		if slashed.Code == http.StatusNotFound {
			t.Fatalf("%s/ answered 404 where %s answered %d", path, path, bare.Code)
		}
		if slashed.Code != bare.Code {
			t.Fatalf("%s/ = %d, %s = %d: a trailing slash must not change the answer",
				path, slashed.Code, path, bare.Code)
		}
	}
}
