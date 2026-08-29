// Package analysisworker consumes AnalysisJob records (internal/
// analysisjob) off a Queue (internal/queue) and turns them into
// persisted PostgreSQL results — the "distributed workers" half of
// Phase 7.3's target architecture. This is entirely separate from
// internal/realtime's live WebSocket pipeline: a Meet call's frames
// never touch this package, and this package never touches a
// WebSocket. See the package's own worker_test.go for the reasoning
// behind each reliability decision (retry backoff, dead-lettering,
// timeouts, idempotency).
//
// Also unrelated to the existing internal/worker package, which is the
// older, synchronous-ish pipeline behind POST /api/v1/analyze (a
// single job type, no retry/dead-letter/ack semantics). This package
// is named differently specifically to avoid colliding with it rather
// than merging two systems with different job models and different
// reliability guarantees.
package analysisworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
)

const (
	// retryBaseDelay and retryMaxDelay implement 7.5.5's exponential
	// backoff (1s, 2s, 4s, 8s, ... capped) — see backoffFor.
	retryBaseDelay = 1 * time.Second
	retryMaxDelay  = 30 * time.Second

	// defaultJobTimeout bounds a single job's processing (7.5.7) if the
	// caller doesn't override it — generous enough for a real detector
	// call, short enough that one hung Python process doesn't tie up a
	// worker slot indefinitely.
	defaultJobTimeout = 60 * time.Second
)

// Worker dequeues jobs from one Queue, one at a time, and runs them
// through a Handler: decode → (timeout-bounded) handle → ack, or on
// failure, retry with backoff or dead-letter. Video and audio workers
// are both just a Worker with a different Handler — see handler.go.
//
// Every transition also updates jobs (7.6.3): Redis is the transport,
// this is the durable record a client polls via
// GET /api/v1/analysis/jobs/:id. A status-tracking write failing (say,
// Postgres hiccups while Redis is fine) is logged but never blocks or
// fails the actual work — losing a status update is far cheaper than
// losing or duplicating a job.
type Worker struct {
	queue   queue.Queue
	handler Handler
	jobs    jobsrepo.Repository
	timeout time.Duration
	metrics *Metrics
	logger  *slog.Logger

	cancel context.CancelFunc
	done   chan struct{}
}

// Option configures a Worker built by NewWorker.
type Option func(*Worker)

// WithTimeout overrides defaultJobTimeout.
func WithTimeout(d time.Duration) Option {
	return func(w *Worker) { w.timeout = d }
}

// NewWorker builds a Worker that will consume from q using handler once
// Start is called, recording status transitions in jobs as it goes.
func NewWorker(q queue.Queue, handler Handler, jobs jobsrepo.Repository, metrics *Metrics, logger *slog.Logger, opts ...Option) *Worker {
	w := &Worker{
		queue:   q,
		handler: handler,
		jobs:    jobs,
		timeout: defaultJobTimeout,
		metrics: metrics,
		logger:  logger,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start begins consuming jobs in a background goroutine. ctx bounds
// the worker's lifetime — canceling it (or calling Stop) stops
// dequeuing new jobs; a job already in flight still runs to
// completion (up to its own timeout) before the goroutine exits, so
// shutdown is graceful rather than abrupt.
func (w *Worker) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})

	w.metrics.activeWorkers.Add(1)
	go w.run(runCtx)
}

// Stop signals the worker to stop dequeuing new jobs and blocks until
// its current job (if any) finishes and the worker goroutine actually
// exits. Bounded by that job's own timeout — never longer.
func (w *Worker) Stop() {
	if w.cancel == nil {
		return // never started
	}
	w.cancel()
	<-w.done
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	defer w.metrics.activeWorkers.Add(-1)

	for {
		d, err := w.queue.Dequeue(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // Stop was called (or parent ctx ended) — exit cleanly
			}
			w.logger.Warn("dequeue failed", slog.String("error", err.Error()))
			continue
		}

		w.metrics.jobsReceived.Add(1)
		w.process(ctx, d)
	}
}

// queueOpTimeout bounds the fresh, independent context every
// post-processing queue call (Ack/Fail/the retry's Enqueue) gets — see
// process's doc comment for why these deliberately don't use the
// worker's run-loop context.
const queueOpTimeout = 5 * time.Second

// queueOpContext builds that fresh context.
func queueOpContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), queueOpTimeout)
}

