package analysisworker_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/analysisworker"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
)

func waitForNotify(t *testing.T, ch <-chan analysisjob.AnalysisJob, timeout time.Duration) analysisjob.AnalysisJob {
	t.Helper()
	select {
	case job := <-ch:
		return job
	case <-time.After(timeout):
		t.Fatal("timed out waiting for handler to be invoked")
		return analysisjob.AnalysisJob{}
	}
}

// TestWorker_EnqueueProcessAck is 7.5.10's headline case: enqueue →
// worker → handler → ack, end to end through a real (miniredis-backed)
// queue.
func TestWorker_EnqueueProcessAck(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	job := newTestVideoJob(t, "session-1")
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()

	got := waitForNotify(t, handler.notify, 2*time.Second)
	if got.ID != job.ID {
		t.Errorf("handler received job %q, want %q", got.ID, job.ID)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsCompleted == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := metrics.Snapshot()
	if snap.JobsReceived != 1 || snap.JobsCompleted != 1 {
		t.Errorf("metrics = %+v, want JobsReceived=1 JobsCompleted=1", snap)
	}

	if n, _ := q.ProcessingLen(context.Background()); n != 0 {
		t.Errorf("ProcessingLen() = %d, want 0 after successful processing", n)
	}
	cancel()
}

// TestWorker_HandlerPanic_DoesNotCrashWorker proves 7.5.7's "the worker
// itself must remain healthy": a panicking handler dead-letters that
// one job instead of taking down the goroutine (or the process), and
// the worker keeps processing the next job normally.
func TestWorker_HandlerPanic_DoesNotCrashWorker(t *testing.T) {
	q := newTestQueue(t, "test:video")
	panicked := false
	handler := newFakeHandler()
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error {
		if !panicked {
			panicked = true
			panic("simulated handler bug")
		}
		return nil
	}
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	enqueueJob(t, q, newTestVideoJob(t, "session-panic"))
	enqueueJob(t, q, newTestVideoJob(t, "session-after-panic"))

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	waitForNotify(t, handler.notify, 2*time.Second) // the panicking call
	waitForNotify(t, handler.notify, 2*time.Second) // the worker survived and picked up the next job

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsDeadLettered == 1 && metrics.Snapshot().JobsCompleted == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := metrics.Snapshot()
	if snap.JobsDeadLettered != 1 {
		t.Errorf("JobsDeadLettered = %d, want 1 (the panicking job)", snap.JobsDeadLettered)
	}
	if snap.JobsCompleted != 1 {
		t.Errorf("JobsCompleted = %d, want 1 (the worker kept working after the panic)", snap.JobsCompleted)
	}
}

// TestWorker_HandlerTimeout proves 7.5.7: a handler that hangs past the
// worker's per-job timeout is treated as a failed (and, being
// transient, retried) job rather than blocking the worker forever.
func TestWorker_HandlerTimeout(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	var attempts int
	var mu sync.Mutex
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			<-ctx.Done() // hang past the timeout on the first attempt
			return ctx.Err()
		}
		return nil // second attempt (the retry) succeeds quickly
	}
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger(), analysisworker.WithTimeout(100*time.Millisecond))

	enqueueJob(t, q, newTestVideoJob(t, "session-timeout"))

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	waitForNotify(t, handler.notify, 2*time.Second) // the hanging attempt, released once ctx times out
	waitForNotify(t, handler.notify, 3*time.Second) // the retried attempt, after backoff

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsCompleted == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if metrics.Snapshot().JobsCompleted != 1 {
		t.Errorf("JobsCompleted = %d, want 1 — the retry after timeout should have succeeded", metrics.Snapshot().JobsCompleted)
	}
}

