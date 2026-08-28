// Package httpserver assembles the gateway's HTTP server and routes.
package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/database"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/middleware"
	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/session"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

// Server bundles the HTTP server with the background job pool (and the
// Redis and PostgreSQL clients they depend on) so callers can shut all
// of it down together.
type Server struct {
	HTTP  *http.Server
	Pool  *worker.Pool
	Redis *redis.Client
	DB    *database.DB
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

	redisClient := redis.NewClient(&redis.Options{
		Addr:        cfg.RedisAddr,
		DialTimeout: 3 * time.Second,
		MaxRetries:  2,
	})

	db, err := database.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	sessionRepo := sessionrepo.NewPostgres(db.Pool)
	jobQueue := worker.NewQueue(redisClient, cfg.WorkerQueueSize)
	jobStore := worker.NewStore(redisClient, cfg.JobTTL)
	pool := worker.NewPool(jobQueue, jobStore, detectorClient, logger)
	pool.Start(cfg.WorkerCount)

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

	router.GET("/health", handlers.Health)

	v1 := router.Group("/api/v1")
	v1.POST("/analyze", handlers.NewAnalyze(pool))
	v1.GET("/analyze/:id", handlers.NewJobStatus(jobStore))
	v1.POST("/analyze-frame", handlers.NewAnalyzeFrame(detectorClient))
	v1.POST("/analyze-audio", handlers.NewAnalyzeAudio(audioClient))
	v1.POST("/analyze-session", handlers.NewAnalyzeSession(sessionService, riskEngine))
	v1.GET("/ws", handlers.NewWebSocket(signalingHub, logger))
	v1.POST("/sessions", handlers.NewCreateSession(liveSessionStore, sessionRepo))
	v1.GET("/sessions/ws", handlers.NewSessionWebSocket(liveSessionStore, sessionRepo, logger))
	v1.GET("/sessions/metrics", handlers.NewSessionMetrics(liveSessionStore))

	return &Server{
		HTTP: &http.Server{
			Addr:    cfg.Addr(),
			Handler: router,
		},
		Pool:  pool,
		Redis: redisClient,
		DB:    db,
	}, nil
}
