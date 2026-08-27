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
	if cfg.RealtimeMaxVideoQueue != defaultRealtimeMaxVideoQueue {
		t.Errorf("RealtimeMaxVideoQueue = %d, want %d", cfg.RealtimeMaxVideoQueue, defaultRealtimeMaxVideoQueue)
	}
	if cfg.RealtimeVideoWorkers != defaultRealtimeVideoWorkers {
		t.Errorf("RealtimeVideoWorkers = %d, want %d", cfg.RealtimeVideoWorkers, defaultRealtimeVideoWorkers)
	}
	if cfg.RealtimeMaxAudioQueue != defaultRealtimeMaxAudioQueue {
		t.Errorf("RealtimeMaxAudioQueue = %d, want %d", cfg.RealtimeMaxAudioQueue, defaultRealtimeMaxAudioQueue)
	}
	if cfg.RealtimeAudioWorkers != defaultRealtimeAudioWorkers {
		t.Errorf("RealtimeAudioWorkers = %d, want %d", cfg.RealtimeAudioWorkers, defaultRealtimeAudioWorkers)
	}
	if cfg.RealtimeMaxSessions != defaultRealtimeMaxSessions {
		t.Errorf("RealtimeMaxSessions = %d, want %d", cfg.RealtimeMaxSessions, defaultRealtimeMaxSessions)
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
	t.Setenv("REALTIME_MAX_VIDEO_QUEUE", "20")
	t.Setenv("REALTIME_VIDEO_WORKERS", "4")
	t.Setenv("REALTIME_MAX_AUDIO_QUEUE", "15")
	t.Setenv("REALTIME_AUDIO_WORKERS", "3")
	t.Setenv("REALTIME_MAX_SESSIONS", "50")

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
	if cfg.RealtimeMaxVideoQueue != 20 {
		t.Errorf("RealtimeMaxVideoQueue = %d, want %d", cfg.RealtimeMaxVideoQueue, 20)
	}
	if cfg.RealtimeVideoWorkers != 4 {
		t.Errorf("RealtimeVideoWorkers = %d, want %d", cfg.RealtimeVideoWorkers, 4)
	}
	if cfg.RealtimeMaxAudioQueue != 15 {
		t.Errorf("RealtimeMaxAudioQueue = %d, want %d", cfg.RealtimeMaxAudioQueue, 15)
	}
	if cfg.RealtimeAudioWorkers != 3 {
		t.Errorf("RealtimeAudioWorkers = %d, want %d", cfg.RealtimeAudioWorkers, 3)
	}
	if cfg.RealtimeMaxSessions != 50 {
		t.Errorf("RealtimeMaxSessions = %d, want %d", cfg.RealtimeMaxSessions, 50)
	}
}

func TestLoad_InvalidRealtimeMaxVideoQueue(t *testing.T) {
	t.Setenv("REALTIME_MAX_VIDEO_QUEUE", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with REALTIME_MAX_VIDEO_QUEUE=0: expected error, got nil")
	}
}

func TestLoad_InvalidRealtimeVideoWorkers(t *testing.T) {
	t.Setenv("REALTIME_VIDEO_WORKERS", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid REALTIME_VIDEO_WORKERS: expected error, got nil")
	}
}

func TestLoad_InvalidRealtimeMaxAudioQueue(t *testing.T) {
	t.Setenv("REALTIME_MAX_AUDIO_QUEUE", "-1")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with REALTIME_MAX_AUDIO_QUEUE=-1: expected error, got nil")
	}
}

func TestLoad_InvalidRealtimeAudioWorkers(t *testing.T) {
	t.Setenv("REALTIME_AUDIO_WORKERS", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with REALTIME_AUDIO_WORKERS=0: expected error, got nil")
	}
}

func TestLoad_InvalidRealtimeMaxSessions(t *testing.T) {
	t.Setenv("REALTIME_MAX_SESSIONS", "nope")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid REALTIME_MAX_SESSIONS: expected error, got nil")
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
