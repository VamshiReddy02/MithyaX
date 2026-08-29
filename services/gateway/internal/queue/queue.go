// Package queue defines the gateway's job queue abstraction and its
// Redis-backed implementation — the first piece of Phase 7.3's
// "Gateway → Redis Queue → workers" pipeline. Workers that actually
// consume from a Queue are Phase 7.5's job: this package only
// establishes the interface and proves it works against real Redis, so
// the gateway (and, later, workers) depend on Queue rather than on
// Redis directly — the same separation internal/repository draws
// between business logic and PostgreSQL.
package queue

import (
	"context"
	"time"
)

// Job is one unit of asynchronous work waiting to be picked up by a
// worker. Type names what kind of work this is (e.g. "video_analysis",
// "audio_analysis") for the benefit of future workers that each handle
// one kind (Phase 7.5) — Queue itself never inspects it. Payload is
// that job's own data, opaque to the queue.
type Job struct {
	ID        string
	Type      string
	Payload   []byte
	CreatedAt time.Time
}

// Queue is a durable, FIFO job queue: jobs enqueued here survive a
// gateway restart and are visible to any process sharing the same
// backing store, unlike an in-process channel. *Redis is the only
// implementation today.
type Queue interface {
	// Enqueue durably adds job to the queue.
	Enqueue(ctx context.Context, job Job) error
	// Dequeue blocks until a job is available or ctx is done, then
	// removes and returns it. Once returned, the job is gone from the
	// queue — there's no ack/requeue-on-failure semantics yet; that's
	// meaningless before Phase 7.5 gives it a worker that can actually
	// fail partway through one.
	Dequeue(ctx context.Context) (Job, error)
}
