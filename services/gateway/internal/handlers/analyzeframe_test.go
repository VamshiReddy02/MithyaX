package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
)

type fakeFrameDetectorClient struct {
	called bool
	result *detector.FrameResult
	err    error
}

func (f *fakeFrameDetectorClient) AnalyzeFrame(ctx context.Context, jpeg []byte) (*detector.FrameResult, error) {
	f.called = true
	return f.result, f.err
}

func newAnalyzeFrameRouter(client handlers.FrameDetectorClient) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/analyze-frame", handlers.NewAnalyzeFrame(client))
	return router
}

func doAnalyzeFrame(t *testing.T, router *gin.Engine, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyze-frame", bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/jpeg")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAnalyzeFrame_Success(t *testing.T) {
	fake := &fakeFrameDetectorClient{result: &detector.FrameResult{
		FaceDetected:    true,
		FakeProbability: 0.91,
		Verdict:         "fake",
	}}
	router := newAnalyzeFrameRouter(fake)

	rec := doAnalyzeFrame(t, router, []byte("fake-jpeg-bytes"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !fake.called {
		t.Error("expected FrameDetectorClient.AnalyzeFrame to be called")
	}

	var body detector.FrameResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Verdict != "fake" || body.FakeProbability != 0.91 {
		t.Errorf("body = %+v, want Verdict=fake FakeProbability=0.91", body)
	}
}

func TestAnalyzeFrame_EmptyBody(t *testing.T) {
	fake := &fakeFrameDetectorClient{}
	router := newAnalyzeFrameRouter(fake)

	rec := doAnalyzeFrame(t, router, []byte{})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if fake.called {
		t.Error("AnalyzeFrame should not be called for an empty body")
	}
}

func TestAnalyzeFrame_TooLarge(t *testing.T) {
	fake := &fakeFrameDetectorClient{}
	router := newAnalyzeFrameRouter(fake)

	rec := doAnalyzeFrame(t, router, bytes.Repeat([]byte("x"), 2<<20+1))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if fake.called {
		t.Error("AnalyzeFrame should not be called for an oversized frame")
	}
}

func TestAnalyzeFrame_DetectorInvalidImage(t *testing.T) {
	fake := &fakeFrameDetectorClient{err: &detector.Error{Kind: detector.KindInvalidVideo, Message: "could not decode image"}}
	router := newAnalyzeFrameRouter(fake)

	rec := doAnalyzeFrame(t, router, []byte("garbage"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
}

func TestAnalyzeFrame_DetectorUnavailable(t *testing.T) {
	fake := &fakeFrameDetectorClient{err: &detector.Error{Kind: detector.KindUnavailable, Message: "unreachable"}}
	router := newAnalyzeFrameRouter(fake)

	rec := doAnalyzeFrame(t, router, []byte("fake-jpeg-bytes"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}

func TestAnalyzeFrame_NoFaceDetected(t *testing.T) {
	fake := &fakeFrameDetectorClient{result: &detector.FrameResult{FaceDetected: false, Verdict: "unknown"}}
	router := newAnalyzeFrameRouter(fake)

	rec := doAnalyzeFrame(t, router, []byte("fake-jpeg-bytes"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `"face_detected":false`) {
		t.Errorf("body = %s, want face_detected:false", rec.Body.String())
	}
}
