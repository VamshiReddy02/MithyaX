// Package worker runs video analysis jobs on a bounded pool of goroutines
// backed by Redis: job state lives in Redis (so it survives a gateway
// restart and is visible to every gateway instance), and the queue
// itself is a Redis list, so POST /api/v1/analyze can return immediately
// instead of blocking the caller for the whole detector round trip.
package worker

import (
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
)

// Status is a Job's position in its lifecycle.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// Job is one video analysis request as it moves through the pool. Job
// values are immutable snapshots — the Store holds the current one per
// ID in Redis, replacing it wholesale on each transition.
type Job struct {
	ID        string           `json:"id"`
	VideoURL  string           `json:"video_url"`
	Status    Status           `json:"status"`
	Attempts  int              `json:"attempts"`
	Result    *detector.Result `json:"result,omitempty"`
	Error     string           `json:"error,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}
