// Package httpserver assembles the gateway's HTTP server and routes.
package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisworker"
	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/auth"
	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/database"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/middleware"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
	"github.com/vamshireddy02/mithyax/gateway/internal/ratelimit"
	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
	ourredis "github.com/vamshireddy02/mithyax/gateway/internal/redis"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
	"github.com/vamshireddy02/mithyax/gateway/internal/session"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

// asyncQueueCapacity bounds the video/audio job queues (7.5.4). Not
// yet configurable — GATEWAY_VIDEO_WORKERS/GATEWAY_AUDIO_WORKERS
// control concurrency, which is what the ticket actually asked for;
// queue depth gets its own setting only once real usage says the
// default needs tuning.
const asyncQueueCapacity = 1000

// Requests-per-minute ceilings for Phase 7.7.3's rate limiter.
// defaultRateLimit covers every /api/v1 route by default;
// analysisCreateRateLimit is layered on top of (not instead of) that
// default specifically for POST /api/v1/analysis, the one route
// expensive enough (it creates async jobs that go on to call a
// Python detector) to warrant a stricter ceiling of its own.
const (
	defaultRateLimit        = 60
	analysisCreateRateLimit = 10
)

// Request body size ceilings (7.7.6). maxRequestBodyBytes is a
// generous, whole-API safety net against memory exhaustion from a
// pathologically large body on any route — well above the largest
// legitimate upload today (analyze-audio's own 25MiB check, see
// internal/handlers/analyzeaudio.go), so it never interferes with
// real traffic. maxJSONRequestBodyBytes is layered on top of that,
// tighter, for routes that only ever expect a small JSON object — a
// session_id and up to two URLs has no business needing more than a
// few KB, so anything past this is already suspicious regardless of
// what SafeFetcher or the URL-length check further down would
// eventually catch.
const (
	maxRequestBodyBytes     = 64 << 20 // 64MiB
	maxJSONRequestBodyBytes = 16 << 10 // 16KiB
)

// Server bundles the HTTP server with the background job pools (and the
// Redis and PostgreSQL clients they depend on) so callers can shut all
// of it down together.
type Server struct {
	HTTP  *http.Server
	Pool  *worker.Pool
	Redis *ourredis.Client
	DB    *database.DB

	// VideoWorkers and AudioWorkers are Phase 7.5's async analysis job
	// pools — entirely separate from Pool above (the older,
	// synchronous-ish pipeline behind POST /api/v1/analyze) and from
	// internal/realtime's live WebSocket pipeline. See StopWorkers.
	VideoWorkers *analysisworker.Pool
	AudioWorkers *analysisworker.Pool

	stopWorkers context.CancelFunc
}

// StopWorkers gracefully stops the async video/audio worker pools —
// separate from Pool.Shutdown (the older synchronous-analyze job pool)
// since the two systems have independent lifecycles. Waits for any
// in-flight job to finish (bounded by its own per-job timeout).
func (s *Server) StopWorkers() {
	s.stopWorkers()
	s.VideoWorkers.Stop()
	s.AudioWorkers.Stop()
}

