package worker_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

// callResult is one canned outcome for fakeClient.Analyze, consumed in
// order as calls come in; the last entry repeats once exhausted.
type callResult struct {
	result *detector.Result
	err    error
}

// fakeClient is a controllable DetectorClient: a scripted sequence of
// results/errors (for retry tests), an optional block channel (to make
// "processing" observable), and an optional panic trigger (for the
// worker-failure test).
type fakeClient struct {
	mu      sync.Mutex
	results []callResult
	calls   int
	block   <-chan struct{}
	panicOn int // if > 0, panic on this call number (1-indexed)
}

func (f *fakeClient) Analyze(ctx context.Context, videoURL string) (*detector.Result, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()

	if f.block != nil {
		<-f.block
	}

	if f.panicOn > 0 && n == f.panicOn {
		panic("simulated worker failure")
	}

	f.mu.Lock()
	idx := n - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	res := f.results[idx]
	f.mu.Unlock()

	return res.result, res.err
}

func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newPool(t *testing.T, client worker.DetectorClient, queueSize int) (*worker.Pool, *worker.Store) {
	t.Helper()
	redisClient := newTestRedis(t)
	queue := worker.NewQueue(redisClient, queueSize)
	store := worker.NewStore(redisClient, time.Hour)
	return worker.NewPool(queue, store, client, testLogger()), store
}

func waitForStatus(t *testing.T, store *worker.Store, id string, want worker.Status, timeout time.Duration) worker.Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := store.Get(context.Background(), id)
		if err == nil && job.Status == want {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, _ := store.Get(context.Background(), id)
	t.Fatalf("job %s never reached status %q (last seen: %+v)", id, want, job)
	return worker.Job{}
}

// --- lifecycle ---

func TestPool_JobLifecycle(t *testing.T) {
	release := make(chan struct{})
	client := &fakeClient{results: []callResult{{result: &detector.Result{Verdict: "real"}}}, block: release}
	pool, store := newPool(t, client, 4)
	pool.Start(1)
	defer pool.Shutdown(context.Background())

	job, err := pool.Submit("https://example.com/v.mp4")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if job.Status != worker.StatusQueued {
		t.Errorf("initial Status = %q, want %q", job.Status, worker.StatusQueued)
	}

	waitForStatus(t, store, job.ID, worker.StatusProcessing, time.Second)
	close(release)

	completed := waitForStatus(t, store, job.ID, worker.StatusCompleted, time.Second)
	if completed.Result == nil || completed.Result.Verdict != "real" {
		t.Errorf("Result = %+v, want Verdict=real", completed.Result)
	}
}

func TestPool_JobFailureLifecycle(t *testing.T) {
	client := &fakeClient{results: []callResult{
		{err: &detector.Error{Kind: detector.KindInvalidVideo, Message: "corrupt file"}},
	}}
	pool, store := newPool(t, client, 4)
	pool.Start(1)
	defer pool.Shutdown(context.Background())

	job, err := pool.Submit("https://example.com/v.mp4")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	failed := waitForStatus(t, store, job.ID, worker.StatusFailed, time.Second)
	if failed.Error != "corrupt file" {
		t.Errorf("Error = %q, want %q", failed.Error, "corrupt file")
	}
	if failed.Result != nil {
		t.Errorf("Result = %+v, want nil on failure", failed.Result)
	}
}

// --- retry ---

func TestPool_RetrySucceedsAfterTransientFailures(t *testing.T) {
	client := &fakeClient{results: []callResult{
		{err: &detector.Error{Kind: detector.KindUnavailable, Message: "unreachable"}},
		{err: &detector.Error{Kind: detector.KindUnavailable, Message: "unreachable"}},
		{result: &detector.Result{Verdict: "real"}},
	}}
	pool, store := newPool(t, client, 4)
	pool.Start(1)
	defer pool.Shutdown(context.Background())

	job, err := pool.Submit("https://example.com/v.mp4")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	completed := waitForStatus(t, store, job.ID, worker.StatusCompleted, 4*time.Second)
	if completed.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", completed.Attempts)
	}
	if client.callCount() != 3 {
		t.Errorf("Analyze() called %d times, want 3", client.callCount())
	}
}

