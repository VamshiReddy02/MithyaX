package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
)

type fakeDetectorClient struct {
	called bool
	result *detector.Result
	err    error
}

func (f *fakeDetectorClient) Analyze(ctx context.Context, videoURL string) (*detector.Result, error) {
	f.called = true
	return f.result, f.err
}

func newAnalyzeRouter(client handlers.DetectorClient) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/analyze", handlers.NewAnalyze(client))
	return router
}

func doAnalyze(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyze", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAnalyze_Success(t *testing.T) {
	fake := &fakeDetectorClient{result: &detector.Result{
		Video:         "clip.mp4",
		Frames:        120,
		FacesDetected: 100,
		FakeScore:     0.83,
		Verdict:       "fake",
	}}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !fake.called {
		t.Error("expected DetectorClient.Analyze to be called")
	}

	var body handlers.AnalyzeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.ID == "" {
		t.Error("ID = empty, want non-empty")
	}
	if body.VideoURL != "https://example.com/video.mp4" {
		t.Errorf("VideoURL = %q, want %q", body.VideoURL, "https://example.com/video.mp4")
	}
	if body.Result.Verdict != "fake" {
		t.Errorf("Result.Verdict = %q, want %q", body.Result.Verdict, "fake")
	}
	if body.Result.FakeScore != 0.83 {
		t.Errorf("Result.FakeScore = %v, want %v", body.Result.FakeScore, 0.83)
	}
}

func TestAnalyze_MissingVideoURL(t *testing.T) {
	fake := &fakeDetectorClient{}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if fake.called {
		t.Error("DetectorClient.Analyze should not be called for an invalid request")
	}
}

func TestAnalyze_InvalidVideoURL(t *testing.T) {
	fake := &fakeDetectorClient{}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"not-a-url"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if fake.called {
		t.Error("DetectorClient.Analyze should not be called for an invalid request")
	}
}

func TestAnalyze_MalformedJSON(t *testing.T) {
	fake := &fakeDetectorClient{}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestAnalyze_DetectorInvalidVideo(t *testing.T) {
	fake := &fakeDetectorClient{err: &detector.Error{Kind: detector.KindInvalidVideo, Message: "could not open video"}}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/broken.mp4"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
}

func TestAnalyze_DetectorTimeout(t *testing.T) {
	fake := &fakeDetectorClient{err: &detector.Error{Kind: detector.KindTimeout, Message: "timed out"}}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusGatewayTimeout, rec.Body.String())
	}
}

func TestAnalyze_DetectorUnavailable(t *testing.T) {
	fake := &fakeDetectorClient{err: &detector.Error{Kind: detector.KindUnavailable, Message: "unreachable"}}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}

func TestAnalyze_DetectorError(t *testing.T) {
	fake := &fakeDetectorClient{err: &detector.Error{Kind: detector.KindDetectorError, Message: "boom"}}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}
