// Package jobs persists the durable lifecycle of an asynchronous
// analysis job (7.6.3). Redis (internal/queue) is only the transport
// that delivers a job to a worker to run; this package is the source
// of truth for where a job actually stands — queued, processing,
// completed, failed, or dead_letter — so a client polling
// GET /api/v1/analysis/jobs/:id, or the completion coordinator
// deciding whether a session's other modality is still outstanding
// (7.6.6), never depends on Redis being reachable or a job's current
// position in a list.
package jobs

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get and GetLatestBySessionAndType when no
// matching job exists.
var ErrNotFound = errors.New("job not found")

// Status is a job's current lifecycle state.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusDeadLetter Status = "dead_letter"
)

// IsTerminal reports whether a job in this status will never
// transition again. The completion coordinator (7.6.6) uses this to
// decide whether it's safe to stop waiting for a session's other
// modality: StatusQueued/StatusProcessing/StatusFailed all mean "still
// might produce a result," so waiting continues; only Completed or
// DeadLetter mean the job is truly done, one way or the other.
func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusDeadLetter
}

// Job is the persisted record of one asynchronous analysis job.
type Job struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id"`
	Type        string     `json:"type"`
	Status      Status     `json:"status"`
	Attempt     int        `json:"attempt"`
	MaxAttempts int        `json:"max_attempts"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Repository persists analysis job state. *Postgres is the only
// implementation; the HTTP handlers and internal/analysisworker depend
// on this interface instead.
type Repository interface {
	// Create records a newly-enqueued job with StatusQueued. Called by
	// the HTTP handler (7.6.1) before the job is actually enqueued into
	// Redis, so the durable record always exists first — see that
	// handler's doc comment for why that order matters.
	Create(ctx context.Context, job Job) error
	// Get looks up a job by ID.
	Get(ctx context.Context, id string) (*Job, error)
	// GetLatestBySessionAndType returns the most recently created job
	// of jobType for sessionID. Used by the completion coordinator to
	// answer "was the other modality even requested, and if so, is it
	// done yet."
	GetLatestBySessionAndType(ctx context.Context, sessionID, jobType string) (*Job, error)
	// MarkProcessing records that a worker has picked up the job for
	// the given attempt, setting StartedAt the first time this is
	// called (left unchanged on a later retry attempt, so it always
	// reflects when work on the job first began).
	MarkProcessing(ctx context.Context, id string, attempt int) error
	// MarkCompleted records successful completion.
	MarkCompleted(ctx context.Context, id string) error
	// MarkFailed records a failed attempt that will be retried,
	// updating Attempt and LastError. Status becomes StatusFailed
	// (a non-terminal, "back in the queue" state — see IsTerminal).
	MarkFailed(ctx context.Context, id string, attempt int, lastError string) error
	// MarkDeadLetter records a permanent failure: retries exhausted, or
	// the error was classified as unrecoverable.
	MarkDeadLetter(ctx context.Context, id string, lastError string) error
}
