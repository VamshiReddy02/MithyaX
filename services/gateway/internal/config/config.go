// Package config loads gateway configuration from environment variables.
package config

import (
	"errors"
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
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight HTTP requests to finish.
	ShutdownTimeout time.Duration
	// WorkerShutdownTimeout bounds how long graceful shutdown waits for
	// queued/in-flight analyze jobs to finish. Separate from
	// ShutdownTimeout because a video analysis can legitimately take far
	// longer than draining HTTP connections should.
	WorkerShutdownTimeout time.Duration
	// DetectorBaseURL is the base URL of the Python video-detector service.
	DetectorBaseURL string
	// DetectorTimeout bounds how long a single analysis request may take.
	DetectorTimeout time.Duration
	// AudioDetectorBaseURL is the base URL of the Python audio-detector service.
	AudioDetectorBaseURL string
	// AudioDetectorTimeout bounds how long a single audio analysis request may take.
	AudioDetectorTimeout time.Duration
	// WorkerCount is how many goroutines process analyze jobs concurrently.
	WorkerCount int
	// WorkerQueueSize bounds how many analyze jobs may be queued at once.
	WorkerQueueSize int
	// RedisURL is the connection string for the Redis instance backing
	// job state, the job queue, and (from Phase 7.3) the gateway's
	// broader coordination/queue infrastructure (see internal/redis,
	// internal/queue). Unlike DatabaseURL, this carries no credential in
	// the current setup (docker-compose's redis service has no auth
	// configured), so a non-secret default is fine here.
	RedisURL string
	// DatabaseURL is the PostgreSQL connection string backing persisted
	// session records (see internal/database, internal/repository/sessions).
	// Required — see Load, which errors rather than defaulting to any
	// value, since a DSN carries a real credential (see
	// deployments/docker/.env.example for local dev setup). Pool sizing
	// (max/min conns, conn lifetime) intentionally isn't configurable yet
	// — pgxpool's own defaults are fine until real usage says otherwise.
	DatabaseURL string
	// JobTTL bounds how long a job's state is kept in Redis after its
	// last update, before being automatically cleaned up.
	JobTTL time.Duration
	// RealtimeMaxVideoQueue bounds how many frames a live session will
	// hold waiting for a video worker. Once full, the oldest queued
	// frame is dropped in favor of the newest — see internal/realtime.
	RealtimeMaxVideoQueue int
	// RealtimeVideoWorkers is how many goroutines per live session pull
	// frames off the video queue and call the video-detector
	// concurrently.
	RealtimeVideoWorkers int
	// RealtimeMaxAudioQueue bounds how many audio chunks a live session
	// will hold waiting for an audio worker. Unlike video, a full audio
	// queue rejects the new chunk rather than dropping an old one —
	// silently discarding part of a speech stream distorts the
	// analysis, so overload is surfaced to the caller instead.
	RealtimeMaxAudioQueue int
	// RealtimeAudioWorkers is how many goroutines per live session pull
	// chunks off the audio queue and call the audio-detector
	// concurrently.
	RealtimeAudioWorkers int
	// RealtimeMaxSessions bounds how many live analysis sessions this
	// process will run at once, protecting it from unbounded goroutine/
	// memory growth if far more sessions are created than the detector
	// services can actually keep up with.
	RealtimeMaxSessions int
	// VideoWorkers is how many goroutines concurrently consume
	// VIDEO_ANALYSIS jobs from the async Redis queue (see
	// internal/analysisworker). Unrelated to RealtimeVideoWorkers, which
	// serves the live WebSocket pipeline, not this one.
	VideoWorkers int
	// AudioWorkers is how many goroutines concurrently consume
	// AUDIO_ANALYSIS jobs from the async Redis queue.
	AudioWorkers int
	// AuthToken is the shared bearer token internal/auth's middleware
	// requires on every protected request (Phase 7.7.1). Required — like
	// DatabaseURL, it carries a real credential, so Load errors rather
	// than defaulting to any value (see
	// deployments/docker/.env.example). A static token is deliberately
	// the whole mechanism for now; see internal/auth's package doc for
	// why.
	AuthToken string
}

const (
	defaultPort                  = "8080"
	defaultEnvironment           = "development"
	defaultLogLevel              = "info"
	defaultShutdownTimeout       = 10 * time.Second
	defaultWorkerShutdownTimeout = 2 * time.Minute
	defaultDetectorBaseURL       = "http://localhost:8000"
	defaultDetectorTimeout       = 60 * time.Second
	defaultAudioDetectorBaseURL  = "http://localhost:8001"
	defaultAudioDetectorTimeout  = 60 * time.Second
	defaultWorkerCount           = 4
	defaultWorkerQueueSize       = 64
	defaultRedisURL              = "redis://localhost:6379"
	defaultJobTTL                = 24 * time.Hour
	defaultRealtimeMaxVideoQueue = 10
	defaultRealtimeVideoWorkers  = 2
	defaultRealtimeMaxAudioQueue = 10
	defaultRealtimeAudioWorkers  = 2
	defaultRealtimeMaxSessions   = 100
	defaultVideoWorkers          = 2
	defaultAudioWorkers          = 2
)

