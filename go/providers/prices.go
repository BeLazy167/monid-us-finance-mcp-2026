// This file ports normalize.normalize_prices + fd.price_record: turning a
// DefiLlama /equities/v1/ohlcv payload ([[unix, open, high, low, close,
// volume], ...]) into ascending FD Price records, with local week/month/year
// aggregation.
package providers

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/belazy/monid-finance/fd"
)

// dailyBar is one validated OHLCV bar keyed to its UTC calendar day.
type dailyBar struct {
	ts                             int64
	date                           string // this bar's own day, YYYY-MM-DD
	endDate                        string // last day folded into this bar (== date until aggregated)
	open, high, low, close, volume float64
}

// extractOHLCVRows finds the row list nested under one of the DefiLlama
// OHLCV wrapper keys (or the payload itself, if it is already a list),
// mirroring normalize._extract_array_rows.
func extractOHLCVRows(raw json.RawMessage) ([][]any, error) {
	current, err := unmarshalAny(raw)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 5; i++ {
		if arr, ok := current.([]any); ok {
			rows := make([][]any, 0, len(arr))
			for index, item := range arr {
				row, ok := item.([]any)
				if !ok {
					return nil, schemaDriftf("OHLCV row %d is not an array", index)
				}
				rows = append(rows, row)
			}
			return rows, nil
		}
		obj, ok := current.(map[string]any)
		if !ok {
			break
		}
		var next any
		for _, key := range []string{"ohlcv", "candles", "prices", "data"} {
			if child, exists := obj[key]; exists {
				switch child.(type) {
				case []any, map[string]any:
					next = child
				}
			}
			if next != nil {
				break
			}
		}
		if next == nil {
			break
		}
		current = next
	}
	return nil, schemaDriftf("DefiLlama payload omitted OHLCV rows")
}

func ohlcvNumber(v any, index int, name string) (float64, error) {
	f, ok := numberValue(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, schemaDriftf("OHLCV row %d %s must be finite and numeric", index, name)
	}
	return f, nil
}

// parseDailyBars validates raw OHLCV rows and keeps only those whose UTC
// calendar day falls within [startDate, endDate] (both YYYY-MM-DD,
// inclusive), mirroring the row-validation half of normalize_prices.
func parseDailyBars(raw json.RawMessage, startDate, endDate string) ([]dailyBar, error) {
	rows, err := extractOHLCVRows(raw)
	if err != nil {
		return nil, err
	}
	daily := make([]dailyBar, 0, len(rows))
	for index, row := range rows {
		if len(row) != 6 {
			return nil, schemaDriftf("OHLCV row %d must contain six values", index)
		}
		tsFloat, ok := numberValue(row[0])
		if !ok {
			return nil, schemaDriftf("OHLCV row %d timestamp must be numeric", index)
		}
		open, err := ohlcvNumber(row[1], index, "open")
		if err != nil {
			return nil, err
		}
		high, err := ohlcvNumber(row[2], index, "high")
		if err != nil {
			return nil, err
		}
		low, err := ohlcvNumber(row[3], index, "low")
		if err != nil {
			return nil, err
		}
		close, err := ohlcvNumber(row[4], index, "close")
		if err != nil {
			return nil, err
		}
		volume, err := ohlcvNumber(row[5], index, "volume")
		if err != nil {
			return nil, err
		}
		if high < low {
			return nil, schemaDriftf("OHLCV row %d high is below low", index)
		}
		if volume < 0 {
			return nil, schemaDriftf("OHLCV row %d volume is negative", index)
		}
		ts := int64(tsFloat)
		day := time.Unix(ts, 0).UTC().Format("2006-01-02")
		if day < startDate || day > endDate {
			continue
		}
		daily = append(daily, dailyBar{
			ts: ts, date: day, endDate: day,
			open: open, high: high, low: low, close: close, volume: volume,
		})
	}
	sort.SliceStable(daily, func(i, j int) bool { return daily[i].ts < daily[j].ts })
	return daily, nil
}

// aggregateBars folds ascending daily bars into week/month/year bars: first
// open, last close, max high, min low, summed volume, keyed by ISO
// year-week, calendar year-month, or calendar year - mirroring
// normalize._aggregate_prices. Groups are emitted in the chronological
// order their first bar appears (daily is already ascending).
func aggregateBars(daily []dailyBar, interval string) []dailyBar {
	type group struct {
		bar   dailyBar
		first bool
	}
	order := make([]string, 0)
	groups := make(map[string]*dailyBar)
	for _, row := range daily {
		day, err := time.Parse("2006-01-02", row.date)
		if err != nil {
			continue
		}
		var key string
		switch interval {
		case "week":
			year, week := day.ISOWeek()
			key = fmt.Sprintf("%04d-W%02d", year, week)
		case "month":
			key = fmt.Sprintf("%04d-%02d", day.Year(), int(day.Month()))
		default: // "year"
			key = fmt.Sprintf("%04d", day.Year())
		}
		bar, ok := groups[key]
		if !ok {
			b := dailyBar{date: row.date, endDate: row.date, open: row.open, high: row.high, low: row.low, close: row.close, volume: row.volume}
			groups[key] = &b
			order = append(order, key)
			continue
		}
		if row.high > bar.high {
			bar.high = row.high
		}
		if row.low < bar.low {
			bar.low = row.low
		}
		bar.volume += row.volume
		bar.close = row.close
		bar.endDate = row.date
	}
	out := make([]dailyBar, 0, len(order))
	for _, key := range order {
		out = append(out, *groups[key])
	}
	return out
}

// integralVolume renders a volume as *int64, matching fd.price_record's
// "integral floats become ints" rule (Price.Volume has no float form).
func integralVolume(v float64) int64 { return int64(math.Round(v)) }

func buildPriceRecords(bars []dailyBar, interval string) []fd.Price {
	records := make([]fd.Price, 0, len(bars))
	for _, bar := range bars {
		day := bar.date
		if interval != "day" {
			day = bar.endDate
		}
		open, high, low, close := bar.open, bar.high, bar.low, bar.close
		volume := integralVolume(bar.volume)
		time := day
		records = append(records, fd.Price{
			Open: &open, High: &high, Low: &low, Close: &close,
			Volume: &volume, Time: &time,
		})
	}
	return records
}

// NormalizePrices parses a DefiLlama /equities/v1/ohlcv payload into
// ascending FD Price records for one interval ("day", "week", "month", or
// "year"), mirroring normalize.normalize_prices + fd.price_record.
// startDate/endDate are inclusive bounds in YYYY-MM-DD form.
func NormalizePrices(raw json.RawMessage, startDate, endDate, interval string) ([]fd.Price, error) {
	daily, err := parseDailyBars(raw, startDate, endDate)
	if err != nil {
		return nil, err
	}
	if interval == "day" {
		return buildPriceRecords(daily, "day"), nil
	}
	return buildPriceRecords(aggregateBars(daily, interval), interval), nil
}