// TestWorker_PermanentError_DeadLettersImmediately proves 7.5.5's "don't
// retry errors that are clearly permanent": a handler classifying its
// own error as permanent dead-letters on the very first attempt,
// regardless of MaxAttempts.
func TestWorker_PermanentError_DeadLettersImmediately(t *testing.T) {
	q := newTestQueue(t, "test:video")
	wantErr := errors.New("malformed input")
	handler := newFakeHandler()
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error { return wantErr }
	handler.permanentFunc = func(err error) bool { return errors.Is(err, wantErr) }
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	enqueueJob(t, q, newTestVideoJob(t, "session-permanent"))

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	waitForNotify(t, handler.notify, 2*time.Second)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsDeadLettered == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := metrics.Snapshot()
	if snap.JobsRetried != 0 {
		t.Errorf("JobsRetried = %d, want 0 — a permanent error must never be retried", snap.JobsRetried)
	}
	if snap.JobsDeadLettered != 1 {
		t.Errorf("JobsDeadLettered = %d, want 1", snap.JobsDeadLettered)
	}
	if handler.callCount() != 1 {
		t.Errorf("handler called %d times, want exactly 1", handler.callCount())
	}
	if n, _ := q.FailedLen(context.Background()); n != 1 {
		t.Errorf("FailedLen() = %d, want 1", n)
	}
}

// TestWorker_TransientError_RetriesThenSucceeds proves 7.5.5's retry
// path end to end: a transient failure is retried with backoff and
// Attempt is incremented, and a subsequent success completes the job
// normally.
func TestWorker_TransientError_RetriesThenSucceeds(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	var mu sync.Mutex
	var attempts []int
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error {
		mu.Lock()
		attempts = append(attempts, job.Attempt)
		n := len(attempts)
		mu.Unlock()
		if n == 1 {
			return errors.New("video-detector unreachable")
		}
		return nil
	}
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	enqueueJob(t, q, newTestVideoJob(t, "session-retry"))

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	waitForNotify(t, handler.notify, 2*time.Second) // first attempt, fails
	waitForNotify(t, handler.notify, 3*time.Second) // retried attempt (after >=1s backoff), succeeds

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsCompleted == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	gotAttempts := append([]int(nil), attempts...)
	mu.Unlock()
	if len(gotAttempts) != 2 || gotAttempts[0] != 1 || gotAttempts[1] != 2 {
		t.Errorf("attempts seen by handler = %v, want [1 2]", gotAttempts)
	}

	snap := metrics.Snapshot()
	if snap.JobsRetried != 1 {
		t.Errorf("JobsRetried = %d, want 1", snap.JobsRetried)
	}
	if snap.JobsCompleted != 1 {
		t.Errorf("JobsCompleted = %d, want 1", snap.JobsCompleted)
	}
}

// TestWorker_MaxAttemptsExceeded_DeadLetters proves a job that keeps
// failing transiently is eventually dead-lettered once MaxAttempts is
// exhausted, rather than retried forever (7.5.5/7.5.6).
func TestWorker_MaxAttemptsExceeded_DeadLetters(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error {
		return errors.New("still unreachable")
	}
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	job := newTestVideoJob(t, "session-exhausted")
	job.MaxAttempts = 2 // keep this test's wall-clock time bounded
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	waitForNotify(t, handler.notify, 2*time.Second) // attempt 1
	waitForNotify(t, handler.notify, 3*time.Second) // attempt 2 (last)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsDeadLettered == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if handler.callCount() != 2 {
		t.Errorf("handler called %d times, want exactly 2 (MaxAttempts)", handler.callCount())
	}
	snap := metrics.Snapshot()
	if snap.JobsRetried != 1 {
		t.Errorf("JobsRetried = %d, want 1 (one retry, from attempt 1 to attempt 2)", snap.JobsRetried)
	}
	if snap.JobsDeadLettered != 1 {
		t.Errorf("JobsDeadLettered = %d, want 1", snap.JobsDeadLettered)
	}
}

