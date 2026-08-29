package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
)

// fakeHealthChecker is a minimal handlers.HealthChecker, standing in for
// *database.DB or *redis.Client so this test doesn't need either
// running.
type fakeHealthChecker struct {
	err error
}

func (f *fakeHealthChecker) HealthCheck(ctx context.Context) error {
	return f.err
}

func newHealthRouter(postgres, redis handlers.HealthChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/health", handlers.NewHealth(postgres, redis))
	return router
}

func TestHealth_AllHealthy(t *testing.T) {
	router := newHealthRouter(&fakeHealthChecker{}, &fakeHealthChecker{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body handlers.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Status != "healthy" {
		t.Errorf("Status = %q, want %q", body.Status, "healthy")
	}
	if body.Checks["postgres"] != "healthy" || body.Checks["redis"] != "healthy" {
		t.Errorf("Checks = %+v, want both healthy", body.Checks)
	}
}

// TestHealth_PostgresDown proves the endpoint reports which dependency
// is down rather than just failing opaquely — and that a downstream ML
// service (video-detector, audio-detector) is never part of this check
// at all, per the "don't couple health to every downstream service" rule.
func TestHealth_PostgresDown(t *testing.T) {
	router := newHealthRouter(&fakeHealthChecker{err: errors.New("connection refused")}, &fakeHealthChecker{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}

	var body handlers.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Status != "unhealthy" {
		t.Errorf("Status = %q, want %q", body.Status, "unhealthy")
	}
	if body.Checks["postgres"] != "connection refused" {
		t.Errorf(`Checks["postgres"] = %q, want the underlying error message`, body.Checks["postgres"])
	}
	if body.Checks["redis"] != "healthy" {
		t.Errorf(`Checks["redis"] = %q, want "healthy" — one dependency failing shouldn't hide the other's status`, body.Checks["redis"])
	}
}

func TestHealth_RedisDown(t *testing.T) {
	router := newHealthRouter(&fakeHealthChecker{}, &fakeHealthChecker{err: errors.New("dial tcp: connection refused")})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}

	var body handlers.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Checks["postgres"] != "healthy" {
		t.Errorf(`Checks["postgres"] = %q, want "healthy"`, body.Checks["postgres"])
	}
	if body.Checks["redis"] != "dial tcp: connection refused" {
		t.Errorf(`Checks["redis"] = %q, want the underlying error message`, body.Checks["redis"])
	}
}
