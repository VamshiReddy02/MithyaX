// Package httpserver assembles the gateway's HTTP server and routes.
package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/middleware"
	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

// Server bundles the HTTP server with the background job pool (and the
// Redis client it depends on) so callers can shut both down together.
type Server struct {
	HTTP  *http.Server
	Pool  *worker.Pool
	Redis *redis.Client
}

// New builds the gateway's HTTP server and its Redis-backed analyze job
// pool, ready to be run.
func New(cfg config.Config, logger *slog.Logger) *Server {
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(middleware.Logging(logger), middleware.CORS(), gin.Recovery())

	detectorClient := detector.NewClient(cfg.DetectorBaseURL, cfg.DetectorTimeout)
	signalingHub := websocket.NewHub()

	redisClient := redis.NewClient(&redis.Options{
		Addr:        cfg.RedisAddr,
		DialTimeout: 3 * time.Second,
		MaxRetries:  2,
	})
	jobQueue := worker.NewQueue(redisClient, cfg.WorkerQueueSize)
	jobStore := worker.NewStore(redisClient, cfg.JobTTL)
	pool := worker.NewPool(jobQueue, jobStore, detectorClient, logger)
	pool.Start(cfg.WorkerCount)

	router.GET("/health", handlers.Health)

	v1 := router.Group("/api/v1")
	v1.POST("/analyze", handlers.NewAnalyze(pool))
	v1.GET("/analyze/:id", handlers.NewJobStatus(jobStore))
	v1.POST("/analyze-frame", handlers.NewAnalyzeFrame(detectorClient))
	v1.GET("/ws", handlers.NewWebSocket(signalingHub, logger))

	return &Server{
		HTTP: &http.Server{
			Addr:    cfg.Addr(),
			Handler: router,
		},
		Pool:  pool,
		Redis: redisClient,
	}
}
