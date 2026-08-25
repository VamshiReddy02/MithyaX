package config

import (
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Port != defaultPort {
		t.Errorf("Port = %q, want %q", cfg.Port, defaultPort)
	}
	if cfg.Environment != defaultEnvironment {
		t.Errorf("Environment = %q, want %q", cfg.Environment, defaultEnvironment)
	}
	if cfg.LogLevel != defaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, defaultLogLevel)
	}
	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, defaultShutdownTimeout)
	}
	if cfg.DetectorBaseURL != defaultDetectorBaseURL {
		t.Errorf("DetectorBaseURL = %q, want %q", cfg.DetectorBaseURL, defaultDetectorBaseURL)
	}
	if cfg.DetectorTimeout != defaultDetectorTimeout {
		t.Errorf("DetectorTimeout = %v, want %v", cfg.DetectorTimeout, defaultDetectorTimeout)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "9090")
	t.Setenv("GATEWAY_ENV", "production")
	t.Setenv("GATEWAY_LOG_LEVEL", "debug")
	t.Setenv("GATEWAY_SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("GATEWAY_DETECTOR_URL", "http://detector:9000")
	t.Setenv("GATEWAY_DETECTOR_TIMEOUT", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want %q", cfg.Port, "9090")
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "production")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 5*time.Second)
	}
	if cfg.DetectorBaseURL != "http://detector:9000" {
		t.Errorf("DetectorBaseURL = %q, want %q", cfg.DetectorBaseURL, "http://detector:9000")
	}
	if cfg.DetectorTimeout != 30*time.Second {
		t.Errorf("DetectorTimeout = %v, want %v", cfg.DetectorTimeout, 30*time.Second)
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "not-a-port")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_PORT: expected error, got nil")
	}
}

func TestLoad_InvalidShutdownTimeout(t *testing.T) {
	t.Setenv("GATEWAY_SHUTDOWN_TIMEOUT", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_SHUTDOWN_TIMEOUT: expected error, got nil")
	}
}

func TestLoad_InvalidDetectorURL(t *testing.T) {
	t.Setenv("GATEWAY_DETECTOR_URL", "not-a-url")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_DETECTOR_URL: expected error, got nil")
	}
}

func TestLoad_InvalidDetectorTimeout(t *testing.T) {
	t.Setenv("GATEWAY_DETECTOR_TIMEOUT", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_DETECTOR_TIMEOUT: expected error, got nil")
	}
}

func TestAddr(t *testing.T) {
	cfg := Config{Port: "8080"}
	if got, want := cfg.Addr(), ":8080"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}
