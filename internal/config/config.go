package config

import (
	"log"
	"net"
	"os"
	"strconv"
	"time"
)

// Config captures foundation runtime settings.
type Config struct {
	Env     string
	Port    int
	Host    string
	Timeout time.Duration
	DBDSN   string
}

// Load reads environment config used by all phases.
func Load() Config {
	return Config{
		Env:     getEnv("HOST_RUTEBAYAR_ENV", "development"),
		Host:    getEnv("HOST_RUTEBAYAR_HOST", "127.0.0.1"),
		Port:    getEnvInt("HOST_RUTEBAYAR_PORT", 8080),
		Timeout: getEnvDuration("HOST_RUTEBAYAR_TIMEOUT", 10*time.Second),
		DBDSN:   getEnv("HOST_RUTEBAYAR_DATABASE_DSN", "file:host-rutebayar.db?_pragma=foreign_keys(ON)"),
	}
}

func (c Config) ListenAddress() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func getEnv(key, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	value, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 65535 {
		log.Printf("invalid integer %s=%q, using default %d", key, value, defaultValue)
		return defaultValue
	}
	return parsed
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		log.Printf("invalid duration %s=%q, using default %s", key, value, defaultValue.String())
		return defaultValue
	}
	return duration
}