// TestWorker_MalformedJob_DeadLettersWithoutCrashing proves a job that
// can't even be decoded (corrupt bytes in the queue, e.g. from a
// version skew) is dead-lettered immediately and doesn't hang or crash
// the worker.
func TestWorker_MalformedJob_DeadLettersWithoutCrashing(t *testing.T) {
	q := newTestQueue(t, "test:video")
	if err := q.Enqueue(context.Background(), mustMalformedJob()); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	handler := newFakeHandler()
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsDeadLettered == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if metrics.Snapshot().JobsDeadLettered != 1 {
		t.Fatal("malformed job was never dead-lettered")
	}
	if handler.callCount() != 0 {
		t.Errorf("handler called %d times, want 0 — a job that can't be decoded should never reach it", handler.callCount())
	}

	// The worker must still be alive and able to process a good job
	// afterward.
	enqueueJob(t, q, newTestVideoJob(t, "session-after-malformed"))
	waitForNotify(t, handler.notify, 2*time.Second)
}

// TestWorker_DuplicateExecution_HandlerCalledButRepositoryStaysConsistent
// simulates 7.5.8's redelivery scenario at the worker level: the same
// AnalysisJob ID enqueued (and thus processed) twice. Worker itself
// doesn't dedupe (the queue is honestly at-least-once — see
// internal/queue's TestRedis_DuplicateJobID) — this proves the handler
// genuinely gets invoked both times, which is what makes the
// repository's upsert-based idempotency (see internal/repository/
// analysis's tests) the layer actually responsible for correctness.
func TestWorker_DuplicateExecution_HandlerInvokedForBothDeliveries(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	job := newTestVideoJob(t, "session-dup")
	enqueueJob(t, q, job)
	enqueueJob(t, q, job) // the exact same job, enqueued again — simulates redelivery

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	first := waitForNotify(t, handler.notify, 2*time.Second)
	second := waitForNotify(t, handler.notify, 2*time.Second)

	if first.ID != job.ID || second.ID != job.ID {
		t.Errorf("got job IDs %q and %q, want both to be %q", first.ID, second.ID, job.ID)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsCompleted == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if metrics.Snapshot().JobsCompleted != 2 {
		t.Errorf("JobsCompleted = %d, want 2 (both deliveries acked successfully)", metrics.Snapshot().JobsCompleted)
	}
}

// TestWorker_GracefulShutdown proves Stop() waits for an in-flight job
// to finish (and ack) before returning, and that no new job is picked
// up afterward (7.5.4).
func TestWorker_GracefulShutdown(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	release := make(chan struct{})
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error {
		<-release
		return nil
	}
	metrics := analysisworker.NewMetrics()
	w := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	enqueueJob(t, q, newTestVideoJob(t, "session-inflight"))
	enqueueJob(t, q, newTestVideoJob(t, "session-never-started"))

	w.Start(context.Background())
	waitForNotify(t, handler.notify, 2*time.Second) // the in-flight job has started

	stopped := make(chan struct{})
	go func() {
		w.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop() returned before the in-flight job finished")
	case <-time.After(200 * time.Millisecond):
	}

	close(release) // let the in-flight job finish

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() never returned after the in-flight job finished")
	}

	if metrics.Snapshot().JobsCompleted != 1 {
		t.Errorf("JobsCompleted = %d, want 1 (only the in-flight job)", metrics.Snapshot().JobsCompleted)
	}
	if n, _ := q.PendingLen(context.Background()); n != 1 {
		t.Errorf("PendingLen() = %d, want 1 — the second job must be untouched", n)
	}
	// The in-flight job's Ack must have actually gone through, not just
	// been attempted against an already-canceled context — a live
	// end-to-end run of this package caught exactly this bug (the job
	// persisted correctly but stayed stuck in "processing" forever
	// because Ack was tied to the run-loop context Stop() cancels).
	if n, _ := q.ProcessingLen(context.Background()); n != 0 {
		t.Errorf("ProcessingLen() = %d, want 0 — the in-flight job's Ack must complete even though Stop() already canceled the run-loop context", n)
	}
}

