package monid

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// wireRun mirrors the /v1/run response shape.
type wireRun struct {
	RunID            string          `json:"runId"`
	Provider         string          `json:"provider"`
	Endpoint         string          `json:"endpoint"`
	Status           string          `json:"status"`
	Output           json.RawMessage `json:"output"`
	ProviderResponse struct {
		HTTPStatus int `json:"httpStatus"`
	} `json:"providerResponse"`
	Billing struct {
		ReportedCost *struct {
			Currency string   `json:"currency"`
			Value    *float64 `json:"value"`
			Unit     string   `json:"unit"`
		} `json:"reportedCost"`
	} `json:"billing"`
	// Polled runs report the settled cost under "cost" instead of "billing".
	Cost *struct {
		Currency string   `json:"currency"`
		Value    *float64 `json:"value"`
		Unit     string   `json:"unit"`
	} `json:"cost"`
	Price *struct {
		Amount *struct {
			Value    *float64 `json:"value"`
			Currency string   `json:"currency"`
		} `json:"amount"`
	} `json:"price"`
	CreatedAt   string `json:"createdAt"`
	CompletedAt string `json:"completedAt"`
}

// costFactors converts a reported billing unit into whole dollars.
var costFactors = map[string]float64{
	"DOLLAR":       1,
	"USD":          1,
	"CENT":         0.01,
	"MILLI_DOLLAR": 0.001,
	"MICRO_DOLLAR": 0.000001,
	"NANO_DOLLAR":  0.000000001,
}

// parseRun validates and converts one /v1/run response.
func parseRun(raw []byte, provider, endpoint string) (*Run, error) {
	var wire wireRun
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: response was not valid JSON", Provider: provider, Endpoint: endpoint}
	}
	if wire.RunID == "" || wire.Status == "" {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: response omitted runId or status", Provider: provider, Endpoint: endpoint}
	}
	if wire.Provider != "" && wire.Provider != provider {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: response provider does not match the request", Provider: provider, Endpoint: endpoint}
	}
	if wire.Endpoint != "" && wire.Endpoint != endpoint {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: response endpoint does not match the request", Provider: provider, Endpoint: endpoint}
	}

	run := &Run{
		Provider:           provider,
		Endpoint:           endpoint,
		RunID:              wire.RunID,
		Status:             wire.Status,
		Output:             wire.Output,
		ProviderHTTPStatus: wire.ProviderResponse.HTTPStatus,
		CreatedAt:          wire.CreatedAt,
		CompletedAt:        wire.CompletedAt,
	}

	cost, err := parseCost(wire, provider, endpoint)
	if err != nil {
		return nil, err
	}
	run.Cost = cost
	return run, nil
}

// parseCost reads the measured cost, preferring the billing report over the
// list price. A run without a finite measured cost is a schema error: the
// receipts ledger must never record a guessed number.
func parseCost(wire wireRun, provider, endpoint string) (*Money, error) {
	if rc := wire.Billing.ReportedCost; rc != nil && rc.Value != nil {
		factor, ok := costFactors[strings.ToUpper(rc.Unit)]
		if !ok {
			return nil, &RunError{Kind: ErrSchema, Message: fmt.Sprintf("monid: unknown billing unit %q", rc.Unit), Provider: provider, Endpoint: endpoint}
		}
		value := *rc.Value * factor
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, &RunError{Kind: ErrSchema, Message: "monid: billing cost is not finite", Provider: provider, Endpoint: endpoint}
		}
		currency := rc.Currency
		if currency == "" {
			currency = "USD"
		}
		return &Money{Value: value, Currency: currency}, nil
	}
	if c := wire.Cost; c != nil && c.Value != nil {
		factor, ok := costFactors[strings.ToUpper(c.Unit)]
		if !ok {
			factor = 1 // a polled run reports whole units when it omits one
		}
		value := *c.Value * factor
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			currency := c.Currency
			if currency == "" {
				currency = "USD"
			}
			return &Money{Value: value, Currency: currency}, nil
		}
	}
	if wire.Price != nil && wire.Price.Amount != nil && wire.Price.Amount.Value != nil {
		value := *wire.Price.Amount.Value
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, &RunError{Kind: ErrSchema, Message: "monid: price is not finite", Provider: provider, Endpoint: endpoint}
		}
		currency := wire.Price.Amount.Currency
		if currency == "" {
			currency = "USD"
		}
		return &Money{Value: value, Currency: currency}, nil
	}
	return nil, &RunError{Kind: ErrSchema, Message: "monid: run omitted a measured billing cost", Provider: provider, Endpoint: endpoint}
}

// Allowlist decides which provider/endpoint pairs may run.
type Allowlist interface {
	Permits(provider, endpoint string) bool
}

// DiscoveryAllowlist is the committed discovery artifact: only endpoints that
// were live-validated during discovery may ever execute.
type DiscoveryAllowlist struct {
	pairs map[string]bool
}

// LoadAllowlist reads the discovery artifact from disk.
func LoadAllowlist(path string) (*DiscoveryAllowlist, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Endpoints []struct {
			Provider string `json:"provider"`
			Endpoint string `json:"endpoint"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	pairs := make(map[string]bool, len(doc.Endpoints))
	for _, e := range doc.Endpoints {
		if e.Provider != "" && e.Endpoint != "" {
			pairs[e.Provider+" "+e.Endpoint] = true
		}
	}
	return &DiscoveryAllowlist{pairs: pairs}, nil
}

// Permits reports whether the pair was present in the discovery artifact.
func (a *DiscoveryAllowlist) Permits(provider, endpoint string) bool {
	if a == nil {
		return false
	}
	return a.pairs[provider+" "+endpoint]
}
