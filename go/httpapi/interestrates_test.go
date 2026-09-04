package httpapi

import (
	"net/http"
	"testing"

	"github.com/belazy/monid-finance/fd"
)

func rateRecord(bank string, rate float64) fd.InterestRate {
	return fd.InterestRate{Bank: &bank, Rate: &rate}
}

func TestInterestRates_BankFilterAndNoHistory(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_interest_rates"] = Result{
		Value:      []any{rateRecord("FED", 3.625), rateRecord("ECB", 2.25)},
		WrapperKey: "interest_rates",
	}
	rt := newTestRouter(caller, nil)

	rec := doGet(t, rt, "/macro/interest-rates?bank=ecb", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	rates := decodeBody(t, rec)["interest_rates"].([]any)
	if len(rates) != 1 || rates[0].(map[string]any)["bank"] != "ECB" {
		t.Fatalf("bank=ecb must keep only the ECB row, got %v", rates)
	}

	rec = doGet(t, rt, "/macro/interest-rates?start_date=2025-01-01", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != "bad_request" {
		t.Fatalf("a date range must be refused, not answered with a snapshot: %d %s", rec.Code, rec.Body.String())
	}
}
