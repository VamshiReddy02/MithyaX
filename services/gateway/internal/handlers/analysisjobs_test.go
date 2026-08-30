package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
)

// permissiveResolver treats every hostname as resolving to a fixed
// public IP — these handler tests aren't about SSRF validation itself
// (see internal/security's own exhaustive tests for that), they just
// need NewCreateAnalysisJob's mandatory video_url/audio_url check
// (7.7.5) to pass for whatever example.com-style URL a case uses.
type permissiveResolver struct{}

func (permissiveResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
}

func permissiveValidator() *security.Validator {
	return security.NewValidator(security.WithResolver(permissiveResolver{}))
}

// fakeAnalysisQueue is a minimal in-memory queue.Queue for
// handler-level tests of POST /api/v1/analysis — these only care that
// the handler enqueues (or doesn't) the right thing, not about real
// queue mechanics (see internal/queue's own tests for that).
type fakeAnalysisQueue struct {
	mu         sync.Mutex
	enqueued   []queue.Job
	enqueueErr error
}

func (q *fakeAnalysisQueue) Enqueue(ctx context.Context, job queue.Job) error {
	if q.enqueueErr != nil {
		return q.enqueueErr
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enqueued = append(q.enqueued, job)
	return nil
}

func (q *fakeAnalysisQueue) Dequeue(ctx context.Context) (queue.Delivery, error) {
	panic("not used by these tests")
}

func (q *fakeAnalysisQueue) Ack(ctx context.Context, d queue.Delivery) error {
	panic("not used by these tests")
}

func (q *fakeAnalysisQueue) Fail(ctx context.Context, d queue.Delivery, reason string) error {
	panic("not used by these tests")
}

func (q *fakeAnalysisQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.enqueued)
}

// fakeAnalysisJobsRepo is a minimal in-memory jobsrepo.Repository for
// these handler tests — see internal/repository/jobs/postgres_test.go
// for tests against the real implementation, and
// internal/analysisworker's own fakeJobsRepo for the worker-side
// equivalent (duplicated rather than shared across two test packages).
type fakeAnalysisJobsRepo struct {
	mu        sync.Mutex
	jobs      map[string]jobsrepo.Job
	createErr error
}

func newFakeAnalysisJobsRepo() *fakeAnalysisJobsRepo {
	return &fakeAnalysisJobsRepo{jobs: make(map[string]jobsrepo.Job)}
}

func (f *fakeAnalysisJobsRepo) Create(ctx context.Context, job jobsrepo.Job) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[job.ID] = job
	return nil
}

func (f *fakeAnalysisJobsRepo) Get(ctx context.Context, id string) (*jobsrepo.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return nil, jobsrepo.ErrNotFound
	}
	return &j, nil
}

func (f *fakeAnalysisJobsRepo) GetLatestBySessionAndType(ctx context.Context, sessionID, jobType string) (*jobsrepo.Job, error) {
	return nil, jobsrepo.ErrNotFound
}

func (f *fakeAnalysisJobsRepo) MarkProcessing(ctx context.Context, id string, attempt int) error {
	return f.update(id, func(j *jobsrepo.Job) { j.Status = jobsrepo.StatusProcessing; j.Attempt = attempt })
}

func (f *fakeAnalysisJobsRepo) MarkCompleted(ctx context.Context, id string) error {
	return f.update(id, func(j *jobsrepo.Job) { j.Status = jobsrepo.StatusCompleted })
}

func (f *fakeAnalysisJobsRepo) MarkFailed(ctx context.Context, id string, attempt int, lastError string) error {
	return f.update(id, func(j *jobsrepo.Job) { j.Status = jobsrepo.StatusFailed; j.Attempt = attempt; j.LastError = lastError })
}

func (f *fakeAnalysisJobsRepo) MarkDeadLetter(ctx context.Context, id string, lastError string) error {
	return f.update(id, func(j *jobsrepo.Job) { j.Status = jobsrepo.StatusDeadLetter; j.LastError = lastError })
}

func (f *fakeAnalysisJobsRepo) update(id string, mutate func(*jobsrepo.Job)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return jobsrepo.ErrNotFound
	}
	mutate(&j)
	f.jobs[id] = j
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newCreateAnalysisRouter(videoQueue, audioQueue queue.Queue, jobs jobsrepo.Repository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/analysis", handlers.NewCreateAnalysisJob(videoQueue, audioQueue, jobs, permissiveValidator(), testLogger()))
	router.GET("/api/v1/analysis/jobs/:id", handlers.NewGetAnalysisJob(jobs))
	return router
}

