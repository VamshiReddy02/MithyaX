package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ErrQueueFull is returned by Redis.Enqueue when the queue is already
// at its configured capacity.
var ErrQueueFull = errors.New("queue is full")

// ErrRedisUnavailable wraps a Redis connectivity failure, distinguishing
// "we can't tell right now" from an ordinary empty queue.
var ErrRedisUnavailable = errors.New("redis is unavailable")

// errNoJob is an internal sentinel: one BLPOP wait timed out with
// nothing queued. Not a failure — Dequeue just loops and tries again,
// the same way internal/worker.Queue.Dequeue does, so callers of this
// package's Dequeue only ever see a job, a real error, or ctx's own
// error — never "try again yourself".
var errNoJob = errors.New("no job available")

// dequeueBlock bounds a single BLPOP wait so Dequeue periodically
// rechecks ctx (e.g. for shutdown) instead of blocking forever.
const dequeueBlock = 2 * time.Second

// Redis is a Queue backed by a single Redis list (RPUSH/BLPOP), the
// same primitive internal/worker.Queue already uses for analyze jobs —
// generalized here to carry any Job rather than just an analyze job ID.
type Redis struct {
	client   *goredis.Client
	key      string
	capacity int
}

// NewRedis builds a Redis-backed Queue using key as the list name,
// holding at most capacity jobs at once. Separate Queue instances
// backed by different keys (e.g. one per Job.Type, in a later phase)
// share one Redis without interfering.
func NewRedis(client *goredis.Client, key string, capacity int) *Redis {
	return &Redis{client: client, key: key, capacity: capacity}
}

// Enqueue appends job to the queue. Returns ErrQueueFull if the queue
// is already at capacity, or ErrRedisUnavailable if Redis couldn't be
// reached.
//
// The capacity check and the push aren't atomic, so under heavy
// concurrent load the queue can briefly exceed capacity by a handful of
// jobs — the same soft-backpressure tradeoff internal/worker.Queue
// makes, acceptable for the same reason: this bounds runaway growth, it
// doesn't need to be a hard guarantee.
func (q *Redis) Enqueue(ctx context.Context, job Job) error {
	length, err := q.client.LLen(ctx, q.key).Result()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	if int(length) >= q.capacity {
		return ErrQueueFull
	}

	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}

	if err := q.client.RPush(ctx, q.key, data).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return nil
}

// Dequeue blocks (rechecking ctx every dequeueBlock) until a job is
// available, then pops and returns it.
func (q *Redis) Dequeue(ctx context.Context) (Job, error) {
	for {
		data, err := q.blpop(ctx)
		if errors.Is(err, errNoJob) {
			select {
			case <-ctx.Done():
				return Job{}, ctx.Err()
			default:
				continue
			}
		}
		if err != nil {
			return Job{}, err
		}

		var job Job
		if err := json.Unmarshal(data, &job); err != nil {
			return Job{}, fmt.Errorf("unmarshal job: %w", err)
		}
		return job, nil
	}
}

type blpopResult struct {
	data []byte
	err  error
}

// blpop runs one bounded BLPOP in its own goroutine, raced against
// ctx.Done(): some Redis clients/servers don't reliably interrupt an
// in-flight blocking command the instant a context is cancelled (only
// once its own server-side timeout elapses), which would otherwise make
// shutdown less responsive than it should be — the same issue and fix
// as internal/worker.Queue.Dequeue. The stray goroutine, if BLPOP itself
// never returns promptly, still exits on its own once dequeueBlock
// elapses; the buffered channel means it never leaks blocked forever.
func (q *Redis) blpop(ctx context.Context) ([]byte, error) {
	resultCh := make(chan blpopResult, 1)

	go func() {
		result, err := q.client.BLPop(ctx, dequeueBlock, q.key).Result()
		switch {
		case errors.Is(err, goredis.Nil):
			resultCh <- blpopResult{err: errNoJob}
		case err != nil:
			resultCh <- blpopResult{err: fmt.Errorf("%w: %v", ErrRedisUnavailable, err)}
		case len(result) != 2: // BLPop returns [key, value]; we only ever query one key
			resultCh <- blpopResult{err: fmt.Errorf("unexpected BLPOP result shape: %v", result)}
		default:
			resultCh <- blpopResult{data: []byte(result[1])}
		}
	}()

	select {
	case res := <-resultCh:
		return res.data, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