// Load builds a Config from environment variables, falling back to
// defaults for anything unset — except GATEWAY_DATABASE_URL, which
// carries a real credential and so has no default; it must be set
// explicitly (see deployments/docker/.env.example).
func Load() (Config, error) {
	cfg := Config{
		Port:                  getEnv("GATEWAY_PORT", defaultPort),
		Environment:           getEnv("GATEWAY_ENV", defaultEnvironment),
		LogLevel:              getEnv("GATEWAY_LOG_LEVEL", defaultLogLevel),
		ShutdownTimeout:       defaultShutdownTimeout,
		WorkerShutdownTimeout: defaultWorkerShutdownTimeout,
		DetectorBaseURL:       getEnv("GATEWAY_DETECTOR_URL", defaultDetectorBaseURL),
		DetectorTimeout:       defaultDetectorTimeout,
		AudioDetectorBaseURL:  getEnv("GATEWAY_AUDIO_DETECTOR_URL", defaultAudioDetectorBaseURL),
		AudioDetectorTimeout:  defaultAudioDetectorTimeout,
		WorkerCount:           defaultWorkerCount,
		WorkerQueueSize:       defaultWorkerQueueSize,
		RedisURL:              getEnv("GATEWAY_REDIS_URL", defaultRedisURL),
		DatabaseURL:           os.Getenv("GATEWAY_DATABASE_URL"),
		JobTTL:                defaultJobTTL,
		RealtimeMaxVideoQueue: defaultRealtimeMaxVideoQueue,
		RealtimeVideoWorkers:  defaultRealtimeVideoWorkers,
		RealtimeMaxAudioQueue: defaultRealtimeMaxAudioQueue,
		RealtimeAudioWorkers:  defaultRealtimeAudioWorkers,
		RealtimeMaxSessions:   defaultRealtimeMaxSessions,
		VideoWorkers:          defaultVideoWorkers,
		AudioWorkers:          defaultAudioWorkers,
		AuthToken:             os.Getenv("GATEWAY_AUTH_TOKEN"),
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

	if raw, ok := os.LookupEnv("GATEWAY_AUDIO_DETECTOR_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid GATEWAY_AUDIO_DETECTOR_TIMEOUT %q: %w", raw, err)
		}
		cfg.AudioDetectorTimeout = d
	}

	if raw, ok := os.LookupEnv("GATEWAY_WORKER_SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid GATEWAY_WORKER_SHUTDOWN_TIMEOUT %q: %w", raw, err)
		}
		cfg.WorkerShutdownTimeout = d
	}

	if raw, ok := os.LookupEnv("GATEWAY_WORKER_COUNT"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("invalid GATEWAY_WORKER_COUNT %q: must be a positive integer", raw)
		}
		cfg.WorkerCount = n
	}

	if raw, ok := os.LookupEnv("GATEWAY_WORKER_QUEUE_SIZE"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("invalid GATEWAY_WORKER_QUEUE_SIZE %q: must be a positive integer", raw)
		}
		cfg.WorkerQueueSize = n
	}

	if raw, ok := os.LookupEnv("GATEWAY_JOB_TTL"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid GATEWAY_JOB_TTL %q: %w", raw, err)
		}
		cfg.JobTTL = d
	}

	var err error
	if cfg.RealtimeMaxVideoQueue, err = positiveIntEnv("REALTIME_MAX_VIDEO_QUEUE", cfg.RealtimeMaxVideoQueue); err != nil {
		return Config{}, err
	}
	if cfg.RealtimeVideoWorkers, err = positiveIntEnv("REALTIME_VIDEO_WORKERS", cfg.RealtimeVideoWorkers); err != nil {
		return Config{}, err
	}
	if cfg.RealtimeMaxAudioQueue, err = positiveIntEnv("REALTIME_MAX_AUDIO_QUEUE", cfg.RealtimeMaxAudioQueue); err != nil {
		return Config{}, err
	}
	if cfg.RealtimeAudioWorkers, err = positiveIntEnv("REALTIME_AUDIO_WORKERS", cfg.RealtimeAudioWorkers); err != nil {
		return Config{}, err
	}
	if cfg.RealtimeMaxSessions, err = positiveIntEnv("REALTIME_MAX_SESSIONS", cfg.RealtimeMaxSessions); err != nil {
		return Config{}, err
	}
	if cfg.VideoWorkers, err = positiveIntEnv("GATEWAY_VIDEO_WORKERS", cfg.VideoWorkers); err != nil {
		return Config{}, err
	}
	if cfg.AudioWorkers, err = positiveIntEnv("GATEWAY_AUDIO_WORKERS", cfg.AudioWorkers); err != nil {
		return Config{}, err
	}

	if _, err := strconv.Atoi(cfg.Port); err != nil {
		return Config{}, fmt.Errorf("invalid GATEWAY_PORT %q: must be numeric", cfg.Port)
	}

	if _, err := url.ParseRequestURI(cfg.DetectorBaseURL); err != nil {
		return Config{}, fmt.Errorf("invalid GATEWAY_DETECTOR_URL %q: %w", cfg.DetectorBaseURL, err)
	}

	if _, err := url.ParseRequestURI(cfg.AudioDetectorBaseURL); err != nil {
		return Config{}, fmt.Errorf("invalid GATEWAY_AUDIO_DETECTOR_URL %q: %w", cfg.AudioDetectorBaseURL, err)
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("GATEWAY_DATABASE_URL is required (see deployments/docker/.env.example)")
	}
	if _, err := url.ParseRequestURI(cfg.DatabaseURL); err != nil {
		return Config{}, fmt.Errorf("invalid GATEWAY_DATABASE_URL %q: %w", cfg.DatabaseURL, err)
	}

	if _, err := url.ParseRequestURI(cfg.RedisURL); err != nil {
		return Config{}, fmt.Errorf("invalid GATEWAY_REDIS_URL %q: %w", cfg.RedisURL, err)
	}

	if cfg.AuthToken == "" {
		return Config{}, errors.New("GATEWAY_AUTH_TOKEN is required (see deployments/docker/.env.example)")
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

// positiveIntEnv reads key as a positive integer, returning fallback
// unchanged if key isn't set.
func positiveIntEnv(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid %s %q: must be a positive integer", key, raw)
	}
	return n, nil
}
