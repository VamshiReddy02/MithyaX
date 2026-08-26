package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

type fakeJobSubmitter struct {
	called   bool
	videoURL string
	job      worker.Job
	err      error
}

func (f *fakeJobSubmitter) Submit(videoURL string) (worker.Job, error) {
	f.called = true
	f.videoURL = videoURL
	return f.job, f.err
}

func newAnalyzeRouter(submitter handlers.JobSubmitter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/analyze", handlers.NewAnalyze(submitter))
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

func TestAnalyze_EnqueuesAndReturnsQueued(t *testing.T) {
	fake := &fakeJobSubmitter{job: worker.Job{
		ID:       "job-1",
		VideoURL: "https://example.com/video.mp4",
		Status:   worker.StatusQueued,
	}}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !fake.called {
		t.Error("expected JobSubmitter.Submit to be called")
	}
	if fake.videoURL != "https://example.com/video.mp4" {
		t.Errorf("Submit() called with %q, want %q", fake.videoURL, "https://example.com/video.mp4")
	}

	var body handlers.AnalyzeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.ID != "job-1" {
		t.Errorf("ID = %q, want %q", body.ID, "job-1")
	}
	if body.Status != string(worker.StatusQueued) {
		t.Errorf("Status = %q, want %q", body.Status, worker.StatusQueued)
	}
}

func TestAnalyze_MissingVideoURL(t *testing.T) {
	fake := &fakeJobSubmitter{}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if fake.called {
		t.Error("JobSubmitter.Submit should not be called for an invalid request")
	}
}

func TestAnalyze_InvalidVideoURL(t *testing.T) {
	fake := &fakeJobSubmitter{}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"not-a-url"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if fake.called {
		t.Error("JobSubmitter.Submit should not be called for an invalid request")
	}
}

func TestAnalyze_MalformedJSON(t *testing.T) {
	fake := &fakeJobSubmitter{}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestAnalyze_QueueFull(t *testing.T) {
	fake := &fakeJobSubmitter{err: worker.ErrQueueFull}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestAnalyze_PoolClosed(t *testing.T) {
	fake := &fakeJobSubmitter{err: worker.ErrPoolClosed}
	router := newAnalyzeRouter(fake)

	rec := doAnalyze(t, router, `{"video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}
