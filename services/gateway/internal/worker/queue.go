package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrQueueFull is returned by Queue.Enqueue when the queue is already at
// its configured capacity.
var ErrQueueFull = errors.New("job queue is full")

// errNoJob is an internal sentinel: Dequeue's blocking wait timed out
// with nothing to pop. Not a failure — callers just loop and try again.
var errNoJob = errors.New("no job available")

const queueKey = "mithyax:job-queue"

// dequeueBlock bounds a single BLPOP wait so a worker periodically
// rechecks its context (e.g. for shutdown) instead of blocking forever.
const dequeueBlock = 2 * time.Second

// Queue is a Redis-backed FIFO of job IDs — the actual queue boundary
// between submission and processing. Unlike an in-process channel, jobs
// queued here survive a gateway restart and are visible to any gateway
// instance sharing the same Redis.
type Queue struct {
	redis    *redis.Client
	capacity int
}

// NewQueue builds a Queue backed by client, holding at most capacity job
// IDs at once.
func NewQueue(client *redis.Client, capacity int) *Queue {
	return &Queue{redis: client, capacity: capacity}
}

// Enqueue appends jobID to the queue. Returns ErrQueueFull if the queue
// is already at capacity, or ErrRedisUnavailable if Redis couldn't be
// reached.
//
// The capacity check and the push aren't atomic, so under heavy
// concurrent load the queue can briefly exceed capacity by a handful of
// jobs — an acceptable tradeoff for a soft backpressure limit, not a
// hard correctness guarantee.
func (q *Queue) Enqueue(ctx context.Context, jobID string) error {
	length, err := q.redis.LLen(ctx, queueKey).Result()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	if int(length) >= q.capacity {
		return ErrQueueFull
	}

	if err := q.redis.RPush(ctx, queueKey, jobID).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return nil
}

type dequeueResult struct {
	id  string
	err error
}

// Dequeue blocks (up to dequeueBlock, or until ctx is done) waiting for
// a job ID to become available, then pops and returns it.
//
// BLPOP is run in its own goroutine and raced against ctx.Done(): some
// Redis clients/servers don't reliably interrupt an in-flight blocking
// command the instant a context is cancelled (only once its own
// server-side timeout elapses), which would otherwise make shutdown less
// responsive than it should be. The stray goroutine, if the BLPOP call
// itself never returns promptly, still exits on its own once dequeueBlock
// elapses; the buffered channel means it never leaks blocked forever.
func (q *Queue) Dequeue(ctx context.Context) (string, error) {
	resultCh := make(chan dequeueResult, 1)

	go func() {
		result, err := q.redis.BLPop(ctx, dequeueBlock, queueKey).Result()
		switch {
		case errors.Is(err, redis.Nil):
			resultCh <- dequeueResult{err: errNoJob}
		case err != nil:
			resultCh <- dequeueResult{err: fmt.Errorf("%w: %v", ErrRedisUnavailable, err)}
		case len(result) != 2: // BLPop returns [key, value]; we only ever query one key
			resultCh <- dequeueResult{err: fmt.Errorf("unexpected BLPOP result shape: %v", result)}
		default:
			resultCh <- dequeueResult{id: result[1]}
		}
	}()

	select {
	case res := <-resultCh:
		return res.id, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
