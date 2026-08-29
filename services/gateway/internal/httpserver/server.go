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
	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/database"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/middleware"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
	ourredis "github.com/vamshireddy02/mithyax/gateway/internal/redis"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
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
	router.Use(middleware.Logging(logger), middleware.CORS(), gin.Recovery())

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
	jobQueue := worker.NewQueue(redisClient.Client, cfg.WorkerQueueSize)
	jobStore := worker.NewStore(redisClient.Client, cfg.JobTTL)
	pool := worker.NewPool(jobQueue, jobStore, detectorClient, logger)
	pool.Start(cfg.WorkerCount)

	// Phase 7.5's async analysis worker pools — separate queues, keys,
	// and Handlers for video vs. audio, per the "Video Queue → Worker
	// 1/2/3, Audio Queue → Worker 1/2" architecture. Not yet fed by any
	// HTTP route (nothing enqueues an AnalysisJob today); they run ready
	// and idle until a future producer exists, the same way this phase
	// was scoped.
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

	videoHandler := analysisworker.NewVideoHandler(detectorClient, analysisRepo)
	audioHandler := analysisworker.NewAudioHandler(analysisworker.NewHTTPAudioFetcher(), audioClient, analysisRepo)
	videoWorkers := analysisworker.NewPool(videoQueue, videoHandler, cfg.VideoWorkers, logger)
	audioWorkers := analysisworker.NewPool(audioQueue, audioHandler, cfg.AudioWorkers, logger)

	workersCtx, stopWorkers := context.WithCancel(context.Background())
	videoWorkers.Start(workersCtx)
	audioWorkers.Start(workersCtx)

	temporalAnalyzer := temporal.NewAnalyzer()
	sessionService := session.NewService(detectorClient, audioClient, temporalAnalyzer, cfg.DetectorTimeout, cfg.AudioDetectorTimeout)
	riskEngine := risk.NewEngine()
	realtimeCfg := realtime.Config{
		MaxVideoQueue: cfg.RealtimeMaxVideoQueue,
		VideoWorkers:  cfg.RealtimeVideoWorkers,
		MaxAudioQueue: cfg.RealtimeMaxAudioQueue,
		AudioWorkers:  cfg.RealtimeAudioWorkers,
		MaxSessions:   cfg.RealtimeMaxSessions,
	}
	liveSessionStore := realtime.NewStore(detectorClient, audioClient, temporalAnalyzer, riskEngine, realtimeCfg)

	router.GET("/health", handlers.NewHealth(db, redisClient))

	v1 := router.Group("/api/v1")
	v1.POST("/analyze", handlers.NewAnalyze(pool))
	v1.GET("/analyze/:id", handlers.NewJobStatus(jobStore))
	v1.POST("/analyze-frame", handlers.NewAnalyzeFrame(detectorClient))
	v1.POST("/analyze-audio", handlers.NewAnalyzeAudio(audioClient))
	v1.POST("/analyze-session", handlers.NewAnalyzeSession(sessionService, riskEngine))
	v1.GET("/ws", handlers.NewWebSocket(signalingHub, logger))
	v1.POST("/sessions", handlers.NewCreateSession(liveSessionStore, sessionRepo))
	v1.GET("/sessions/ws", handlers.NewSessionWebSocket(liveSessionStore, sessionRepo, analysisRepo, logger))
	v1.GET("/sessions/metrics", handlers.NewSessionMetrics(liveSessionStore))
	v1.GET("/sessions/:id/analysis", handlers.NewGetAnalysisResult(analysisRepo))

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
