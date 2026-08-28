package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
)

type fakeSessionCreator struct {
	store *realtime.Store
	err   error
}

func (f *fakeSessionCreator) Create() (*realtime.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.store.Create()
}

func newCreateSessionRouter(creator handlers.SessionCreator, repo sessionrepo.Repository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/sessions", handlers.NewCreateSession(creator, repo))
	return router
}

func TestCreateSession_Success(t *testing.T) {
	store := realtime.NewStore(&fakeRealtimeVideoAnalyzer{}, &fakeRealtimeAudioAnalyzer{}, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, realtime.DefaultConfig)
	router := newCreateSessionRouter(&fakeSessionCreator{store: store}, newFakeSessionRepository())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var body struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.ID == "" {
		t.Error("id is empty")
	}
	if body.Status != "active" {
		t.Errorf("status = %q, want %q", body.Status, "active")
	}
}

func TestCreateSession_StoreError(t *testing.T) {
	router := newCreateSessionRouter(&fakeSessionCreator{err: errors.New("boom")}, newFakeSessionRepository())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

func TestCreateSession_TooManySessions(t *testing.T) {
	router := newCreateSessionRouter(&fakeSessionCreator{err: realtime.ErrTooManySessions}, newFakeSessionRepository())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

// TestCreateSession_RepositoryError proves a failed persistence write
// fails the request even though the in-memory session was already
// created — POST /api/v1/sessions is meant to guarantee a durable
// record exists by the time it returns 201.
func TestCreateSession_RepositoryError(t *testing.T) {
	store := realtime.NewStore(&fakeRealtimeVideoAnalyzer{}, &fakeRealtimeAudioAnalyzer{}, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, realtime.DefaultConfig)
	repo := newFakeSessionRepository()
	repo.createErr = errors.New("connection refused")
	router := newCreateSessionRouter(&fakeSessionCreator{store: store}, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

func newSessionMetricsRouter(store handlers.SessionMetricsProvider) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/sessions/metrics", handlers.NewSessionMetrics(store))
	return router
}

func TestSessionMetrics_ReturnsSnapshot(t *testing.T) {
	store := realtime.NewStore(&fakeRealtimeVideoAnalyzer{}, &fakeRealtimeAudioAnalyzer{}, &fakeRealtimeTemporalAnalyzer{}, &fakeRealtimeRiskEngine{}, realtime.DefaultConfig)
	router := newSessionMetricsRouter(store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/metrics", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var snapshot realtime.MetricsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if snapshot.FramesReceived != 0 || snapshot.AudioChunksReceived != 0 {
		t.Errorf("snapshot = %+v, want all-zero for a store with no sessions yet", snapshot)
	}
}
