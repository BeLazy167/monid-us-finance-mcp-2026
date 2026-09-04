// Command server is the single Go binary for the Monid Finance API: the
// Financial Datasets-identical REST surface, the MCP transport mounted at
// /mcp and /api, and the edge concerns (auth, cache, rate limit, static
// site) — all in one process, with no proxy hop to a separate backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/httpapi"
	"github.com/belazy/monid-finance/mcpserver"
	"github.com/belazy/monid-finance/monid"
	"github.com/belazy/monid-finance/service"
)

func main() {
	cfg, err := loadEnvConfig()
	if err != nil {
		log.Fatalf("server config: %v", err)
	}

	allowlist, err := monid.LoadAllowlist(cfg.allowlistPath)
	if err != nil {
		log.Fatalf("server: loading allowlist %s: %v", cfg.allowlistPath, err)
	}

	httpClient := &http.Client{
		Timeout: 180 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 20,
			MaxConnsPerHost:     50,
		},
	}

	// The receipts ledger is best-effort observability, never a response
	// dependency: a nil ledger just disables recording (see the shared
	// contract), so a missing/unwritable path never blocks startup.
	var ledger *fd.ReceiptsLedger
	if cfg.receiptsPath != "" {
		ledger = fd.NewReceiptsLedger(cfg.receiptsPath)
	}

	svc := service.New(service.Config{
		HTTP:              httpClient,
		Allowlist:         allowlist,
		Ledger:            ledger,
		MaxConcurrentRuns: 8,
		CacheTTL:          5 * time.Minute,
	})

	mcpServer, err := mcpserver.New(dispatcher{svc: svc})
	if err != nil {
		log.Fatalf("server: building MCP server: %v", err)
	}

	router := httpapi.NewRouter(httpapi.Config{
		Caller:             callerAdapter{svc: svc},
		MCPHandler:         mcpServer,
		StaticDir:          cfg.staticDir,
		AllowedAPIKeys:     cfg.apiKeys,
		DemoMonidAPIKey:    cfg.demoMonidAPIKey,
		RateLimitPerMinute: cfg.rateLimitPerMinute,
		CORSAllowedOrigins: cfg.corsAllowedOrigins,
	})

	httpServer := &http.Server{
		Addr:    ":" + cfg.port,
		Handler: router,
		// Some upstream calls (scrapes, extractions) are slow by nature;
		// keep the write window wide enough for them to complete.
		WriteTimeout:      180 * time.Second,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	// Fly sends SIGTERM on deploy/stop; SIGINT covers local Ctrl-C.
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("server listening on :%s (static %s)", cfg.port, cfg.staticDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-stop
	log.Print("server shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}

// envConfig holds every setting read from the environment.
type envConfig struct {
	port               string
	staticDir          string
	allowlistPath      string
	receiptsPath       string
	rateLimitPerMinute int
	corsAllowedOrigins []string
	demoMonidAPIKey    string
	apiKeys            []string
}

func loadEnvConfig() (envConfig, error) {
	cfg := envConfig{
		port:          envOr("PORT", "8080"),
		staticDir:     envOr("STATIC_DIR", "website"),
		allowlistPath: envOr("ALLOWLIST_PATH", "docs/monid_finance_discovery.json"),
		receiptsPath:  strings.TrimSpace(os.Getenv("RECEIPTS_PATH")),
	}

	rate := 60
	if raw := strings.TrimSpace(os.Getenv("RATE_LIMIT_PER_MINUTE")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return cfg, errors.New("invalid RATE_LIMIT_PER_MINUTE: " + raw)
		}
		rate = n
	}
	cfg.rateLimitPerMinute = rate

	cfg.corsAllowedOrigins = splitList(os.Getenv("CORS_ALLOWED_ORIGINS"))
	// DEMO_MONID_API_KEY is never logged; only its presence, never its
	// value, is observable.
	cfg.demoMonidAPIKey = strings.TrimSpace(os.Getenv("DEMO_MONID_API_KEY"))
	cfg.apiKeys = splitList(os.Getenv("API_KEYS"))

	return cfg, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// callerAdapter satisfies httpapi.Caller by delegating to *service.Service
// and copying its Result field-for-field into httpapi.Result. httpapi
// defines its own Result type (rather than importing go/service) so it
// builds and tests independently of go/service; this adapter is the one
// place the two shapes are bridged.
type callerAdapter struct {
	svc *service.Service
}

func (a callerAdapter) Call(ctx context.Context, apiKey, tool string, args map[string]any) (httpapi.Result, error) {
	result, err := a.svc.Call(ctx, apiKey, tool, args)
	if err != nil {
		return httpapi.Result{}, err
	}
	return adaptResult(result), nil
}

// capabilityHandlers maps every httpapi.Caller.Capability name to the
// go/service.Service exported method it runs. Every entry here is one of
// the capabilities go/service/capabilities.go exposes precisely because
// it is NOT a Financial Datasets MCP tool name (so it is never a valid
// Call argument, and none of these names appear in
// go/mcpserver/tool_schemas.json): the two are separate namespaces by
// construction, not just by convention. Each method value's signature
// already matches this map's value type exactly (method expressions on
// *service.Service), so no per-capability adapter function is needed.
//
// A capability with no entry here is unreachable from every surface and
// nothing fails at build time, so TestCapabilityNamesNeverCollideWithMCPToolNames
// also pins this map's size as a deliberate ratchet.
var capabilityHandlers = map[string]func(*service.Service, context.Context, string, map[string]any) (service.Result, error){
	"list_company_facts_tickers":          (*service.Service).ListCompanyFactsTickers,
	"list_earnings_tickers":               (*service.Service).ListEarningsTickers,
	"list_filings_tickers":                (*service.Service).ListFilingsTickers,
	"list_metrics_snapshot_tickers":       (*service.Service).ListMetricsSnapshotTickers,
	"list_prices_tickers":                 (*service.Service).ListPricesTickers,
	"list_price_snapshot_tickers":         (*service.Service).ListPriceSnapshotTickers,
	"list_institutional_holdings_tickers": (*service.Service).ListInstitutionalHoldingsTickers,
	"list_kpi_tickers":                    (*service.Service).ListKPITickers,
	"list_filing_types":                   (*service.Service).ListFilingTypes,
	"list_filing_item_types":              (*service.Service).ListFilingItemTypes,
	"list_interest_rate_banks":            (*service.Service).ListInterestRateBanks,
	"get_all_financials":                  (*service.Service).GetAllFinancials,
	"search_line_items":                   (*service.Service).SearchLineItems,
	"get_market_snapshot":                 (*service.Service).GetMarketSnapshot,
}

// Capability satisfies httpapi.Caller's non-tool capability surface (see
// httpapi.Caller's doc comment for why this is a separate method rather
// than a Call tool name).
func (a callerAdapter) Capability(ctx context.Context, apiKey, name string, args map[string]any) (httpapi.Result, error) {
	handler, ok := capabilityHandlers[name]
	if !ok {
		return httpapi.Result{}, fmt.Errorf("unknown capability %q", name)
	}
	result, err := handler(a.svc, ctx, apiKey, args)
	if err != nil {
		return httpapi.Result{}, err
	}
	return adaptResult(result), nil
}

// adaptResult copies a go/service.Result field-for-field into
// httpapi.Result, the one place the two shapes are bridged.
func adaptResult(result service.Result) httpapi.Result {
	return httpapi.Result{
		Value:      result.Value,
		WrapperKey: result.WrapperKey,
		Paginate:   result.Paginate,
	}
}

// dispatcher satisfies mcpserver.Dispatcher: it extracts the caller's own
// Monid API key from the request and runs the tool through the shared
// service, returning the bare FD value (Result.Value) with no envelope and
// no pagination — MCP list tools answer with a bare array, matching
// Financial Datasets' own MCP server.
type dispatcher struct {
	svc *service.Service
}

func (d dispatcher) Call(r *http.Request, name string, args map[string]any) (any, error) {
	apiKey := strings.TrimSpace(r.Header.Get("X-API-KEY"))
	result, err := d.svc.Call(r.Context(), apiKey, name, args)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}
