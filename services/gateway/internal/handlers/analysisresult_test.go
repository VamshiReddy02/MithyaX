package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
)

func newGetAnalysisResultRouter(repo *fakeAnalysisRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/sessions/:id/analysis", handlers.NewGetAnalysisResult(repo))
	return router
}

func TestGetAnalysisResult_Success(t *testing.T) {
	repo := newFakeAnalysisRepository()
	videoScore, audioScore := 0.8, 0.9
	repo.Create(context.Background(), analysisrepo.Result{
		SessionID:      "abc-123",
		VideoFakeScore: &videoScore,
		VideoVerdict:   "fake",
		AudioFakeScore: &audioScore,
		AudioVerdict:   "fake",
		RiskScore:      0.82,
		RiskVerdict:    "LIKELY_FAKE",
		RiskReasons:    []string{"Video signal indicates likely synthetic or manipulated content"},
	})
	router := newGetAnalysisResultRouter(repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/abc-123/analysis", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got analysisrepo.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "abc-123")
	}
	if got.RiskScore != 0.82 || got.RiskVerdict != "LIKELY_FAKE" {
		t.Errorf("RiskScore/RiskVerdict = %v/%q, want 0.82/LIKELY_FAKE", got.RiskScore, got.RiskVerdict)
	}
	if len(got.RiskReasons) != 1 {
		t.Errorf("RiskReasons = %v, want 1 reason", got.RiskReasons)
	}
}

func TestGetAnalysisResult_NotFound(t *testing.T) {
	router := newGetAnalysisResultRouter(newFakeAnalysisRepository())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/does-not-exist/analysis", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}