func doPostAnalysis(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analysis", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestCreateAnalysisJob_VideoOnly is 7.6.1's headline case: a
// video-only request creates exactly one job, enqueued into the video
// queue only, and returns 202 with the exact {"job_id", "status"}
// shape the ticket specifies.
func TestCreateAnalysisJob_VideoOnly(t *testing.T) {
	videoQueue := &fakeAnalysisQueue{}
	audioQueue := &fakeAnalysisQueue{}
	jobs := newFakeAnalysisJobsRepo()
	router := newCreateAnalysisRouter(videoQueue, audioQueue, jobs)

	rec := doPostAnalysis(t, router, `{"session_id":"session-1","video_url":"https://example.com/video.mp4"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp struct {
		JobID     string `json:"job_id"`
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		Jobs      []struct {
			JobID  string `json:"job_id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body = %s", err, rec.Body.String())
	}
	if resp.JobID == "" || resp.Status != "queued" {
		t.Errorf("response = %+v, want non-empty job_id and status=queued", resp)
	}
	if len(resp.Jobs) != 1 || resp.Jobs[0].Type != "VIDEO_ANALYSIS" {
		t.Errorf("Jobs = %+v, want exactly one VIDEO_ANALYSIS entry", resp.Jobs)
	}

	if videoQueue.count() != 1 {
		t.Errorf("video queue got %d jobs, want 1", videoQueue.count())
	}
	if audioQueue.count() != 0 {
		t.Errorf("audio queue got %d jobs, want 0 — this was a video-only request", audioQueue.count())
	}

	stored, err := jobs.Get(context.Background(), resp.JobID)
	if err != nil {
		t.Fatalf("jobs.Get(%q) error = %v — the durable record must exist before the handler responds", resp.JobID, err)
	}
	if stored.Status != jobsrepo.StatusQueued || stored.SessionID != "session-1" {
		t.Errorf("stored job = %+v, want status=queued session_id=session-1", stored)
	}
}