// process runs one Delivery through decode → handle → ack/fail/retry.
// ctx is the worker's run-loop context (canceled on Stop) — passed
// through only as far as retry's backoff wait, which is allowed to be
// cut short by shutdown; see retry's doc comment.
//
// Every actual queue call in here (Ack, Fail, the retry path's
// Enqueue) uses its own fresh context from queueOpContext instead,
// never ctx: ctx gets canceled the instant Stop() is called, including
// while a job that was already in flight is still finishing.
// Acknowledging a job that completed during shutdown is exactly what
// graceful shutdown is supposed to still do correctly; tying that Ack
// to the very context Stop() cancels would make the job look
// successful in Postgres (Handle's own context is separately
// timed-out, unaffected by ctx) but never actually leave the
// processing list — silently forcing a pointless reprocessing on next
// startup, which is exactly the bug a live end-to-end run of this
// package caught.
func (w *Worker) process(ctx context.Context, d queue.Delivery) {
	job, err := analysisjob.FromQueueJob(d.Job)
	if err != nil {
		// Can't even decode the job — there's no Attempt/MaxAttempts to
		// consult, and retrying identical unparseable bytes can't ever
		// succeed. Dead-letter immediately, per 7.5.5's "don't retry
		// errors that are clearly permanent" rule. The outer queue.Job
		// envelope (unlike the corrupt AnalysisJob payload inside it)
		// decoded fine, so its ID still lets the durable record reflect
		// this — just without the session/type detail we can't recover.
		w.logger.Error("malformed job in queue", slog.String("error", err.Error()))
		w.markDeadLetter(d.Job.ID, "malformed job: "+err.Error())
		w.failPermanently(d, "malformed job: "+err.Error())
		return
	}

	w.metrics.queueWaitLatency.observe(time.Since(job.CreatedAt))
	w.markProcessing(job)

	handleCtx, cancel := context.WithTimeout(context.Background(), w.timeout)
	start := time.Now()
	handleErr := w.safeHandle(handleCtx, job)
	cancel()
	w.metrics.processingLatency.observe(time.Since(start))

	if handleErr == nil {
		ackCtx, ackCancel := queueOpContext()
		err := w.queue.Ack(ackCtx, d)
		ackCancel()
		if err != nil {
			w.logger.Error("ack failed", slog.String("job_id", job.ID), slog.String("error", err.Error()))
		}
		w.markCompleted(job)
		w.metrics.jobsCompleted.Add(1)
		return
	}

	w.metrics.jobsFailed.Add(1)
	w.logger.Warn("job failed",
		slog.String("job_id", job.ID),
		slog.String("session_id", job.SessionID),
		slog.Int("attempt", job.Attempt),
		slog.String("error", handleErr.Error()),
	)

	var panicked *handlerPanicError
	permanent := errors.As(handleErr, &panicked) || w.handler.IsPermanent(handleErr) || !job.HasAttemptsRemaining()
	if permanent {
		w.markDeadLetter(job.ID, handleErr.Error())
		w.notifyDeadLetter(job)
		w.failPermanently(d, handleErr.Error())
		return
	}

	w.markFailed(job, handleErr.Error())
	w.retry(ctx, d, job, handleErr)
}

// markProcessing, markCompleted, markFailed, and markDeadLetter update
// the durable job record (7.6.3), logging rather than failing the job
// if the write itself fails — see Worker's doc comment.
func (w *Worker) markProcessing(job analysisjob.AnalysisJob) {
	ctx, cancel := queueOpContext()
	defer cancel()
	if err := w.jobs.MarkProcessing(ctx, job.ID, job.Attempt); err != nil {
		w.logger.Error("mark job processing failed", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}
}

func (w *Worker) markCompleted(job analysisjob.AnalysisJob) {
	ctx, cancel := queueOpContext()
	defer cancel()
	if err := w.jobs.MarkCompleted(ctx, job.ID); err != nil {
		w.logger.Error("mark job completed failed", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}
}

func (w *Worker) markFailed(job analysisjob.AnalysisJob, reason string) {
	ctx, cancel := queueOpContext()
	defer cancel()
	if err := w.jobs.MarkFailed(ctx, job.ID, job.Attempt, reason); err != nil {
		w.logger.Error("mark job failed (retry) failed", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}
}

func (w *Worker) markDeadLetter(jobID, reason string) {
	ctx, cancel := queueOpContext()
	defer cancel()
	if err := w.jobs.MarkDeadLetter(ctx, jobID, reason); err != nil {
		w.logger.Error("mark job dead-letter failed", slog.String("job_id", jobID), slog.String("error", err.Error()))
	}
}

// notifyDeadLetter runs the completion coordinator (7.6.6) for a job
// that's being permanently given up on — see Handler.OnDeadLetter's
// doc comment for why a dead-lettered modality still needs to trigger
// this the same way a successful one does.
func (w *Worker) notifyDeadLetter(job analysisjob.AnalysisJob) {
	ctx, cancel := queueOpContext()
	defer cancel()
	if err := w.handler.OnDeadLetter(ctx, job); err != nil {
		w.logger.Error("on-dead-letter completion check failed",
			slog.String("job_id", job.ID), slog.String("session_id", job.SessionID), slog.String("error", err.Error()))
	}
}

// handlerPanicError wraps a recovered panic from Handler.Handle.
// Always treated as permanent (see process): a handler panicking on a
// given job is almost always a bug that will reproduce identically on
// retry, not a transient condition — and even if it weren't, silently
// looping a panicking handler is worse than dead-lettering it for a
// human to look at.
type handlerPanicError struct {
	value any
}

func (e *handlerPanicError) Error() string {
	return fmt.Sprintf("handler panicked: %v", e.value)
}

// safeHandle runs handler.Handle with panic recovery — a bug in one
// Handler implementation (this package ships two: VideoHandler,
// AudioHandler) must not take down the whole worker goroutine, let
// alone the gateway process. This is what "the worker itself must
// remain healthy" (7.5.7) means in practice: no single job, however
// badly it fails, gets to end the process that's supposed to keep
// serving every other job.
func (w *Worker) safeHandle(ctx context.Context, job analysisjob.AnalysisJob) (err error) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("handler panicked",
				slog.String("job_id", job.ID),
				slog.Any("panic", r),
			)
			err = &handlerPanicError{value: r}
		}
	}()
	return w.handler.Handle(ctx, job)
}

