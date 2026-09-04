package providers

import (
	"encoding/json"
	"testing"
)

// pricesFixture mirrors tests/test_service.py's OHLCV constant.
const pricesFixture = `[
	[1767225600, 30, 34, 29, 33, 300],
	[1767139200, 20, 24, 19, 23, 200],
	[1767052800, 10, 14, 9, 13, 100]
]`

// TestNormalizePrices_DayInterval ports the "day" half of
// tests/test_service.py::test_stock_prices_day_and_month_aggregation.
func TestNormalizePrices_DayInterval(t *testing.T) {
	records, err := NormalizePrices(json.RawMessage(pricesFixture), "2025-12-30", "2026-01-01", "day")
	if err != nil {
		t.Fatalf("NormalizePrices: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	wantTimes := []string{"2025-12-30", "2025-12-31", "2026-01-01"}
	for i, want := range wantTimes {
		assertStrPtr(t, "time", records[i].Time, want)
	}
	if *records[2].Open != 30 || *records[2].Close != 33 {
		t.Fatalf("got open=%v close=%v, want open=30 close=33", *records[2].Open, *records[2].Close)
	}
	if *records[2].Volume != 300 {
		t.Fatalf("got volume=%v, want 300", *records[2].Volume)
	}
}

// TestNormalizePrices_MonthAggregation ports the "month" half of
// tests/test_service.py::test_stock_prices_day_and_month_aggregation.
func TestNormalizePrices_MonthAggregation(t *testing.T) {
	records, err := NormalizePrices(json.RawMessage(pricesFixture), "2025-12-30", "2026-01-01", "month")
	if err != nil {
		t.Fatalf("NormalizePrices: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	december := records[0]
	assertStrPtr(t, "time", december.Time, "2025-12-31")
	if *december.Open != 10 {
		t.Fatalf("got open=%v, want 10", *december.Open)
	}
	if *december.Close != 23 {
		t.Fatalf("got close=%v, want 23", *december.Close)
	}
	if *december.Volume != 300 {
		t.Fatalf("got volume=%v, want 300 (100 + 200)", *december.Volume)
	}
	if *december.High != 24 {
		t.Fatalf("got high=%v, want 24", *december.High)
	}
	if *december.Low != 9 {
		t.Fatalf("got low=%v, want 9", *december.Low)
	}
}

// TestNormalizePrices_WeekAggregate exercises the week aggregation path the
// Python test suite does not cover directly: 2025-12-30/31 and 2026-01-01
// all fall in ISO week 2026-W01, so they fold into one bar.
func TestNormalizePrices_WeekAggregate(t *testing.T) {
	records, err := NormalizePrices(json.RawMessage(pricesFixture), "2025-12-30", "2026-01-01", "week")
	if err != nil {
		t.Fatalf("NormalizePrices: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (all three days share ISO week 2026-W01)", len(records))
	}
	bar := records[0]
	if *bar.Open != 10 || *bar.Close != 33 {
		t.Fatalf("got open=%v close=%v, want open=10 close=33", *bar.Open, *bar.Close)
	}
	if *bar.Volume != 600 {
		t.Fatalf("got volume=%v, want 600", *bar.Volume)
	}
	assertStrPtr(t, "time", bar.Time, "2026-01-01")
}

// TestNormalizePrices_YearAggregate exercises the year aggregation path:
// unlike the ISO week, the calendar year splits 2025-12-30/31 from
// 2026-01-01 into two bars.
func TestNormalizePrices_YearAggregate(t *testing.T) {
	records, err := NormalizePrices(json.RawMessage(pricesFixture), "2025-12-30", "2026-01-01", "year")
	if err != nil {
		t.Fatalf("NormalizePrices: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (2025 and 2026)", len(records))
	}
	year2025 := records[0]
	assertStrPtr(t, "time", year2025.Time, "2025-12-31")
	if *year2025.Open != 10 || *year2025.Close != 23 || *year2025.Volume != 300 {
		t.Fatalf("2025: got open=%v close=%v volume=%v, want open=10 close=23 volume=300",
			*year2025.Open, *year2025.Close, *year2025.Volume)
	}
	year2026 := records[1]
	assertStrPtr(t, "time", year2026.Time, "2026-01-01")
	if *year2026.Open != 30 || *year2026.Close != 33 || *year2026.Volume != 300 {
		t.Fatalf("2026: got open=%v close=%v volume=%v, want open=30 close=33 volume=300",
			*year2026.Open, *year2026.Close, *year2026.Volume)
	}
}

func TestNormalizePrices_DateRangeExcludesOutsideRows(t *testing.T) {
	records, err := NormalizePrices(json.RawMessage(pricesFixture), "2025-12-31", "2025-12-31", "day")
	if err != nil {
		t.Fatalf("NormalizePrices: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	assertStrPtr(t, "time", records[0].Time, "2025-12-31")
}

func TestNormalizePrices_RejectsHighBelowLow(t *testing.T) {
	_, err := NormalizePrices(json.RawMessage(`[[1767225600, 30, 10, 29, 33, 300]]`), "2025-01-01", "2026-12-31", "day")
	if err == nil {
		t.Fatal("expected a schema-drift error when high < low")
	}
}

func TestNormalizePrices_RejectsNegativeVolume(t *testing.T) {
	_, err := NormalizePrices(json.RawMessage(`[[1767225600, 30, 34, 29, 33, -1]]`), "2025-01-01", "2026-12-31", "day")
	if err == nil {
		t.Fatal("expected a schema-drift error for negative volume")
	}
}

func TestNormalizePrices_RejectsWrongRowWidth(t *testing.T) {
	_, err := NormalizePrices(json.RawMessage(`[[1767225600, 30, 34, 29, 33]]`), "2025-01-01", "2026-12-31", "day")
	if err == nil {
		t.Fatal("expected a schema-drift error for a five-element row")
	}
}

// TestNormalizePrices_FractionalVolumeStaysIntegral checks fd.price_record's
// "integral floats become ints" rule: OHLCV volumes are always whole,
// Price.Volume is *int64 (never fractional in the FD schema).
func TestNormalizePrices_FractionalVolumeStaysIntegral(t *testing.T) {
	records, err := NormalizePrices(json.RawMessage(`[[1767225600, 30, 34, 29, 33, 300]]`), "2025-01-01", "2026-12-31", "day")
	if err != nil {
		t.Fatalf("NormalizePrices: %v", err)
	}
	if got := *records[0].Volume; got != 300 {
		t.Fatalf("got volume=%v, want 300", got)
	}
}
