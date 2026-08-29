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

// errNoJob is an internal sentinel: one BLMOVE wait timed out with
// nothing queued. Not a failure — Dequeue just loops and tries again,
// so callers of this package's Dequeue only ever see a job, a real
// error, or ctx's own error — never "try again yourself".
var errNoJob = errors.New("no job available")

// dequeueBlock bounds a single BLMOVE wait so Dequeue periodically
// rechecks ctx (e.g. for shutdown) instead of blocking forever.
const dequeueBlock = 2 * time.Second

// Redis is a Queue backed by three Redis lists under one key prefix:
// the pending queue itself, a processing list holding everything
// currently dequeued-but-not-yet-acknowledged, and a failed list
// recording permanently-failed deliveries. Dequeue uses BLMOVE to pop
// from pending and push into processing atomically — the job exists in
// exactly one of the three lists at all times, so a consumer that dies
// mid-job leaves it sitting in processing rather than gone.
type Redis struct {
	client   *goredis.Client
	key      string
	capacity int
}

// NewRedis builds a Redis-backed Queue using key as the base list name
// (its processing and failed lists are key+":processing" and
// key+":failed"), holding at most capacity pending jobs at once.
// Separate Queue instances backed by different keys (e.g. one per
// Job.Type, in a later phase) share one Redis without interfering.
func NewRedis(client *goredis.Client, key string, capacity int) *Redis {
	return &Redis{client: client, key: key, capacity: capacity}
}

func (q *Redis) processingKey() string { return q.key + ":processing" }
func (q *Redis) failedKey() string     { return q.key + ":failed" }

// Enqueue appends job to the pending list. Returns ErrQueueFull if the
// queue is already at capacity, or ErrRedisUnavailable if Redis
// couldn't be reached.
//
// The capacity check and the push aren't atomic, so under heavy
// concurrent load the queue can briefly exceed capacity by a handful of
// jobs — a soft backpressure limit, not a hard correctness guarantee
// (the same tradeoff internal/worker.Queue makes).
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
// available, atomically moves it from pending into processing, and
// returns it. The job stays in processing until Ack or Fail removes it.
func (q *Redis) Dequeue(ctx context.Context) (Delivery, error) {
	for {
		data, err := q.blmove(ctx)
		if errors.Is(err, errNoJob) {
			select {
			case <-ctx.Done():
				return Delivery{}, ctx.Err()
			default:
				continue
			}
		}
		if err != nil {
			return Delivery{}, err
		}

		var job Job
		if err := json.Unmarshal(data, &job); err != nil {
			return Delivery{}, fmt.Errorf("unmarshal job: %w", err)
		}
		return Delivery{Job: job, raw: data}, nil
	}
}

type blmoveResult struct {
	data []byte
	err  error
}

// blmove runs one bounded BLMOVE (pending → processing) in its own
// goroutine, raced against ctx.Done(): some Redis clients/servers don't
// reliably interrupt an in-flight blocking command the instant a
// context is cancelled (only once its own server-side timeout
// elapses), which would otherwise make shutdown less responsive than it
// should be — the same issue and fix as internal/worker.Queue.Dequeue.
// The stray goroutine, if BLMOVE itself never returns promptly, still
// exits on its own once dequeueBlock elapses; the buffered channel
// means it never leaks blocked forever.
//
// LEFT/RIGHT preserve FIFO order relative to Enqueue's RPUSH: the
// oldest pending job sits at the head (LEFT) of the pending list, and
// BLMOVE takes from there, same end BLPOP always popped from.
func (q *Redis) blmove(ctx context.Context) ([]byte, error) {
	resultCh := make(chan blmoveResult, 1)

	go func() {
		result, err := q.client.BLMove(ctx, q.key, q.processingKey(), "LEFT", "RIGHT", dequeueBlock).Result()
		switch {
		case errors.Is(err, goredis.Nil):
			resultCh <- blmoveResult{err: errNoJob}
		case err != nil:
			resultCh <- blmoveResult{err: fmt.Errorf("%w: %v", ErrRedisUnavailable, err)}
		default:
			resultCh <- blmoveResult{data: []byte(result)}
		}
	}()

	select {
	case res := <-resultCh:
		return res.data, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Ack removes d from the processing list, permanently completing it.
func (q *Redis) Ack(ctx context.Context, d Delivery) error {
	if err := q.client.LRem(ctx, q.processingKey(), 1, d.raw).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return nil
}

// FailedDelivery is what Fail records in the dead-letter list: enough
// to see what the job was and why it didn't complete.
type FailedDelivery struct {
	Job      Job       `json:"job"`
	Reason   string    `json:"reason"`
	FailedAt time.Time `json:"failed_at"`
}

// Fail removes d from the processing list and records it, with reason,
// in the failed list. It does not inspect or update Job.Payload — a
// caller wanting the next attempt's metadata (see
// analysisjob.AnalysisJob.WithFailure) re-enqueues that itself; Fail
// only guarantees the failed delivery isn't silently dropped.
func (q *Redis) Fail(ctx context.Context, d Delivery, reason string) error {
	if err := q.client.LRem(ctx, q.processingKey(), 1, d.raw).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}

	entry := FailedDelivery{Job: d.Job, Reason: reason, FailedAt: time.Now().UTC()}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal failed delivery: %w", err)
	}
	if err := q.client.RPush(ctx, q.failedKey(), data).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return nil
}

// PendingLen, ProcessingLen, and FailedLen report how many jobs are
// currently waiting, in-flight (dequeued but not yet acked/failed), and
// permanently failed. Never used by the queue's own logic — for tests
// and future metrics/health reporting.
func (q *Redis) PendingLen(ctx context.Context) (int64, error) {
	return q.length(ctx, q.key)
}

func (q *Redis) ProcessingLen(ctx context.Context) (int64, error) {
	return q.length(ctx, q.processingKey())
}

func (q *Redis) FailedLen(ctx context.Context) (int64, error) {
	return q.length(ctx, q.failedKey())
}

func (q *Redis) length(ctx context.Context, key string) (int64, error) {
	n, err := q.client.LLen(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return n, nil
}
