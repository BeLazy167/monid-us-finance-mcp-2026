// Command server is the single Go binary for the Monid Finance API: the
// Financial Datasets-identical REST surface, the MCP transport mounted at
// /mcp and /api, and the edge concerns (auth, cache, rate limit, static
// site) — all in one process, with no proxy hop to a separate backend.
package main

import (
	"context"
	"errors"
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
	return httpapi.Result{
		Value:      result.Value,
		WrapperKey: result.WrapperKey,
		Paginate:   result.Paginate,
	}, nil
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
