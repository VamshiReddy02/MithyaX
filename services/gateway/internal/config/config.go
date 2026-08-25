// Package config loads gateway configuration from environment variables.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration for the gateway service.
type Config struct {
	// Port the HTTP server listens on.
	Port string
	// Environment is a free-form deployment label (e.g. "development", "production").
	Environment string
	// LogLevel controls the minimum severity of emitted log records ("debug", "info", "warn", "error").
	LogLevel string
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight requests to finish.
	ShutdownTimeout time.Duration
	// DetectorBaseURL is the base URL of the Python video-detector service.
	DetectorBaseURL string
	// DetectorTimeout bounds how long a single analysis request may take.
	DetectorTimeout time.Duration
}

const (
	defaultPort            = "8080"
	defaultEnvironment     = "development"
	defaultLogLevel        = "info"
	defaultShutdownTimeout = 10 * time.Second
	defaultDetectorBaseURL = "http://localhost:8000"
	defaultDetectorTimeout = 60 * time.Second
)

// Load builds a Config from environment variables, falling back to defaults
// for anything unset.
func Load() (Config, error) {
	cfg := Config{
		Port:            getEnv("GATEWAY_PORT", defaultPort),
		Environment:     getEnv("GATEWAY_ENV", defaultEnvironment),
		LogLevel:        getEnv("GATEWAY_LOG_LEVEL", defaultLogLevel),
		ShutdownTimeout: defaultShutdownTimeout,
		DetectorBaseURL: getEnv("GATEWAY_DETECTOR_URL", defaultDetectorBaseURL),
		DetectorTimeout: defaultDetectorTimeout,
	}

	if raw, ok := os.LookupEnv("GATEWAY_SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid GATEWAY_SHUTDOWN_TIMEOUT %q: %w", raw, err)
		}
		cfg.ShutdownTimeout = d
	}

	if raw, ok := os.LookupEnv("GATEWAY_DETECTOR_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid GATEWAY_DETECTOR_TIMEOUT %q: %w", raw, err)
		}
		cfg.DetectorTimeout = d
	}

	if _, err := strconv.Atoi(cfg.Port); err != nil {
		return Config{}, fmt.Errorf("invalid GATEWAY_PORT %q: must be numeric", cfg.Port)
	}

	if _, err := url.ParseRequestURI(cfg.DetectorBaseURL); err != nil {
		return Config{}, fmt.Errorf("invalid GATEWAY_DETECTOR_URL %q: %w", cfg.DetectorBaseURL, err)
	}

	return cfg, nil
}

// Addr returns the address the HTTP server should bind to.
func (c Config) Addr() string {
	return ":" + c.Port
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
