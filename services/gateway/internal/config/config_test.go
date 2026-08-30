package config

import (
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	// GATEWAY_DATABASE_URL has no default (it carries a real credential —
	// see TestLoad_MissingDatabaseURL) and so must be set even for a test
	// that's only checking every other field's default.
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://user:pass@localhost:5432/mithyax")
	// GATEWAY_AUTH_TOKEN/GATEWAY_ADMIN_AUTH_TOKEN have no default either
	// (see TestLoad_MissingAuthToken/TestLoad_MissingAdminAuthToken).
	t.Setenv("GATEWAY_AUTH_TOKEN", "test-token")
	t.Setenv("GATEWAY_ADMIN_AUTH_TOKEN", "test-admin-token")
	// GATEWAY_EXTENSION_TOKEN has no default either (see
	// TestLoad_MissingExtensionToken).
	t.Setenv("GATEWAY_EXTENSION_TOKEN", "test-extension-token")

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
	if cfg.DatabaseURL != "postgres://user:pass@localhost:5432/mithyax" {
		t.Errorf("DatabaseURL = %q, want the value GATEWAY_DATABASE_URL was set to", cfg.DatabaseURL)
	}
	if cfg.RedisURL != defaultRedisURL {
		t.Errorf("RedisURL = %q, want %q", cfg.RedisURL, defaultRedisURL)
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
	if cfg.RealtimeMaxSessionDuration != defaultRealtimeMaxSessionDuration {
		t.Errorf("RealtimeMaxSessionDuration = %v, want %v", cfg.RealtimeMaxSessionDuration, defaultRealtimeMaxSessionDuration)
	}
	if cfg.RealtimeMaxFrames != defaultRealtimeMaxFrames {
		t.Errorf("RealtimeMaxFrames = %d, want %d", cfg.RealtimeMaxFrames, defaultRealtimeMaxFrames)
	}
	if cfg.RealtimeMaxAudioChunks != defaultRealtimeMaxAudioChunks {
		t.Errorf("RealtimeMaxAudioChunks = %d, want %d", cfg.RealtimeMaxAudioChunks, defaultRealtimeMaxAudioChunks)
	}
	if cfg.VideoWorkers != defaultVideoWorkers {
		t.Errorf("VideoWorkers = %d, want %d", cfg.VideoWorkers, defaultVideoWorkers)
	}
	if cfg.AudioWorkers != defaultAudioWorkers {
		t.Errorf("AudioWorkers = %d, want %d", cfg.AudioWorkers, defaultAudioWorkers)
	}
	if cfg.AuthToken != "test-token" {
		t.Errorf("AuthToken = %q, want the value GATEWAY_AUTH_TOKEN was set to", cfg.AuthToken)
	}
	if cfg.AdminAuthToken != "test-admin-token" {
		t.Errorf("AdminAuthToken = %q, want the value GATEWAY_ADMIN_AUTH_TOKEN was set to", cfg.AdminAuthToken)
	}
	if cfg.ExtensionAuthToken != "test-extension-token" {
		t.Errorf("ExtensionAuthToken = %q, want the value GATEWAY_EXTENSION_TOKEN was set to", cfg.ExtensionAuthToken)
	}
	if cfg.ExtensionSessionCredentialTTL != defaultExtensionSessionCredentialTTL {
		t.Errorf("ExtensionSessionCredentialTTL = %v, want %v", cfg.ExtensionSessionCredentialTTL, defaultExtensionSessionCredentialTTL)
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
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://user:pass@db:5432/mithyax_test")
	t.Setenv("GATEWAY_REDIS_URL", "redis://cache:6379")
	t.Setenv("GATEWAY_WORKER_COUNT", "8")
	t.Setenv("GATEWAY_WORKER_QUEUE_SIZE", "256")
	t.Setenv("GATEWAY_WORKER_SHUTDOWN_TIMEOUT", "3m")
	t.Setenv("REALTIME_MAX_VIDEO_QUEUE", "20")
	t.Setenv("REALTIME_VIDEO_WORKERS", "4")
	t.Setenv("REALTIME_MAX_AUDIO_QUEUE", "15")
	t.Setenv("REALTIME_AUDIO_WORKERS", "3")
	t.Setenv("REALTIME_MAX_SESSIONS", "50")
	t.Setenv("REALTIME_MAX_SESSION_DURATION", "30m")
	t.Setenv("REALTIME_MAX_FRAMES", "5000")
	t.Setenv("REALTIME_MAX_AUDIO_CHUNKS", "2500")
	t.Setenv("GATEWAY_VIDEO_WORKERS", "5")
	t.Setenv("GATEWAY_AUDIO_WORKERS", "4")
	t.Setenv("GATEWAY_AUTH_TOKEN", "override-token")
	t.Setenv("GATEWAY_ADMIN_AUTH_TOKEN", "override-admin-token")
	t.Setenv("GATEWAY_EXTENSION_TOKEN", "override-extension-token")
	t.Setenv("GATEWAY_EXTENSION_SESSION_TTL", "5m")

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
	if cfg.DatabaseURL != "postgres://user:pass@db:5432/mithyax_test" {
		t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, "postgres://user:pass@db:5432/mithyax_test")
	}
	if cfg.RedisURL != "redis://cache:6379" {
		t.Errorf("RedisURL = %q, want %q", cfg.RedisURL, "redis://cache:6379")
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
	if cfg.RealtimeMaxSessionDuration != 30*time.Minute {
		t.Errorf("RealtimeMaxSessionDuration = %v, want %v", cfg.RealtimeMaxSessionDuration, 30*time.Minute)
	}
	if cfg.RealtimeMaxFrames != 5000 {
		t.Errorf("RealtimeMaxFrames = %d, want %d", cfg.RealtimeMaxFrames, 5000)
	}
	if cfg.RealtimeMaxAudioChunks != 2500 {
		t.Errorf("RealtimeMaxAudioChunks = %d, want %d", cfg.RealtimeMaxAudioChunks, 2500)
	}
	if cfg.VideoWorkers != 5 {
		t.Errorf("VideoWorkers = %d, want %d", cfg.VideoWorkers, 5)
	}
	if cfg.AudioWorkers != 4 {
		t.Errorf("AudioWorkers = %d, want %d", cfg.AudioWorkers, 4)
	}
	if cfg.AuthToken != "override-token" {
		t.Errorf("AuthToken = %q, want %q", cfg.AuthToken, "override-token")
	}
	if cfg.AdminAuthToken != "override-admin-token" {
		t.Errorf("AdminAuthToken = %q, want %q", cfg.AdminAuthToken, "override-admin-token")
	}
	if cfg.ExtensionAuthToken != "override-extension-token" {
		t.Errorf("ExtensionAuthToken = %q, want %q", cfg.ExtensionAuthToken, "override-extension-token")
	}
	if cfg.ExtensionSessionCredentialTTL != 5*time.Minute {
		t.Errorf("ExtensionSessionCredentialTTL = %v, want %v", cfg.ExtensionSessionCredentialTTL, 5*time.Minute)
	}
}

func TestLoad_InvalidVideoWorkers(t *testing.T) {
	t.Setenv("GATEWAY_VIDEO_WORKERS", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with GATEWAY_VIDEO_WORKERS=0: expected error, got nil")
	}
}

func TestLoad_InvalidAudioWorkers(t *testing.T) {
	t.Setenv("GATEWAY_AUDIO_WORKERS", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_AUDIO_WORKERS: expected error, got nil")
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

func TestLoad_InvalidRealtimeMaxFrames(t *testing.T) {
	t.Setenv("REALTIME_MAX_FRAMES", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with REALTIME_MAX_FRAMES=0: expected error, got nil")
	}
}

func TestLoad_InvalidRealtimeMaxAudioChunks(t *testing.T) {
	t.Setenv("REALTIME_MAX_AUDIO_CHUNKS", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid REALTIME_MAX_AUDIO_CHUNKS: expected error, got nil")
	}
}

func TestLoad_InvalidRealtimeMaxSessionDuration(t *testing.T) {
	t.Setenv("REALTIME_MAX_SESSION_DURATION", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid REALTIME_MAX_SESSION_DURATION: expected error, got nil")
	}
}

func TestLoad_ZeroRealtimeMaxSessionDuration_Rejected(t *testing.T) {
	t.Setenv("REALTIME_MAX_SESSION_DURATION", "0s")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with REALTIME_MAX_SESSION_DURATION=0s: expected error, got nil")
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

func TestLoad_InvalidDatabaseURL(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "not-a-url")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_DATABASE_URL: expected error, got nil")
	}
}

// TestLoad_MissingDatabaseURL proves Load() refuses to start with no
// database configured at all, rather than quietly falling back to some
// default connection string — GATEWAY_DATABASE_URL carries a real
// credential, so unlike every other setting there's nothing safe to
// default it to.
func TestLoad_MissingDatabaseURL(t *testing.T) {
	if _, err := Load(); err == nil {
		t.Fatal("Load() with GATEWAY_DATABASE_URL unset: expected error, got nil")
	}
}

// TestLoad_MissingAuthToken proves Load() refuses to start with no
// GATEWAY_AUTH_TOKEN configured — like GATEWAY_DATABASE_URL, it
// carries a real credential, so there's nothing safe to default it to.
// GATEWAY_DATABASE_URL is set here specifically so the error this
// triggers is actually about the missing token, not the also-missing
// (but unrelated) database URL.
func TestLoad_MissingAuthToken(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://user:pass@localhost:5432/mithyax")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with GATEWAY_AUTH_TOKEN unset: expected error, got nil")
	}
}

// TestLoad_MissingAdminAuthToken mirrors TestLoad_MissingAuthToken for
// GATEWAY_ADMIN_AUTH_TOKEN — also a credential with no default (7.7.2).
// GATEWAY_AUTH_TOKEN is set here so the error is actually about the
// missing admin token, not the (checked first) user one.
func TestLoad_MissingAdminAuthToken(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://user:pass@localhost:5432/mithyax")
	t.Setenv("GATEWAY_AUTH_TOKEN", "test-token")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with GATEWAY_ADMIN_AUTH_TOKEN unset: expected error, got nil")
	}
}

// TestLoad_MissingExtensionToken mirrors TestLoad_MissingAdminAuthToken
// for GATEWAY_EXTENSION_TOKEN — also a credential with no default
// (8.1). GATEWAY_AUTH_TOKEN/GATEWAY_ADMIN_AUTH_TOKEN are set here so the
// error is actually about the missing extension token, not either of
// those.
func TestLoad_MissingExtensionToken(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://user:pass@localhost:5432/mithyax")
	t.Setenv("GATEWAY_AUTH_TOKEN", "test-token")
	t.Setenv("GATEWAY_ADMIN_AUTH_TOKEN", "test-admin-token")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with GATEWAY_EXTENSION_TOKEN unset: expected error, got nil")
	}
}

func TestLoad_InvalidExtensionSessionTTL(t *testing.T) {
	t.Setenv("GATEWAY_EXTENSION_SESSION_TTL", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_EXTENSION_SESSION_TTL: expected error, got nil")
	}
}

func TestLoad_ZeroExtensionSessionTTL_Rejected(t *testing.T) {
	t.Setenv("GATEWAY_EXTENSION_SESSION_TTL", "0s")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with GATEWAY_EXTENSION_SESSION_TTL=0s: expected error, got nil")
	}
}

func TestLoad_InvalidRedisURL(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://user:pass@localhost:5432/mithyax")
	t.Setenv("GATEWAY_AUTH_TOKEN", "test-token")
	t.Setenv("GATEWAY_ADMIN_AUTH_TOKEN", "test-admin-token")
	t.Setenv("GATEWAY_REDIS_URL", "not-a-url")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with invalid GATEWAY_REDIS_URL: expected error, got nil")
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
