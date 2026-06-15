package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/pendig/host-rutebayar/internal/config"
	"github.com/pendig/host-rutebayar/internal/gateway"
	httphandlers "github.com/pendig/host-rutebayar/internal/http"
	"github.com/pendig/host-rutebayar/internal/orchestration"
	"github.com/pendig/host-rutebayar/internal/proxy"
	"github.com/pendig/host-rutebayar/internal/storage"
)

func main() {
	cfg := config.Load()
	store, err := storage.NewSQLiteStore(cfg.DBDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = store.DB().Close()
	}()
	if err := store.Migrate(); err != nil {
		log.Fatal(err)
	}

	orchestrator := orchestration.NewOrchestratorWithStore(store, gateway.DefaultGateway())
	serviceMux := httphandlers.SetupMux(orchestrator, cfg.AdminPassword)
	appMux := serviceMux
	trimmedUpstreamURL := strings.TrimSpace(cfg.UpstreamURL)
	if trimmedUpstreamURL != "" {
		router := http.NewServeMux()
		router.Handle("/host/", proxy.NewOpenAPIProxy(trimmedUpstreamURL))
		router.Handle("/", serviceMux)
		appMux = router
	}

	addr := cfg.ListenAddress()
	server := &http.Server{
		Addr:              addr,
		Handler:           appMux,
		ReadHeaderTimeout: cfg.Timeout,
		ReadTimeout:       cfg.Timeout,
		WriteTimeout:      cfg.Timeout,
		IdleTimeout:       cfg.Timeout,
	}
	log.Printf("database dsn configured")
	log.Printf("host-rutebayar starting on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
