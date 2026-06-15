package config

import (
	"testing"
	"time"
)

func TestLoadDefault(t *testing.T) {
	t.Setenv("HOST_RUTEBAYAR_ENV", "development")
	t.Setenv("HOST_RUTEBAYAR_HOST", "127.0.0.1")
	t.Setenv("HOST_RUTEBAYAR_PORT", "18123")
	t.Setenv("HOST_RUTEBAYAR_TIMEOUT", "10s")
	t.Setenv("HOST_RUTEBAYAR_DATABASE_DSN", "file:host-rutebayar.db?_pragma=foreign_keys(ON)")

	cfg := Load()
	if cfg.Env != "development" {
		t.Fatalf("expected default Env=development, got %q", cfg.Env)
	}
	if cfg.Port != 18123 {
		t.Fatalf("expected default Port=18123, got %d", cfg.Port)
	}
	if cfg.ListenAddress() != "127.0.0.1:18123" {
		t.Fatalf("unexpected listen address: %s", cfg.ListenAddress())
	}
	if cfg.Timeout != 10*time.Second {
		t.Fatalf("expected default Timeout=10s, got %s", cfg.Timeout)
	}
	if cfg.DBDSN == "" {
		t.Fatalf("expected default DBDSN set")
	}
}

func TestLoadWithEnv(t *testing.T) {
	t.Setenv("HOST_RUTEBAYAR_ENV", "production")
	t.Setenv("HOST_RUTEBAYAR_HOST", "0.0.0.0")
	t.Setenv("HOST_RUTEBAYAR_PORT", "9000")
	t.Setenv("HOST_RUTEBAYAR_TIMEOUT", "2s")
	t.Setenv("HOST_RUTEBAYAR_DATABASE_DSN", "file:test.db")

	cfg := Load()
	if cfg.Env != "production" || cfg.Host != "0.0.0.0" || cfg.Port != 9000 || cfg.Timeout != 2*time.Second || cfg.DBDSN != "file:test.db" {
		t.Fatalf("loaded config does not match overrides: %+v", cfg)
	}
	if cfg.ListenAddress() != "0.0.0.0:9000" {
		t.Fatalf("unexpected listen address: %s", cfg.ListenAddress())
	}
}

func TestLoadInvalidConfigUsesDefault(t *testing.T) {
	t.Setenv("HOST_RUTEBAYAR_PORT", "0")
	t.Setenv("HOST_RUTEBAYAR_TIMEOUT", "-1s")

	cfg := Load()
	if cfg.Port != 18123 {
		t.Fatalf("expected fallback port 18123, got %d", cfg.Port)
	}
	if cfg.Timeout != 10*time.Second {
		t.Fatalf("expected fallback timeout 10s, got %s", cfg.Timeout)
	}
}