// TestCreateAnalysisJob_AudioOnly mirrors the video-only case for the
// audio modality.
func TestCreateAnalysisJob_AudioOnly(t *testing.T) {
	videoQueue := &fakeAnalysisQueue{}
	audioQueue := &fakeAnalysisQueue{}
	jobs := newFakeAnalysisJobsRepo()
	router := newCreateAnalysisRouter(videoQueue, audioQueue, jobs)

	rec := doPostAnalysis(t, router, `{"session_id":"session-2","audio_url":"https://example.com/audio.wav"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if videoQueue.count() != 0 {
		t.Errorf("video queue got %d jobs, want 0 — this was an audio-only request", videoQueue.count())
	}
	if audioQueue.count() != 1 {
		t.Errorf("audio queue got %d jobs, want 1", audioQueue.count())
	}
}

// TestCreateAnalysisJob_VideoAndAudio is 7.6.2: one request with both
// URLs creates two independent jobs sharing session_id, one per queue.
func TestCreateAnalysisJob_VideoAndAudio(t *testing.T) {
	videoQueue := &fakeAnalysisQueue{}
	audioQueue := &fakeAnalysisQueue{}
	jobs := newFakeAnalysisJobsRepo()
	router := newCreateAnalysisRouter(videoQueue, audioQueue, jobs)

	rec := doPostAnalysis(t, router, `{"session_id":"session-3","video_url":"https://example.com/v.mp4","audio_url":"https://example.com/a.wav"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp struct {
		SessionID string `json:"session_id"`
		Jobs      []struct {
			JobID  string `json:"job_id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.SessionID != "session-3" {
		t.Errorf("session_id = %q, want session-3", resp.SessionID)
	}
	if len(resp.Jobs) != 2 {
		t.Fatalf("Jobs = %+v, want exactly 2 entries", resp.Jobs)
	}
	if resp.Jobs[0].JobID == resp.Jobs[1].JobID {
		t.Error("video and audio jobs got the same job_id, want distinct ids")
	}
	if videoQueue.count() != 1 || audioQueue.count() != 1 {
		t.Errorf("video queue = %d, audio queue = %d, want 1 each", videoQueue.count(), audioQueue.count())
	}
}

// TestCreateAnalysisJob_InvalidRequest covers 7.6.8's "invalid
// request": missing session_id, and no URL at all.
func TestCreateAnalysisJob_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing session_id", `{"video_url":"https://example.com/v.mp4"}`},
		{"no urls", `{"session_id":"session-1"}`},
		{"malformed json", `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newCreateAnalysisRouter(&fakeAnalysisQueue{}, &fakeAnalysisQueue{}, newFakeAnalysisJobsRepo())
			rec := doPostAnalysis(t, router, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

// TestCreateAnalysisJob_URLTooLong proves 7.7.6's maximum URL length:
// a video_url or audio_url longer than allowed is rejected outright,
// before it's ever persisted or enqueued — regardless of whether the
// oversized string would eventually fail SSRF validation too.
func TestCreateAnalysisJob_URLTooLong(t *testing.T) {
	tooLong := "https://example.com/" + strings.Repeat("a", 2048) + ".mp4"

	tests := []struct {
		name string
		body string
	}{
		{"video_url too long", fmt.Sprintf(`{"session_id":"session-1","video_url":%q}`, tooLong)},
		{"audio_url too long", fmt.Sprintf(`{"session_id":"session-1","audio_url":%q}`, tooLong)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			videoQueue := &fakeAnalysisQueue{}
			audioQueue := &fakeAnalysisQueue{}
			jobs := newFakeAnalysisJobsRepo()
			router := newCreateAnalysisRouter(videoQueue, audioQueue, jobs)

			rec := doPostAnalysis(t, router, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if videoQueue.count() != 0 || audioQueue.count() != 0 {
				t.Error("a job was enqueued despite the oversized URL")
			}
		})
	}
}

// TestCreateAnalysisJob_QueueUnavailable proves that when Redis is
// unreachable after the durable record was already written, the
// handler doesn't leave that record silently stuck at "queued"
// forever — it's marked dead_letter so a client polling it gets a
// truthful answer instead of an eternal "queued".
func TestCreateAnalysisJob_QueueUnavailable(t *testing.T) {
	videoQueue := &fakeAnalysisQueue{enqueueErr: errors.New("redis is unavailable")}
	audioQueue := &fakeAnalysisQueue{}
	jobs := newFakeAnalysisJobsRepo()
	router := newCreateAnalysisRouter(videoQueue, audioQueue, jobs)

	rec := doPostAnalysis(t, router, `{"session_id":"session-4","video_url":"https://example.com/v.mp4"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var found *jobsrepo.Job
	for _, j := range jobs.jobs {
		found = &j
	}
	if found == nil {
		t.Fatal("no job record was ever created")
	}
	if found.Status != jobsrepo.StatusDeadLetter {
		t.Errorf("job status = %q, want dead_letter — the enqueue failure must be reflected, not left as queued forever", found.Status)
	}
}

// TestCreateAnalysisJob_PostgresUnavailable is 7.7.7's adversarial
// counterpart to TestCreateAnalysisJob_QueueUnavailable: the durable
// jobs record is written first (see NewCreateAnalysisJob's own doc for
// why), so if Postgres itself is unreachable, jobs.Create fails before
// Redis is ever touched at all. This must fail cleanly (500, no panic,
// no job ever queued) rather than silently falling through to enqueue
// a job nothing will ever be able to look up the status of.
func TestCreateAnalysisJob_PostgresUnavailable(t *testing.T) {
	videoQueue := &fakeAnalysisQueue{}
	audioQueue := &fakeAnalysisQueue{}
	jobs := newFakeAnalysisJobsRepo()
	jobs.createErr = errors.New("dial tcp: connect: connection refused")
	router := newCreateAnalysisRouter(videoQueue, audioQueue, jobs)

	rec := doPostAnalysis(t, router, `{"session_id":"session-5","video_url":"https://example.com/v.mp4"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if videoQueue.count() != 0 {
		t.Error("a job was enqueued into Redis despite the Postgres write failing — the durable record must exist before anything is queued")
	}
	if len(jobs.jobs) != 0 {
		t.Error("a job record exists despite Create() having failed")
	}
}

func TestGetAnalysisJob_Found(t *testing.T) {
	jobs := newFakeAnalysisJobsRepo()
	jobs.jobs["job-1"] = jobsrepo.Job{
		ID: "job-1", SessionID: "session-1", Type: "VIDEO_ANALYSIS",
		Status: jobsrepo.StatusProcessing, Attempt: 1, MaxAttempts: 3,
	}
	router := newCreateAnalysisRouter(&fakeAnalysisQueue{}, &fakeAnalysisQueue{}, jobs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/analysis/jobs/job-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got jobsrepo.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.ID != "job-1" || got.Status != jobsrepo.StatusProcessing {
		t.Errorf("got job = %+v, want id=job-1 status=processing", got)
	}
}

func TestGetAnalysisJob_NotFound(t *testing.T) {
	router := newCreateAnalysisRouter(&fakeAnalysisQueue{}, &fakeAnalysisQueue{}, newFakeAnalysisJobsRepo())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/analysis/jobs/does-not-exist", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
