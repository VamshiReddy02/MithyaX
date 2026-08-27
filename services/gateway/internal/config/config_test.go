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
	if cfg.AudioDetectorBaseURL != defaultAudioDetectorBaseURL {
		t.Errorf("AudioDetectorBaseURL = %q, want %q", cfg.AudioDetectorBaseURL, defaultAudioDetectorBaseURL)
	}
	if cfg.AudioDetectorTimeout != defaultAudioDetectorTimeout {
		t.Errorf("AudioDetectorTimeout = %v, want %v", cfg.AudioDetectorTimeout, defaultAudioDetectorTimeout)
	}
	if cfg.WorkerCount != defaultWorkerCount {
		t.Errorf("WorkerCount = %d, want %d", cfg.WorkerCount, defaultWorkerCount)
	}
	if cfg.WorkerQueueSize != defaultWorkerQueueSize {
		t.Errorf("WorkerQueueSize = %d, want %d", cfg.WorkerQueueSize, defaultWorkerQueueSize)
	}
	if cfg.WorkerShutdownTimeout != defaultWorkerShutdownTimeout {
		t.Errorf("WorkerShutdownTimeout = %v, want %v", cfg.WorkerShutdownTimeout, defaultWorkerShutdownTimeout)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "9090")
	t.Setenv("GATEWAY_ENV", "production")
	t.Setenv("GATEWAY_LOG_LEVEL", "debug")
	t.Setenv("GATEWAY_SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("GATEWAY_DETECTOR_URL", "http://detector:9000")
	t.Setenv("GATEWAY_DETECTOR_TIMEOUT", "30s")
	t.Setenv("GATEWAY_AUDIO_DETECTOR_URL", "http://audio-detector:9001")
	t.Setenv("GATEWAY_AUDIO_DETECTOR_TIMEOUT", "45s")
	t.Setenv("GATEWAY_WORKER_COUNT", "8")
	t.Setenv("GATEWAY_WORKER_QUEUE_SIZE", "256")
	t.Setenv("GATEWAY_WORKER_SHUTDOWN_TIMEOUT", "3m")

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
	if cfg.AudioDetectorBaseURL != "http://audio-detector:9001" {
		t.Errorf("AudioDetectorBaseURL = %q, want %q", cfg.AudioDetectorBaseURL, "http://audio-detector:9001")
	}
	if cfg.AudioDetectorTimeout != 45*time.Second {
		t.Errorf("AudioDetectorTimeout = %v, want %v", cfg.AudioDetectorTimeout, 45*time.Second)
	}
	if cfg.WorkerCount != 8 {
		t.Errorf("WorkerCount = %d, want %d", cfg.WorkerCount, 8)
	}
	if cfg.WorkerQueueSize != 256 {
		t.Errorf("WorkerQueueSize = %d, want %d", cfg.WorkerQueueSize, 256)
	}
	if cfg.WorkerShutdownTimeout != 3*time.Minute {
		t.Errorf("WorkerShutdownTimeout = %v, want %v", cfg.WorkerShutdownTimeout, 3*time.Minute)
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

func TestLoad_InvalidAudioDetectorURL(t *testing.T) {
	t.Setenv("GATEWAY_AUDIO_DETECTOR_URL", "not-a-url")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_AUDIO_DETECTOR_URL: expected error, got nil")
	}
}

func TestLoad_InvalidAudioDetectorTimeout(t *testing.T) {
	t.Setenv("GATEWAY_AUDIO_DETECTOR_TIMEOUT", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_AUDIO_DETECTOR_TIMEOUT: expected error, got nil")
	}
}

func TestLoad_InvalidWorkerShutdownTimeout(t *testing.T) {
	t.Setenv("GATEWAY_WORKER_SHUTDOWN_TIMEOUT", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_WORKER_SHUTDOWN_TIMEOUT: expected error, got nil")
	}
}

func TestLoad_InvalidWorkerCount(t *testing.T) {
	t.Setenv("GATEWAY_WORKER_COUNT", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with GATEWAY_WORKER_COUNT=0: expected error, got nil")
	}
}

func TestLoad_InvalidWorkerQueueSize(t *testing.T) {
	t.Setenv("GATEWAY_WORKER_QUEUE_SIZE", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_WORKER_QUEUE_SIZE: expected error, got nil")
	}
}

func TestAddr(t *testing.T) {
	cfg := Config{Port: "8080"}
	if got, want := cfg.Addr(), ":8080"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}
