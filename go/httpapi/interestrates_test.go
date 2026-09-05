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

	// The window is forwarded to the tool now that a history is sourced.
	rec = doGet(t, rt, "/macro/interest-rates?start_date=2025-01-01&end_date=2025-06-01",
		map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("a date range must be accepted: %d %s", rec.Code, rec.Body.String())
	}
	last := caller.lastCall()
	if last.args["start_date"] != "2025-01-01" || last.args["end_date"] != "2025-06-01" {
		t.Fatalf("the window was not forwarded: %#v", last.args)
	}
}
