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
	adminPassword := strings.TrimSpace(cfg.AdminPassword)
	isLoopbackHost := cfg.Host == "127.0.0.1" || strings.EqualFold(cfg.Host, "localhost") || cfg.Host == "::1"
	isNonDevelopment := !strings.EqualFold(cfg.Env, "development") && !strings.EqualFold(cfg.Env, "dev")
	if adminPassword == "" {
		if isLoopbackHost && strings.EqualFold(cfg.Env, "development") {
			adminPassword = "admin123"
			log.Println("No admin password configured; defaulting to admin123 for local development.")
		} else {
			log.Fatal("missing HOST_RUTEBAYAR_ADMIN_PASSWORD; set a non-default value before running outside local development")
		}
	}
	if adminPassword == "admin123" && isNonDevelopment {
		if !isLoopbackHost {
			log.Fatal("insecure default admin password outside local development; set HOST_RUTEBAYAR_ADMIN_PASSWORD and avoid exposing default credentials")
		}
		log.Println("WARNING: running outside production-safe mode with default admin password. set HOST_RUTEBAYAR_ADMIN_PASSWORD before public exposure")
	}

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
	serviceMux := httphandlers.SetupMux(orchestrator, adminPassword)
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
