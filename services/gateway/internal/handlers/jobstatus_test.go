package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

type fakeJobLookup struct {
	jobs map[string]worker.Job
	err  error
}

func (f *fakeJobLookup) Get(ctx context.Context, id string) (worker.Job, error) {
	if f.err != nil {
		return worker.Job{}, f.err
	}
	job, ok := f.jobs[id]
	if !ok {
		return worker.Job{}, worker.ErrJobNotFound
	}
	return job, nil
}

func newJobStatusRouter(lookup handlers.JobLookup) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/analyze/:id", handlers.NewJobStatus(lookup))
	return router
}

func doGetStatus(t *testing.T, router *gin.Engine, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/analyze/"+id, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestJobStatus_Queued(t *testing.T) {
	lookup := &fakeJobLookup{jobs: map[string]worker.Job{
		"job-1": {ID: "job-1", VideoURL: "https://example.com/v.mp4", Status: worker.StatusQueued},
	}}
	router := newJobStatusRouter(lookup)

	rec := doGetStatus(t, router, "job-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body handlers.JobStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Status != string(worker.StatusQueued) {
		t.Errorf("Status = %q, want %q", body.Status, worker.StatusQueued)
	}
	if body.Result != nil {
		t.Errorf("Result = %+v, want nil while queued", body.Result)
	}
}

func TestJobStatus_Completed(t *testing.T) {
	result := &detector.Result{Verdict: "real", FakeScore: 0.05}
	lookup := &fakeJobLookup{jobs: map[string]worker.Job{
		"job-2": {ID: "job-2", VideoURL: "https://example.com/v.mp4", Status: worker.StatusCompleted, Result: result},
	}}
	router := newJobStatusRouter(lookup)

	rec := doGetStatus(t, router, "job-2")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body handlers.JobStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Status != string(worker.StatusCompleted) {
		t.Errorf("Status = %q, want %q", body.Status, worker.StatusCompleted)
	}
	if body.Result == nil || body.Result.Verdict != "real" {
		t.Errorf("Result = %+v, want Verdict=real", body.Result)
	}
}

func TestJobStatus_Failed(t *testing.T) {
	lookup := &fakeJobLookup{jobs: map[string]worker.Job{
		"job-3": {ID: "job-3", Status: worker.StatusFailed, Error: "video-detector unreachable"},
	}}
	router := newJobStatusRouter(lookup)

	rec := doGetStatus(t, router, "job-3")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body handlers.JobStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Status != string(worker.StatusFailed) {
		t.Errorf("Status = %q, want %q", body.Status, worker.StatusFailed)
	}
	if body.Error != "video-detector unreachable" {
		t.Errorf("Error = %q, want %q", body.Error, "video-detector unreachable")
	}
}

func TestJobStatus_NotFound(t *testing.T) {
	lookup := &fakeJobLookup{jobs: map[string]worker.Job{}}
	router := newJobStatusRouter(lookup)

	rec := doGetStatus(t, router, "does-not-exist")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestJobStatus_RedisUnavailable(t *testing.T) {
	lookup := &fakeJobLookup{err: worker.ErrRedisUnavailable}
	router := newJobStatusRouter(lookup)

	rec := doGetStatus(t, router, "job-1")

	// Distinct from 404: we can't tell whether the job exists, not that
	// it doesn't.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}
