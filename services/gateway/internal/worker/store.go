package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrJobNotFound is returned by Store.Get when no job exists under the
// given ID (never existed, or its TTL has expired).
var ErrJobNotFound = errors.New("job not found")

// ErrRedisUnavailable wraps a Redis connectivity failure, distinguishing
// "we can't tell right now" from ErrJobNotFound's "this job doesn't
// exist" — callers should treat the two very differently (503 vs 404).
var ErrRedisUnavailable = errors.New("redis is unavailable")

const jobKeyPrefix = "mithyax:job:"

// Store holds the current state of every job by ID in Redis. The pool
// writes to it as a job progresses; handlers read from it to answer
// status queries. Safe for concurrent use — it's just a thin wrapper
// over the Redis client, which is itself concurrency-safe.
type Store struct {
	redis *redis.Client
	ttl   time.Duration
}

// NewStore builds a Store backed by client. Every job written is given
// ttl to live in Redis before it's automatically cleaned up — refreshed
// on every write, so a job's TTL counts from its most recent update, not
// its creation.
func NewStore(client *redis.Client, ttl time.Duration) *Store {
	return &Store{redis: client, ttl: ttl}
}

// Put stores job, replacing whatever was previously stored under its ID
// and resetting its TTL.
func (s *Store) Put(ctx context.Context, job Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}

	if err := s.redis.Set(ctx, jobKeyPrefix+job.ID, data, s.ttl).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return nil
}

// Get returns the job stored under id. Returns ErrJobNotFound if it
// doesn't exist (or has expired), or ErrRedisUnavailable if Redis
// itself couldn't be reached.
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	data, err := s.redis.Get(ctx, jobKeyPrefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}

	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return Job{}, fmt.Errorf("unmarshal job: %w", err)
	}
	return job, nil
}