func TestPool_RetryExhaustedMarksFailed(t *testing.T) {
	always := &detector.Error{Kind: detector.KindTimeout, Message: "timed out"}
	client := &fakeClient{results: []callResult{{err: always}}} // repeats for every call
	pool, store := newPool(t, client, 4)
	pool.Start(1)
	defer pool.Shutdown(context.Background())

	job, err := pool.Submit("https://example.com/v.mp4")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	failed := waitForStatus(t, store, job.ID, worker.StatusFailed, 3*time.Second)
	if failed.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3 (maxAttempts)", failed.Attempts)
	}
	if client.callCount() != 3 {
		t.Errorf("Analyze() called %d times, want 3", client.callCount())
	}
	if failed.Error != "timed out" {
		t.Errorf("Error = %q, want %q", failed.Error, "timed out")
	}
}

func TestPool_NonRetryableErrorFailsImmediately(t *testing.T) {
	client := &fakeClient{results: []callResult{
		{err: &detector.Error{Kind: detector.KindInvalidVideo, Message: "bad video"}},
		{result: &detector.Result{Verdict: "real"}}, // should never be reached
	}}
	pool, store := newPool(t, client, 4)
	pool.Start(1)
	defer pool.Shutdown(context.Background())

	job, err := pool.Submit("https://example.com/v.mp4")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	failed := waitForStatus(t, store, job.ID, worker.StatusFailed, time.Second)
	if failed.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (no retry for an invalid video)", failed.Attempts)
	}
	if client.callCount() != 1 {
		t.Errorf("Analyze() called %d times, want 1", client.callCount())
	}
}

// --- timeout ---

func TestPool_ShutdownTimesOutIfJobHangs(t *testing.T) {
	client := &fakeClient{results: []callResult{{result: &detector.Result{}}}, block: make(chan struct{})} // never released
	pool, store := newPool(t, client, 4)
	pool.Start(1)

	job, err := pool.Submit("https://example.com/v.mp4")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	waitForStatus(t, store, job.ID, worker.StatusProcessing, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err = pool.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown() error = %v, want context.DeadlineExceeded", err)
	}
}

// --- worker failure ---

func TestPool_WorkerFailureRecoversFromPanic(t *testing.T) {
	client := &fakeClient{
		results: []callResult{
			{}, // call 1: panics before this is used
			{result: &detector.Result{Verdict: "real"}}, // call 2
		},
		panicOn: 1,
	}
	pool, store := newPool(t, client, 4)
	pool.Start(1)
	defer pool.Shutdown(context.Background())

	job1, err := pool.Submit("https://example.com/a.mp4")
	if err != nil {
		t.Fatalf("Submit(a) error = %v", err)
	}

	failed := waitForStatus(t, store, job1.ID, worker.StatusFailed, time.Second)
	if failed.Error == "" {
		t.Error("Error = empty, want a message describing the panic")
	}

	// The same worker goroutine must still be alive and able to process
	// the next job — a panic in one job must not take the worker down.
	job2, err := pool.Submit("https://example.com/b.mp4")
	if err != nil {
		t.Fatalf("Submit(b) error = %v", err)
	}
	completed := waitForStatus(t, store, job2.ID, worker.StatusCompleted, time.Second)
	if completed.Result == nil || completed.Result.Verdict != "real" {
		t.Errorf("Result = %+v, want Verdict=real", completed.Result)
	}
}

// --- redis unavailable ---

