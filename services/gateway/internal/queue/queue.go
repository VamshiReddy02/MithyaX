// Package queue defines the gateway's job queue abstraction and its
// Redis-backed implementation — the "Gateway → Redis Queue → workers"
// pipeline from Phase 7.3, now (7.4) reliable enough to actually carry
// jobs: a Dequeue that crashes a consumer before it acknowledges
// doesn't lose the job, only delays it. Workers that consume from a
// Queue are still Phase 7.5's job: this package establishes the
// interface and proves it works against real Redis, so the gateway
// (and, later, workers) depend on Queue rather than on Redis directly —
// the same separation internal/repository draws between business logic
// and PostgreSQL.
package queue

import (
	"context"
	"time"
)

// Job is one unit of asynchronous work waiting to be picked up by a
// worker. Type names what kind of work this is (e.g. "VIDEO_ANALYSIS")
// for the benefit of future workers that each handle one kind (Phase
// 7.5) — Queue itself never inspects it. Payload is that job's own
// data, opaque to the queue; see internal/analysisjob for the
// domain-specific record actually carried in Payload for analysis work.
type Job struct {
	ID        string
	Type      string
	Payload   []byte
	CreatedAt time.Time
}

// Delivery is one Job popped off a Queue, carrying whatever that
// Queue implementation needs to later Ack or Fail this specific
// delivery. Only a Queue's own Dequeue can produce a valid one — the
// backing field is unexported so a caller can't fabricate a Delivery
// that doesn't correspond to a real in-flight job.
type Delivery struct {
	Job Job
	raw []byte
}

// Queue is a durable, FIFO job queue: jobs enqueued here survive a
// gateway restart and are visible to any process sharing the same
// backing store, unlike an in-process channel. *Redis is the only
// implementation today.
type Queue interface {
	// Enqueue durably adds job to the queue.
	Enqueue(ctx context.Context, job Job) error
	// Dequeue blocks until a job is available or ctx is done, then
	// moves it into an in-progress state and returns it as a Delivery.
	// The job is NOT removed from durable storage until Ack or Fail is
	// called: a consumer that crashes after Dequeue but before
	// acknowledging leaves the job recoverable rather than silently
	// gone. (Nothing recovers it automatically yet — that redelivery
	// loop is Phase 7.5's retry scheduler. 7.4 only guarantees the job
	// isn't lost, sitting inspectable via Redis.ProcessingLen.)
	Dequeue(ctx context.Context) (Delivery, error)
	// Ack marks a Delivery as successfully completed, permanently
	// removing it.
	Ack(ctx context.Context, d Delivery) error
	// Fail marks a Delivery as failed, recording reason in a
	// dead-letter list rather than silently dropping it. This does not
	// retry — deciding whether/when a failed job gets another attempt
	// is Phase 7.5's job, not this package's.
	Fail(ctx context.Context, d Delivery, reason string) error
}
