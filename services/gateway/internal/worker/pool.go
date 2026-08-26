package worker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
)

// ErrPoolClosed is returned by Submit once Shutdown has been called.
var ErrPoolClosed = errors.New("worker pool is shut down")

// maxAttempts bounds retries for a job whose detector call fails with a
// retryable error (see isRetryable). The first try plus up to
// maxAttempts-1 retries.
const maxAttempts = 3

// retryBaseDelay is the linear backoff unit between retry attempts:
// attempt 1 waits retryBaseDelay, attempt 2 waits 2*retryBaseDelay, etc.
const retryBaseDelay = 500 * time.Millisecond

// DetectorClient analyzes a video. detector.Client implements it; tests
// can supply a fake.
type DetectorClient interface {
	Analyze(ctx context.Context, videoURL string) (*detector.Result, error)
}

// Pool runs a fixed number of worker goroutines that pull job IDs off a
// Redis-backed Queue and run them against the video-detector, recording
// each job's progress and result in a Redis-backed Store.
type Pool struct {
	queue  *Queue
	store  *Store
	client DetectorClient
	logger *slog.Logger
	wg     sync.WaitGroup

	mu         sync.Mutex
	closed     bool
	workCtx    context.Context
	cancelWork context.CancelFunc
}

// NewPool builds a Pool. Call Start to launch its workers.
func NewPool(queue *Queue, store *Store, client DetectorClient, logger *slog.Logger) *Pool {
	workCtx, cancel := context.WithCancel(context.Background())
	return &Pool{
		queue:      queue,
		store:      store,
		client:     client,
		logger:     logger,
		workCtx:    workCtx,
		cancelWork: cancel,
	}
}

// Start launches workerCount worker goroutines. Call once, before any
// calls to Submit.
func (p *Pool) Start(workerCount int) {
	for i := 0; i < workerCount; i++ {
		p.wg.Add(1)
		go p.runWorker()
	}
}

func (p *Pool) runWorker() {
	defer p.wg.Done()

	for {
		if p.workCtx.Err() != nil {
			return
		}

		jobID, err := p.queue.Dequeue(p.workCtx)
		if err != nil {
			if errors.Is(err, errNoJob) {
				continue
			}
			if p.workCtx.Err() != nil {
				return
			}
			p.logger.Warn("dequeue failed, retrying shortly", slog.String("error", err.Error()))
			time.Sleep(retryBaseDelay)
			continue
		}

		job, err := p.store.Get(context.Background(), jobID)
		if err != nil {
			p.logger.Warn("failed to load dequeued job", slog.String("id", jobID), slog.String("error", err.Error()))
			continue
		}

		p.processWithRecover(job)
	}
}

// processWithRecover runs process, converting a panic into a failed job
// instead of taking down the worker goroutine — one bad job shouldn't
// stop this worker from picking up the next one.
func (p *Pool) processWithRecover(job Job) {
	defer func() {
		if r := recover(); r != nil {
			job.Status = StatusFailed
			job.Error = fmt.Sprintf("worker panic: %v", r)
			job.UpdatedAt = time.Now()
			if err := p.store.Put(context.Background(), job); err != nil {
				p.logger.Error("failed to record panicked job", slog.String("id", job.ID), slog.String("error", err.Error()))
			}
			p.logger.Error("worker recovered from panic", slog.String("id", job.ID), slog.Any("panic", r))
		}
	}()
	p.process(job)
}

func (p *Pool) process(job Job) {
	job.Status = StatusProcessing
	job.UpdatedAt = time.Now()
	if err := p.store.Put(context.Background(), job); err != nil {
		p.logger.Warn("failed to record job as processing", slog.String("id", job.ID), slog.String("error", err.Error()))
	}

	var result *detector.Result
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		job.Attempts = attempt
		result, lastErr = p.client.Analyze(context.Background(), job.VideoURL)
		if lastErr == nil {
			break
		}
		if !isRetryable(lastErr) {
			break
		}
		if attempt < maxAttempts {
			p.logger.Warn("job attempt failed, retrying",
				slog.String("id", job.ID),
				slog.Int("attempt", attempt),
				slog.String("error", lastErr.Error()),
			)
			time.Sleep(retryBaseDelay * time.Duration(attempt))
		}
	}

	job.UpdatedAt = time.Now()
	if lastErr != nil {
		job.Status = StatusFailed
		job.Error = lastErr.Error()
		if err := p.store.Put(context.Background(), job); err != nil {
			p.logger.Error("failed to record failed job", slog.String("id", job.ID), slog.String("error", err.Error()))
		}
		p.logger.Warn("job failed", slog.String("id", job.ID), slog.Int("attempts", job.Attempts), slog.String("error", lastErr.Error()))
		return
	}

	job.Status = StatusCompleted
	job.Result = result
	if err := p.store.Put(context.Background(), job); err != nil {
		p.logger.Error("failed to record completed job", slog.String("id", job.ID), slog.String("error", err.Error()))
	}
}

// isRetryable reports whether a failed detector call is worth retrying.
// An invalid video (corrupt file, unreachable URL, unsupported format)
// will fail identically on every retry, so those aren't retried; timeouts
// and connectivity/server errors are transient and are.
func isRetryable(err error) bool {
	var derr *detector.Error
	if errors.As(err, &derr) {
		return derr.Kind != detector.KindInvalidVideo
	}
	return true
}

// Submit creates a queued Job for videoURL, records it in the Store, and
// enqueues it for a worker to pick up. Returns ErrQueueFull if the queue
// is full, ErrRedisUnavailable if Redis couldn't be reached, or
// ErrPoolClosed once Shutdown has been called.
func (p *Pool) Submit(videoURL string) (Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return Job{}, ErrPoolClosed
	}

	id, err := newJobID()
	if err != nil {
		return Job{}, err
	}

	now := time.Now()
	job := Job{
		ID:        id,
		VideoURL:  videoURL,
		Status:    StatusQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}

	ctx := context.Background()
	if err := p.store.Put(ctx, job); err != nil {
		return Job{}, err
	}

	if err := p.queue.Enqueue(ctx, job.ID); err != nil {
		// The job is recorded but never made it onto the queue — mark it
		// failed rather than leaving an orphaned "queued" job that no
		// worker will ever find.
		job.Status = StatusFailed
		job.Error = err.Error()
		job.UpdatedAt = time.Now()
		_ = p.store.Put(ctx, job) // best effort; Submit's own error return still surfaces the failure
		return job, err
	}
	return job, nil
}

// Shutdown stops accepting new jobs (further Submit calls return
// ErrPoolClosed) and waits for in-flight jobs to finish, or for ctx to
// be done, whichever comes first. Jobs already queued in Redis but not
// yet picked up are left there — they'll be picked up by the next
// process (this one, on restart, or another gateway instance) to start,
// since the queue lives in Redis, not this process's memory.
func (p *Pool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.cancelWork()
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// newJobID generates a random UUID v4 string.
func newJobID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
