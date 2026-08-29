package analysisworker_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
)

// fakeHandler is a configurable analysisworker.Handler for worker/pool-
// level tests, which care about Worker's own orchestration (retry,
// timeout, dead-letter, shutdown) rather than any real detector or
// database — see handler_test.go for tests of the real handlers.
type fakeHandler struct {
	mu              sync.Mutex
	handleFunc      func(ctx context.Context, job analysisjob.AnalysisJob) error
	permanentFunc   func(error) bool
	onDeadLetter    func(ctx context.Context, job analysisjob.AnalysisJob) error
	calls           []analysisjob.AnalysisJob
	deadLetterCalls []analysisjob.AnalysisJob
	notify          chan analysisjob.AnalysisJob
}

func newFakeHandler() *fakeHandler {
	return &fakeHandler{notify: make(chan analysisjob.AnalysisJob, 100)}
}

func (f *fakeHandler) Handle(ctx context.Context, job analysisjob.AnalysisJob) error {
	f.mu.Lock()
	f.calls = append(f.calls, job)
	f.mu.Unlock()

	// Signal invocation before calling handleFunc, not after: a
	// handleFunc that panics or blocks (both deliberately exercised by
	// tests in this package) would otherwise never reach a post-call
	// send, starving anything waiting on notify for no reason related
	// to what's actually being tested.
	f.notify <- job
	if f.handleFunc != nil {
		return f.handleFunc(ctx, job)
	}
	return nil
}

func (f *fakeHandler) IsPermanent(err error) bool {
	if f.permanentFunc != nil {
		return f.permanentFunc(err)
	}
	return false
}

func (f *fakeHandler) OnDeadLetter(ctx context.Context, job analysisjob.AnalysisJob) error {
	f.mu.Lock()
	f.deadLetterCalls = append(f.deadLetterCalls, job)
	f.mu.Unlock()
	if f.onDeadLetter != nil {
		return f.onDeadLetter(ctx, job)
	}
	return nil
}

func (f *fakeHandler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeHandler) deadLetterCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deadLetterCalls)
}

// fakeJobsRepo is a minimal in-memory jobsrepo.Repository for
// worker/pool/coordinator-level tests — see
// internal/repository/jobs/postgres_test.go for tests against the real
// implementation.
type fakeJobsRepo struct {
	mu   sync.Mutex
	jobs map[string]jobsrepo.Job
	err  error
}

func newFakeJobsRepo() *fakeJobsRepo {
	return &fakeJobsRepo{jobs: make(map[string]jobsrepo.Job)}
}

func (f *fakeJobsRepo) Create(ctx context.Context, job jobsrepo.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.jobs[job.ID] = job
	return nil
}

func (f *fakeJobsRepo) Get(ctx context.Context, id string) (*jobsrepo.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return nil, jobsrepo.ErrNotFound
	}
	return &j, nil
}

func (f *fakeJobsRepo) GetLatestBySessionAndType(ctx context.Context, sessionID, jobType string) (*jobsrepo.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var latest *jobsrepo.Job
	for _, j := range f.jobs {
		if j.SessionID != sessionID || j.Type != jobType {
			continue
		}
		jj := j
		if latest == nil || jj.CreatedAt.After(latest.CreatedAt) {
			latest = &jj
		}
	}
	if latest == nil {
		return nil, jobsrepo.ErrNotFound
	}
	return latest, nil
}

func (f *fakeJobsRepo) MarkProcessing(ctx context.Context, id string, attempt int) error {
	return f.update(id, func(j *jobsrepo.Job) { j.Status = jobsrepo.StatusProcessing; j.Attempt = attempt })
}

func (f *fakeJobsRepo) MarkCompleted(ctx context.Context, id string) error {
	return f.update(id, func(j *jobsrepo.Job) { j.Status = jobsrepo.StatusCompleted })
}

func (f *fakeJobsRepo) MarkFailed(ctx context.Context, id string, attempt int, lastError string) error {
	return f.update(id, func(j *jobsrepo.Job) {
		j.Status = jobsrepo.StatusFailed
		j.Attempt = attempt
		j.LastError = lastError
	})
}

func (f *fakeJobsRepo) MarkDeadLetter(ctx context.Context, id string, lastError string) error {
	return f.update(id, func(j *jobsrepo.Job) { j.Status = jobsrepo.StatusDeadLetter; j.LastError = lastError })
}

func (f *fakeJobsRepo) update(id string, mutate func(*jobsrepo.Job)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	j, ok := f.jobs[id]
	if !ok {
		return jobsrepo.ErrNotFound
	}
	mutate(&j)
	f.jobs[id] = j
	return nil
}

// put seeds a job record directly, bypassing Create — a convenience
// for tests that only care about a job's status when queried by the
// coordinator, not the exact fields Create would have set.
func (f *fakeJobsRepo) put(job jobsrepo.Job) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[job.ID] = job
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestQueue spins up an in-process fake Redis (miniredis) and
// returns a real queue.Redis backed by it, the same pattern
// internal/queue's own tests use.
func newTestQueue(t *testing.T, key string) *queue.Redis {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	t.Cleanup(mr.Close)

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	return queue.NewRedis(client, key, 100)
}

// enqueueJob wraps and enqueues an AnalysisJob, failing the test on error.
func enqueueJob(t *testing.T, q *queue.Redis, job analysisjob.AnalysisJob) {
	t.Helper()
	qj, err := job.ToQueueJob()
	if err != nil {
		t.Fatalf("ToQueueJob() error = %v", err)
	}
	if err := q.Enqueue(context.Background(), qj); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
}

func newTestVideoJob(t *testing.T, sessionID string) analysisjob.AnalysisJob {
	t.Helper()
	job, err := analysisjob.NewVideoAnalysisJob(sessionID, "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}
	return job
}
