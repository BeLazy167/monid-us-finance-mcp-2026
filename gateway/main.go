package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// main boots the gateway: resolve config from the environment, serve until a
// termination signal arrives, then drain in-flight requests.
func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("gateway config: %v", err)
	}

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: newGateway(cfg),
		// Upstream scrapes and extractions are slow by nature; keep the
		// read/write windows wide enough for them.
		ReadTimeout:       180 * time.Second,
		WriteTimeout:      180 * time.Second,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("gateway listening on :%s (upstream %s, website %s)", cfg.Port, cfg.Upstream, cfg.WebsiteRoot)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("gateway serve: %v", err)
		}
	}()

	<-stop
	log.Print("gateway shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("gateway shutdown: %v", err)
	}
}