func TestPool_RedisUnavailable_SubmitFails(t *testing.T) {
	redisClient := unreachableRedis(t)
	queue := worker.NewQueue(redisClient, 4)
	store := worker.NewStore(redisClient, time.Hour)
	client := &fakeClient{results: []callResult{{result: &detector.Result{}}}}
	pool := worker.NewPool(queue, store, client, testLogger())

	_, err := pool.Submit("https://example.com/v.mp4")
	if !errors.Is(err, worker.ErrRedisUnavailable) {
		t.Errorf("Submit() error = %v, want ErrRedisUnavailable", err)
	}
}

func TestPool_RedisUnavailable_JobStatusUnreadable(t *testing.T) {
	redisClient := unreachableRedis(t)
	store := worker.NewStore(redisClient, time.Hour)

	_, err := store.Get(context.Background(), "any-id")
	if !errors.Is(err, worker.ErrRedisUnavailable) {
		t.Errorf("Get() error = %v, want ErrRedisUnavailable", err)
	}
}

// --- shutdown (carried over from the in-process pool) ---

func TestPool_ShutdownWaitsForInFlightJob(t *testing.T) {
	release := make(chan struct{})
	client := &fakeClient{results: []callResult{{result: &detector.Result{Verdict: "real"}}}, block: release}
	pool, store := newPool(t, client, 4)
	pool.Start(1)

	job, err := pool.Submit("https://example.com/v.mp4")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	waitForStatus(t, store, job.ID, worker.StatusProcessing, time.Second)

	var shutdownReturned atomic.Bool
	shutdownErr := make(chan error, 1)
	go func() {
		shutdownErr <- pool.Shutdown(context.Background())
		shutdownReturned.Store(true)
	}()

	time.Sleep(50 * time.Millisecond)
	if shutdownReturned.Load() {
		t.Fatal("Shutdown() returned before the in-flight job finished")
	}

	close(release)

	if err := <-shutdownErr; err != nil {
		t.Errorf("Shutdown() error = %v, want nil", err)
	}
	waitForStatus(t, store, job.ID, worker.StatusCompleted, time.Second)
}

func TestPool_ShutdownRejectsNewSubmissions(t *testing.T) {
	client := &fakeClient{results: []callResult{{result: &detector.Result{}}}}
	pool, _ := newPool(t, client, 4)
	pool.Start(1)

	if err := pool.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	_, err := pool.Submit("https://example.com/v.mp4")
	if !errors.Is(err, worker.ErrPoolClosed) {
		t.Fatalf("Submit() after shutdown error = %v, want ErrPoolClosed", err)
	}
}

func TestPool_ShutdownIsIdempotent(t *testing.T) {
	client := &fakeClient{results: []callResult{{result: &detector.Result{}}}}
	pool, _ := newPool(t, client, 4)
	pool.Start(1)

	if err := pool.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := pool.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v, want nil (idempotent)", err)
	}
}

func TestPool_QueueFullRejectsSubmit(t *testing.T) {
	client := &fakeClient{results: []callResult{{result: &detector.Result{}}}}
	// No workers started — nothing ever drains the queue, so its single
	// slot fills after one Submit.
	pool, store := newPool(t, client, 1)

	first, err := pool.Submit("https://example.com/a.mp4")
	if err != nil {
		t.Fatalf("first Submit() error = %v", err)
	}
	if first.Status != worker.StatusQueued {
		t.Errorf("first job status = %q, want %q", first.Status, worker.StatusQueued)
	}

	second, err := pool.Submit("https://example.com/b.mp4")
	if !errors.Is(err, worker.ErrQueueFull) {
		t.Fatalf("second Submit() error = %v, want ErrQueueFull", err)
	}
	if second.Status != worker.StatusFailed {
		t.Errorf("second job status = %q, want %q", second.Status, worker.StatusFailed)
	}

	stored, err := store.Get(context.Background(), second.ID)
	if err != nil || stored.Status != worker.StatusFailed {
		t.Errorf("store.Get(%s) = %+v, %v, want a stored failed job", second.ID, stored, err)
	}
}
