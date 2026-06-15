package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefault(t *testing.T) {
	_ = os.Unsetenv("HOST_RUTEBAYAR_ENV")
	_ = os.Unsetenv("HOST_RUTEBAYAR_HOST")
	_ = os.Unsetenv("HOST_RUTEBAYAR_PORT")
	_ = os.Unsetenv("HOST_RUTEBAYAR_TIMEOUT")

	cfg := Load()
	if cfg.Env != "development" {
		t.Fatalf("expected default Env=development, got %q", cfg.Env)
	}
	if cfg.Port != 8080 {
		t.Fatalf("expected default Port=8080, got %d", cfg.Port)
	}
	if cfg.ListenAddress() != "127.0.0.1:8080" {
		t.Fatalf("unexpected listen address: %s", cfg.ListenAddress())
	}
	if cfg.Timeout != 10*time.Second {
		t.Fatalf("expected default Timeout=10s, got %s", cfg.Timeout)
	}
}

func TestLoadWithEnv(t *testing.T) {
	_ = os.Setenv("HOST_RUTEBAYAR_ENV", "production")
	_ = os.Setenv("HOST_RUTEBAYAR_HOST", "0.0.0.0")
	_ = os.Setenv("HOST_RUTEBAYAR_PORT", "9000")
	_ = os.Setenv("HOST_RUTEBAYAR_TIMEOUT", "2s")
	defer func() {
		_ = os.Unsetenv("HOST_RUTEBAYAR_ENV")
		_ = os.Unsetenv("HOST_RUTEBAYAR_HOST")
		_ = os.Unsetenv("HOST_RUTEBAYAR_PORT")
		_ = os.Unsetenv("HOST_RUTEBAYAR_TIMEOUT")
	}()

	cfg := Load()
	if cfg.Env != "production" || cfg.Host != "0.0.0.0" || cfg.Port != 9000 || cfg.Timeout != 2*time.Second {
		t.Fatalf("loaded config does not match overrides: %+v", cfg)
	}
	if cfg.ListenAddress() != "0.0.0.0:9000" {
		t.Fatalf("unexpected listen address: %s", cfg.ListenAddress())
	}
}