func mustMalformedJob() queue.Job {
	return queue.Job{ID: "malformed", Type: "VIDEO_ANALYSIS", Payload: []byte("not valid json")}
}

// TestWorker_Success_MarksJobCompleted proves 7.6.3's status tracking:
// a successful job is reflected as completed in the durable jobs
// record, not just acked off the queue.
func TestWorker_Success_MarksJobCompleted(t *testing.T) {
	q := newTestQueue(t, "test:video")
	handler := newFakeHandler()
	jobs := newFakeJobsRepo()
	w := analysisworker.NewWorker(q, handler, jobs, analysisworker.NewMetrics(), testLogger())

	job := newTestVideoJob(t, "session-status")
	jobs.put(jobsrepo.Job{ID: job.ID, SessionID: job.SessionID, Type: string(job.Type), Status: jobsrepo.StatusQueued, CreatedAt: job.CreatedAt, MaxAttempts: job.MaxAttempts})
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	waitForNotify(t, handler.notify, 2*time.Second)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, err := jobs.Get(context.Background(), job.ID); err == nil && got.Status == jobsrepo.StatusCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := jobs.Get(context.Background(), job.ID)
	t.Fatalf("job status = %+v, want status=completed", got)
}

// TestWorker_PermanentFailure_MarksJobDeadLetterAndNotifiesHandler
// proves both halves of 7.6.3/7.6.6 on the dead-letter path: the
// durable record is updated to dead_letter, and the handler's
// OnDeadLetter hook (the completion coordinator's entry point for a
// permanently-failed modality) is actually invoked.
func TestWorker_PermanentFailure_MarksJobDeadLetterAndNotifiesHandler(t *testing.T) {
	q := newTestQueue(t, "test:video")
	wantErr := errors.New("malformed input")
	handler := newFakeHandler()
	handler.handleFunc = func(ctx context.Context, job analysisjob.AnalysisJob) error { return wantErr }
	handler.permanentFunc = func(err error) bool { return errors.Is(err, wantErr) }
	jobs := newFakeJobsRepo()
	w := analysisworker.NewWorker(q, handler, jobs, analysisworker.NewMetrics(), testLogger())

	job := newTestVideoJob(t, "session-permanent-status")
	jobs.put(jobsrepo.Job{ID: job.ID, SessionID: job.SessionID, Type: string(job.Type), Status: jobsrepo.StatusQueued, CreatedAt: job.CreatedAt, MaxAttempts: job.MaxAttempts})
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	waitForNotify(t, handler.notify, 2*time.Second)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if handler.deadLetterCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if handler.deadLetterCount() != 1 {
		t.Fatal("handler.OnDeadLetter was never called")
	}

	got, err := jobs.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("jobs.Get() error = %v", err)
	}
	if got.Status != jobsrepo.StatusDeadLetter {
		t.Errorf("job status = %q, want dead_letter", got.Status)
	}
}

// TestWorker_MalformedJob_MarksDeadLetterByEnvelopeID proves the
// malformed-job path still updates the durable record — using the
// outer queue.Job's ID, since the corrupt AnalysisJob payload can't be
// decoded at all.
func TestWorker_MalformedJob_MarksDeadLetterByEnvelopeID(t *testing.T) {
	q := newTestQueue(t, "test:video")
	if err := q.Enqueue(context.Background(), mustMalformedJob()); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	jobs := newFakeJobsRepo()
	jobs.put(jobsrepo.Job{ID: "malformed", SessionID: "unknown", Type: "VIDEO_ANALYSIS", Status: jobsrepo.StatusQueued, CreatedAt: time.Now()})
	w := analysisworker.NewWorker(q, newFakeHandler(), jobs, analysisworker.NewMetrics(), testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, err := jobs.Get(context.Background(), "malformed"); err == nil && got.Status == jobsrepo.StatusDeadLetter {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := jobs.Get(context.Background(), "malformed")
	t.Fatalf("job status = %+v, want status=dead_letter", got)
}