// New builds the gateway's HTTP server and its Redis-backed analyze job
// pool, ready to be run. It does not run database migrations or verify
// PostgreSQL is reachable — see database.DB.Migrate, called separately
// from cmd/gateway so that building a Server (as httpserver's own tests
// do, with no real database configured) never requires one.
func New(cfg config.Config, logger *slog.Logger) (*Server, error) {
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	// MaxBodyBytes runs first, before anything else even starts reading
	// the request — a whole-API safety net (7.7.6) against an
	// oversized body regardless of route.
	router.Use(middleware.MaxBodyBytes(maxRequestBodyBytes), middleware.Logging(logger), middleware.CORS(), gin.Recovery())

	detectorClient := detector.NewClient(cfg.DetectorBaseURL, cfg.DetectorTimeout)
	audioClient := audio.NewClient(cfg.AudioDetectorBaseURL, cfg.AudioDetectorTimeout)
	signalingHub := websocket.NewHub()

	redisClient, err := ourredis.New(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("build redis client: %w", err)
	}

	db, err := database.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	sessionRepo := sessionrepo.NewPostgres(db.Pool)
	analysisRepo := analysisrepo.NewPostgres(db.Pool)
	jobsRepo := jobsrepo.NewPostgres(db.Pool)
	jobQueue := worker.NewQueue(redisClient.Client, cfg.WorkerQueueSize)
	jobStore := worker.NewStore(redisClient.Client, cfg.JobTTL)
	pool := worker.NewPool(jobQueue, jobStore, detectorClient, logger)
	pool.Start(cfg.WorkerCount)

	// Phase 7.5's async analysis worker pools — separate queues, keys,
	// and Handlers for video vs. audio, per the "Video Queue → Worker
	// 1/2/3, Audio Queue → Worker 1/2" architecture. Fed by
	// POST /api/v1/analysis (7.6.1), below.
	videoQueue := queue.NewRedis(redisClient.Client, "mithyax:jobs:video_analysis", asyncQueueCapacity)
	audioQueue := queue.NewRedis(redisClient.Client, "mithyax:jobs:audio_analysis", asyncQueueCapacity)

	// Recover anything a previous, ungracefully-terminated run left
	// stranded mid-processing (see queue.Redis.RecoverStale) before
	// workers start pulling new jobs — a crash shouldn't leave a job
	// merely inspectable forever when it could just be reprocessed.
	if n, err := videoQueue.RecoverStale(context.Background()); err != nil {
		logger.Warn("failed to recover stale video jobs", slog.String("error", err.Error()))
	} else if n > 0 {
		logger.Info("recovered stale video jobs from a previous run", slog.Int("count", n))
	}
	if n, err := audioQueue.RecoverStale(context.Background()); err != nil {
		logger.Warn("failed to recover stale audio jobs", slog.String("error", err.Error()))
	} else if n > 0 {
		logger.Info("recovered stale audio jobs from a previous run", slog.Int("count", n))
	}

	// SSRF protection (7.7.4/7.7.5): one Validator shared by the API
	// boundary (POST /api/v1/analysis, below) and every worker fetch —
	// see internal/security's package doc for why both matter. Both
	// VideoHandler and AudioHandler now fetch their own bytes through a
	// SafeFetcher-backed SafeURLFetcher rather than handing a URL to a
	// Python service to fetch itself: the video-detector's
	// /analyze-upload endpoint (as opposed to its older, still-present
	// /analyze) takes bytes precisely so this worker is the only thing
	// that ever makes an outbound request to a client-supplied URL.
	urlValidator := security.NewValidator()
	safeFetcher := security.NewSafeFetcher(urlValidator, security.Config{})

	completionCoordinator := analysisworker.NewCoordinator(jobsRepo)
	videoFetcher := analysisworker.NewSafeURLFetcher(safeFetcher, analysisworker.MaxVideoFetchBytes, []string{"video/"})
	videoHandler := analysisworker.NewVideoHandler(videoFetcher, detectorClient, analysisRepo, completionCoordinator)
	audioFetcher := analysisworker.NewSafeURLFetcher(safeFetcher, analysisworker.MaxAudioFetchBytes, []string{"audio/"})
	audioHandler := analysisworker.NewAudioHandler(audioFetcher, audioClient, analysisRepo, completionCoordinator)
	videoWorkers := analysisworker.NewPool(videoQueue, videoHandler, jobsRepo, cfg.VideoWorkers, logger)
	audioWorkers := analysisworker.NewPool(audioQueue, audioHandler, jobsRepo, cfg.AudioWorkers, logger)

	workersCtx, stopWorkers := context.WithCancel(context.Background())
	videoWorkers.Start(workersCtx)
	audioWorkers.Start(workersCtx)

	temporalAnalyzer := temporal.NewAnalyzer()
	sessionService := session.NewService(detectorClient, audioClient, temporalAnalyzer, cfg.DetectorTimeout, cfg.AudioDetectorTimeout)
	riskEngine := risk.NewEngine()
	realtimeCfg := realtime.Config{
		MaxVideoQueue:      cfg.RealtimeMaxVideoQueue,
		VideoWorkers:       cfg.RealtimeVideoWorkers,
		MaxAudioQueue:      cfg.RealtimeMaxAudioQueue,
		AudioWorkers:       cfg.RealtimeAudioWorkers,
		MaxSessions:        cfg.RealtimeMaxSessions,
		MaxSessionDuration: cfg.RealtimeMaxSessionDuration,
		MaxFrames:          cfg.RealtimeMaxFrames,
		MaxAudioChunks:     cfg.RealtimeMaxAudioChunks,
	}
	liveSessionStore := realtime.NewStore(detectorClient, audioClient, temporalAnalyzer, riskEngine, realtimeCfg)

	// /health stays outside auth.Middleware — deliberately public so a
	// Kubernetes liveness/readiness probe or load balancer health check
	// can call it without a token (7.7.1).
	router.GET("/health", handlers.NewHealth(db, redisClient))

	// Authentication (who is this caller), authorization (what can they
	// do), and rate limiting (how much can they do it) stay separate
	// middleware stages in that order (7.7.1/7.7.2/7.7.3):
	//   auth.Middleware -> auth.RequireRole -> ratelimit.Middleware -> handler
	// Rate limiting runs after authentication specifically so an
	// unauthenticated request gets 401, never 429 — auth.Middleware
	// already rejects it before ratelimit.Middleware ever sees it.
	// Redis-backed (internal/ratelimit) rather than in-memory so the
	// limit stays meaningful if the gateway ever runs multiple replicas.
	limiter := ratelimit.New(redisClient.Client)
	v1 := router.Group("/api/v1")
	v1.Use(auth.Middleware(map[string]auth.Role{
		cfg.AuthToken:      auth.RoleUser,
		cfg.AdminAuthToken: auth.RoleAdmin,
	}))
	v1.Use(ratelimit.Middleware(limiter, "default", defaultRateLimit, logger))
	v1.POST("/analyze", handlers.NewAnalyze(pool))
	v1.GET("/analyze/:id", handlers.NewJobStatus(jobStore))
	v1.POST("/analyze-frame", handlers.NewAnalyzeFrame(detectorClient))
	v1.POST("/analyze-audio", handlers.NewAnalyzeAudio(audioClient))
	v1.POST("/analyze-session", handlers.NewAnalyzeSession(sessionService, riskEngine))
	v1.GET("/ws", handlers.NewWebSocket(signalingHub, logger))
	v1.POST("/sessions", handlers.NewCreateSession(liveSessionStore, sessionRepo))
	v1.GET("/sessions/ws", handlers.NewSessionWebSocket(liveSessionStore, sessionRepo, analysisRepo, logger))
	v1.GET("/sessions/metrics", auth.RequireRole(auth.RoleAdmin), handlers.NewSessionMetrics(liveSessionStore))
	v1.GET("/sessions/:id/analysis", handlers.NewGetAnalysisResult(analysisRepo))
	// POST /analysis gets a stricter rate limit layered on top of the
	// group's default 60/min — it creates async jobs that go on to call
	// a Python detector, so it's worth capping harder than a cheap GET —
	// and a much tighter body-size limit (7.7.6) than the whole-API one,
	// since its body is never more than a session_id and up to two URLs.
	v1.POST("/analysis",
		ratelimit.Middleware(limiter, "analysis-create", analysisCreateRateLimit, logger),
		middleware.MaxBodyBytes(maxJSONRequestBodyBytes),
		handlers.NewCreateAnalysisJob(videoQueue, audioQueue, jobsRepo, urlValidator, logger),
	)
	v1.GET("/analysis/jobs/:id", handlers.NewGetAnalysisJob(jobsRepo))

	return &Server{
		HTTP: &http.Server{
			Addr:    cfg.Addr(),
			Handler: router,
		},
		Pool:         pool,
		Redis:        redisClient,
		DB:           db,
		VideoWorkers: videoWorkers,
		AudioWorkers: audioWorkers,
		stopWorkers:  stopWorkers,
	}, nil
}
