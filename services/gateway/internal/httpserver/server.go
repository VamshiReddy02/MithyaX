// Package httpserver assembles the gateway's HTTP server and routes.
package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/middleware"
)

// New builds the gateway's HTTP server, ready to be run.
func New(cfg config.Config, logger *slog.Logger) *http.Server {
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(middleware.Logging(logger), gin.Recovery())

	detectorClient := detector.NewClient(cfg.DetectorBaseURL, cfg.DetectorTimeout)

	router.GET("/health", handlers.Health)

	v1 := router.Group("/api/v1")
	v1.POST("/analyze", handlers.NewAnalyze(detectorClient))

	return &http.Server{
		Addr:    cfg.Addr(),
		Handler: router,
	}
}
