package providers

import (
	"encoding/json"
	"testing"
)

// insiderFixture mirrors tests/test_gate_b.py's INSIDER constant.
const insiderFixture = `{
	"status": "success",
	"data": {
		"query": "AAPL",
		"results": [
			{
				"transaction_date": "2026-09-01 Sale",
				"reported_datetime": "2026-09-03 6:30 pm",
				"company": "Apple Inc.",
				"symbol": "AAPL",
				"insider_relationship": "Newstead Jennifer SVP, GC",
				"shares_traded": "1,439",
				"average_price": "$317.01",
				"total_amount": "$456,177",
				"shares_owned": "35,790 (Direct)",
				"filing": "View",
				"filing_url": "https://www.secform4.com/filings/1.htm",
				"symbol_url": "https://www.secform4.com/#S2",
				"insider_relationship_url": "https://www.secform4.com/insider/1.htm"
			},
			{
				"transaction_date": "2026-08-25 Purchase",
				"reported_datetime": "2026-08-27 6:31 pm",
				"company": "Apple Inc.",
				"symbol": "AAPL",
				"insider_relationship": "Cook Timothy CEO",
				"shares_traded": "20,000",
				"average_price": "$300.00",
				"total_amount": "$6,000,000",
				"shares_owned": "3,000,000 (Indirect)",
				"filing": "View",
				"filing_url": "https://www.secform4.com/filings/2.htm",
				"symbol_url": "https://www.secform4.com/#S2",
				"insider_relationship_url": "https://www.secform4.com/insider/2.htm"
			}
		]
	}
}`

// TestNormalizeInsiderTrades_MatchesFDContract ports
// tests/test_gate_b.py::test_get_insider_trades_matches_fd_contract.
func TestNormalizeInsiderTrades_MatchesFDContract(t *testing.T) {
	records, err := NormalizeInsiderTrades(json.RawMessage(insiderFixture), "AAPL", 10, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NormalizeInsiderTrades: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	newest := records[0]
	assertStrPtr(t, "ticker", newest.Ticker, "AAPL")
	assertStrPtr(t, "issuer", newest.Issuer, "Apple Inc.")
	assertStrPtr(t, "name", newest.Name, "Newstead Jennifer SVP, GC")
	assertStrPtr(t, "filing_date", newest.FilingDate, "2026-09-03")
	assertStrPtr(t, "transaction_date", newest.TransactionDate, "2026-09-01")
	assertStrPtr(t, "transaction_type", newest.TransactionType, "Sale")
	if *newest.TransactionShares != 1439 {
		t.Fatalf("got transaction_shares=%v, want 1439", *newest.TransactionShares)
	}
	if got := *newest.TransactionPricePerShare; got < 317.0099 || got > 317.0101 {
		t.Fatalf("got transaction_price_per_share=%v, want ~317.01", got)
	}
	if *newest.TransactionValue != 456177 {
		t.Fatalf("got transaction_value=%v, want 456177", *newest.TransactionValue)
	}
	if *newest.SharesOwnedAfterTransaction != 35790 {
		t.Fatalf("got shares_owned_after_transaction=%v, want 35790", *newest.SharesOwnedAfterTransaction)
	}
	// Fields the validated SECForm4 route cannot source stay unsourced.
	if newest.Title != nil || newest.IsBoardDirector != nil || newest.FormType != nil ||
		newest.ReportPeriod != nil || newest.TransactionCode != nil ||
		newest.SharesOwnedBeforeTransaction != nil || newest.SecurityTitle != nil {
		t.Fatal("unsourced InsiderTrade fields must stay nil, never fabricated")
	}
}

// TestNormalizeInsiderTrades_Filters ports
// tests/test_gate_b.py::test_get_insider_trades_filters_and_rejections.
func TestNormalizeInsiderTrades_Filters(t *testing.T) {
	name := "cook"
	transactionType := "purchase"
	records, err := NormalizeInsiderTrades(json.RawMessage(insiderFixture), "AAPL", 10, &name, &transactionType, nil, nil, nil)
	if err != nil {
		t.Fatalf("NormalizeInsiderTrades: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	assertStrPtr(t, "name", records[0].Name, "Cook Timothy CEO")
}

func TestNormalizeInsiderTrades_FilingDateExactAndRange(t *testing.T) {
	exact := "2026-09-03"
	records, err := NormalizeInsiderTrades(json.RawMessage(insiderFixture), "AAPL", 10, nil, nil, &exact, nil, nil)
	if err != nil {
		t.Fatalf("NormalizeInsiderTrades: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	assertStrPtr(t, "filing_date", records[0].FilingDate, "2026-09-03")

	gte := "2026-09-01"
	records, err = NormalizeInsiderTrades(json.RawMessage(insiderFixture), "AAPL", 10, nil, nil, nil, &gte, nil)
	if err != nil {
		t.Fatalf("NormalizeInsiderTrades: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (only the Sep 3 filing is >= Sep 1)", len(records))
	}

	lte := "2026-08-31"
	records, err = NormalizeInsiderTrades(json.RawMessage(insiderFixture), "AAPL", 10, nil, nil, nil, nil, &lte)
	if err != nil {
		t.Fatalf("NormalizeInsiderTrades: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (only the Aug 27 filing is <= Aug 31)", len(records))
	}
	assertStrPtr(t, "name", records[0].Name, "Cook Timothy CEO")
}

func TestNormalizeInsiderTrades_Limit(t *testing.T) {
	records, err := NormalizeInsiderTrades(json.RawMessage(insiderFixture), "AAPL", 1, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NormalizeInsiderTrades: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	assertStrPtr(t, "name", records[0].Name, "Newstead Jennifer SVP, GC")
}

func TestNormalizeInsiderTrades_RejectsTooManyRows(t *testing.T) {
	results := make([]map[string]any, 0, 16)
	for i := 0; i < 16; i++ {
		results = append(results, map[string]any{
			"transaction_date":     "2026-09-01 Sale",
			"reported_datetime":    "2026-09-03 6:30 pm",
			"company":              "Apple Inc.",
			"symbol":               "AAPL",
			"insider_relationship": "Someone",
			"shares_traded":        "1",
			"average_price":        "$1.00",
			"total_amount":         "$1.00",
			"shares_owned":         "1 (Direct)",
		})
	}
	raw, err := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"query": "AAPL", "results": results},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NormalizeInsiderTrades(json.RawMessage(raw), "AAPL", 10, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected a schema-drift error for more than 15 rows")
	}
}

func TestNormalizeInsiderTrades_RejectsSymbolMismatch(t *testing.T) {
	raw := `{"status":"success","data":{"query":"AAPL","results":[{
		"transaction_date":"2026-09-01 Sale","reported_datetime":"2026-09-03 6:30 pm",
		"company":"Apple Inc.","symbol":"MSFT","insider_relationship":"Someone",
		"shares_traded":"1","average_price":"$1.00","total_amount":"$1.00","shares_owned":"1 (Direct)"
	}]}}`
	_, err := NormalizeInsiderTrades(json.RawMessage(raw), "AAPL", 10, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected a schema-drift error when a result symbol does not match the ticker")
	}
}

func TestNormalizeInsiderTrades_RejectsNonSuccessStatus(t *testing.T) {
	_, err := NormalizeInsiderTrades(json.RawMessage(`{"status":"error","data":{}}`), "AAPL", 10, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected a schema-drift error for a non-success status")
	}
}
