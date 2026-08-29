package analysisworker_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/analysisworker"
)

// trackingHandler records every job it's asked to handle, safely under
// concurrent calls from multiple pool workers — what
// TestPool_MultipleWorkers_ConcurrentJobs and
// TestPool_QueueRecovery use to verify exactly-once-per-enqueue
// processing under real concurrency.
type trackingHandler struct {
	mu   sync.Mutex
	seen map[string]int
}

func newTrackingHandler() *trackingHandler {
	return &trackingHandler{seen: make(map[string]int)}
}

func (h *trackingHandler) Handle(ctx context.Context, job analysisjob.AnalysisJob) error {
	h.mu.Lock()
	h.seen[job.ID]++
	h.mu.Unlock()
	return nil
}

func (h *trackingHandler) IsPermanent(err error) bool { return false }

func (h *trackingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.seen)
}

func (h *trackingHandler) duplicates() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var dups []string
	for id, n := range h.seen {
		if n != 1 {
			dups = append(dups, fmt.Sprintf("%s(x%d)", id, n))
		}
	}
	return dups
}

// TestPool_MultipleWorkers_ConcurrentJobs is 7.5.10's "multiple
// workers, concurrent jobs": a pool of several workers draining many
// jobs, each processed exactly once — no job dropped, none doubled up
// under real concurrency (run with -race).
func TestPool_MultipleWorkers_ConcurrentJobs(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newTrackingHandler()
	pool := analysisworker.NewPool(q, handler, 5, testLogger())

	const numJobs = 40
	for i := 0; i < numJobs; i++ {
		job, err := analysisjob.NewVideoAnalysisJob(fmt.Sprintf("session-%d", i), "https://example.com/v.mp4")
		if err != nil {
			t.Fatalf("NewVideoAnalysisJob() error = %v", err)
		}
		enqueueJob(t, q, job)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && handler.count() < numJobs {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	pool.Stop()

	if handler.count() != numJobs {
		t.Errorf("processed %d distinct jobs, want %d", handler.count(), numJobs)
	}
	if dups := handler.duplicates(); len(dups) > 0 {
		t.Errorf("jobs processed more than once: %v", dups)
	}

	snap := pool.Metrics()
	if snap.JobsCompleted != numJobs {
		t.Errorf("Metrics().JobsCompleted = %d, want %d", snap.JobsCompleted, numJobs)
	}
}

// TestPool_GracefulShutdown_SIGTERM proves Pool.Stop() (what
// cmd/gateway calls on SIGTERM — see 7.5.4) actually waits for every
// worker, not just one, before returning.
func TestPool_GracefulShutdown_SIGTERM(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	release := make(chan struct{})
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error {
		<-release
		return nil
	}
	pool := analysisworker.NewPool(q, handler, 3, testLogger())

	for i := 0; i < 3; i++ {
		enqueueJob(t, q, newTestVideoJob(t, fmt.Sprintf("session-%d", i)))
	}

	pool.Start(context.Background())
	for i := 0; i < 3; i++ {
		waitForNotify(t, handler.notify, 2*time.Second)
	}

	stopped := make(chan struct{})
	go func() {
		pool.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop() returned before in-flight jobs finished")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() never returned")
	}
}

// TestPool_QueueRecovery is 7.5.10's "queue recovery": a job stranded
// in the processing list by a prior run that never called Ack or Fail
// (see internal/queue's TestRedis_CrashedConsumer_JobSurvivesInProcessing)
// is recoverable via Queue.RecoverStale and then actually gets
// processed by a freshly-started Pool — not just visible, but usable.
func TestPool_QueueRecovery(t *testing.T) {
	q := newTestQueue(t, "test:recovery")

	// Simulate a previous run's crash: dequeue without ever acking.
	enqueueJob(t, q, newTestVideoJob(t, "session-stranded"))
	if _, err := q.Dequeue(context.Background()); err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if n, _ := q.ProcessingLen(context.Background()); n != 1 {
		t.Fatalf("ProcessingLen() = %d before recovery, want 1", n)
	}

	recovered, err := q.RecoverStale(context.Background())
	if err != nil {
		t.Fatalf("RecoverStale() error = %v", err)
	}
	if recovered != 1 {
		t.Fatalf("RecoverStale() recovered %d, want 1", recovered)
	}

	handler := newFakeHandler()
	pool := analysisworker.NewPool(q, handler, 2, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	defer func() { cancel(); pool.Stop() }()

	got := waitForNotify(t, handler.notify, 2*time.Second)
	if got.SessionID != "session-stranded" {
		t.Errorf("recovered job's SessionID = %q, want %q", got.SessionID, "session-stranded")
	}
}
