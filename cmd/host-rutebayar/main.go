package main

import (
	"log"
	"net/http"

	"github.com/pendig/host-rutebayar/internal/config"
)

func main() {
	cfg := config.Load()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	addr := cfg.ListenAddress()
	log.Printf("host-rutebayar starting on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