// failPermanently moves d to the dead-letter list and counts it —
// shared by the "can't even decode" and "exhausted retries / permanent
// error" paths, which both end the same way (7.5.6). Builds its own
// context (see process's doc comment) rather than accepting one from
// its caller, so it can never be undermined by the run-loop context a
// caller further up might otherwise have passed in.
func (w *Worker) failPermanently(d queue.Delivery, reason string) {
	ctx, cancel := queueOpContext()
	defer cancel()
	if err := w.queue.Fail(ctx, d, reason); err != nil {
		w.logger.Error("fail (dead-letter) failed", slog.String("error", err.Error()))
	}
	w.metrics.jobsDeadLettered.Add(1)
}

// retry waits out this attempt's backoff, then re-enqueues job with
// Attempt incremented and LastError set (7.4.5's metadata, finally put
// to use) and acks the original delivery — the retry lives as a new
// queue entry, not a mutation of the in-flight one.
//
// ctx (the worker's run-loop context) bounds only the backoff wait —
// so Stop() doesn't have to sit out someone else's 30-second delay to
// shut down — and nothing past that point: once the wait ends, for
// whatever reason, preparing and requeuing the retry always runs to
// completion on its own independent context (see process's doc
// comment for why that separation matters).
func (w *Worker) retry(ctx context.Context, d queue.Delivery, job analysisjob.AnalysisJob, cause error) {
	retryJob := job.WithFailure(cause.Error())

	select {
	case <-time.After(backoffFor(retryJob.Attempt)):
	case <-ctx.Done():
		// Shutting down mid-backoff: don't make Stop() wait out the rest
		// of a delay nobody's running against right now — requeue
		// immediately instead of losing it.
	}

	qj, err := retryJob.ToQueueJob()
	if err != nil {
		// Can't happen in practice (retryJob only ever adds a string
		// field to an already-valid job), but if it ever did, this
		// attempt is unrecoverable — dead-letter rather than spin.
		w.failPermanently(d, "failed to prepare retry: "+err.Error())
		return
	}

	requeueCtx, cancel := queueOpContext()
	defer cancel()

	if err := w.queue.Enqueue(requeueCtx, qj); err != nil {
		// Couldn't put the retry back — the safest thing left to do is
		// dead-letter the original rather than silently lose it.
		w.failPermanently(d, "retry enqueue failed: "+err.Error())
		return
	}
	if err := w.queue.Ack(requeueCtx, d); err != nil {
		w.logger.Error("ack of original delivery after successful retry-enqueue failed",
			slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}
	w.metrics.jobsRetried.Add(1)
}

// backoffFor returns 7.5.5's exponential backoff for the upcoming
// attempt number (2 = about to run the first retry, 3 = the second,
// ...), capped at retryMaxDelay.
func backoffFor(attempt int) time.Duration {
	exponent := attempt - 2
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 20 { // guard against overflow from a runaway MaxAttempts
		exponent = 20
	}
	delay := retryBaseDelay * time.Duration(1<<uint(exponent))
	if delay <= 0 || delay > retryMaxDelay {
		delay = retryMaxDelay
	}
	return delay
}
