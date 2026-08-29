package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// healthCheckTimeout bounds how long a single dependency's health check
// may take, so a wedged connection to Postgres or Redis can't hang the
// whole /health response indefinitely.
const healthCheckTimeout = 2 * time.Second

// HealthChecker reports whether a dependency is currently reachable.
// *database.DB and *redis.Client both implement it via their own
// HealthCheck methods — this interface is what lets NewHealth depend on
// neither package directly.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// HealthResponse is the JSON body returned by the health endpoint.
type HealthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// NewHealth builds the GET /health handler: reports whether the
// gateway's own dependencies — PostgreSQL and Redis — are reachable.
// Deliberately scoped to just those two, not every downstream ML
// service (video-detector, audio-detector): a slow or down detector is
// a real problem, but it shouldn't make an orchestrator decide the
// gateway process itself is unhealthy and needs restarting.
func NewHealth(postgres, redis HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), healthCheckTimeout)
		defer cancel()

		checks := make(map[string]string, 2)
		healthy := true

		if err := postgres.HealthCheck(ctx); err != nil {
			checks["postgres"] = err.Error()
			healthy = false
		} else {
			checks["postgres"] = "healthy"
		}

		if err := redis.HealthCheck(ctx); err != nil {
			checks["redis"] = err.Error()
			healthy = false
		} else {
			checks["redis"] = "healthy"
		}

		status := http.StatusOK
		overall := "healthy"
		if !healthy {
			status = http.StatusServiceUnavailable
			overall = "unhealthy"
		}

		c.JSON(status, HealthResponse{Status: overall, Checks: checks})
	}
}
