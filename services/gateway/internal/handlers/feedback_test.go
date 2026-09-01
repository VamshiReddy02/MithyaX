package handlers_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
)

func newSessionFeedbackRouter(logger *slog.Logger) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/sessions/:id/feedback", handlers.NewSessionFeedback(logger))
	return router
}

func TestSessionFeedback_Success(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	router := newSessionFeedbackRouter(logger)

	body, _ := json.Marshal(handlers.SessionFeedbackRequest{Useful: true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/abc-123/feedback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "detection feedback") {
		t.Errorf("log output missing \"detection feedback\": %s", logged)
	}
	if !strings.Contains(logged, "abc-123") {
		t.Errorf("log output missing session id: %s", logged)
	}
	if !strings.Contains(logged, `"useful":true`) {
		t.Errorf("log output missing useful=true: %s", logged)
	}
}

func TestSessionFeedback_InvalidBody(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := newSessionFeedbackRouter(logger)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/abc-123/feedback", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
