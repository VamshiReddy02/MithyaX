// Package analysisjob defines the durable, asynchronous analysis job
// record — the domain-specific payload carried inside a generic
// internal/queue.Job's Payload field. This is deliberately a separate
// concern from internal/queue (which knows nothing about video, audio,
// or sessions) and from internal/realtime (the live WebSocket
// pipeline, which this package has nothing to do with — see the
// package's own note on that boundary). Phase 7.5 is what actually
// builds a worker that consumes these; this package only defines the
// record and proves it serializes correctly.
package analysisjob

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
)

// Type identifies what kind of analysis a job asks for.
type Type string

const (
	TypeVideoAnalysis Type = "VIDEO_ANALYSIS"
	TypeAudioAnalysis Type = "AUDIO_ANALYSIS"
)

// defaultMaxAttempts bounds how many times a job may be attempted
// before it's considered permanently failed — a fixed sensible default
// until Phase 7.5's retry scheduler needs it to vary per job or type.
const defaultMaxAttempts = 3

// AnalysisJob is a reference to work a future video/audio worker
// (Phase 7.5) should do — never the media itself. Raw video/audio
// bytes have no business sitting in Redis: Payload only ever carries a
// small reference (see VideoPayload/AudioPayload) a worker fetches
// from.
//
// ID is this job's idempotency key. The queue this rides on is at
// least once, not exactly once (see queue.Queue.Dequeue and
// TestRedis_DuplicateJobID): a crash after Dequeue but before Ack
// leaves the job recoverable by design, which means it can genuinely
// be delivered, or even enqueued, more than once. A consumer must
// treat handling the same ID twice as a no-op, not redo (or
// double-record) the work. In practice this is close to free: since
// analysis_results.session_id is already a primary key (see
// internal/repository/analysis), a duplicate final write for the same
// session fails with a constraint violation a worker can treat as
// "already done" rather than corruption.
type AnalysisJob struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	Type        Type            `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"max_attempts"`
	LastError   string          `json:"last_error,omitempty"`
}

// VideoPayload is the Payload shape for a TypeVideoAnalysis job: a URL
// to fetch, not frame data.
type VideoPayload struct {
	VideoURL string `json:"video_url"`
}

// AudioPayload is the Payload shape for a TypeAudioAnalysis job: a URL
// to fetch, not audio data.
type AudioPayload struct {
	AudioURL string `json:"audio_url"`
}

// NewVideoAnalysisJob builds a ready-to-enqueue video analysis job for
// sessionID, referencing videoURL.
func NewVideoAnalysisJob(sessionID, videoURL string) (AnalysisJob, error) {
	return newJob(sessionID, TypeVideoAnalysis, VideoPayload{VideoURL: videoURL})
}

// NewAudioAnalysisJob builds a ready-to-enqueue audio analysis job for
// sessionID, referencing audioURL.
func NewAudioAnalysisJob(sessionID, audioURL string) (AnalysisJob, error) {
	return newJob(sessionID, TypeAudioAnalysis, AudioPayload{AudioURL: audioURL})
}

func newJob(sessionID string, jobType Type, payload any) (AnalysisJob, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return AnalysisJob{}, fmt.Errorf("marshal payload: %w", err)
	}
	id, err := newID()
	if err != nil {
		return AnalysisJob{}, fmt.Errorf("generate job id: %w", err)
	}
	return AnalysisJob{
		ID:          id,
		SessionID:   sessionID,
		Type:        jobType,
		Payload:     data,
		CreatedAt:   time.Now().UTC(),
		Attempt:     1,
		MaxAttempts: defaultMaxAttempts,
	}, nil
}

// VideoPayload decodes this job's Payload as a VideoPayload. Callers
// should check Type == TypeVideoAnalysis first — this doesn't.
func (j AnalysisJob) VideoPayload() (VideoPayload, error) {
	var p VideoPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return VideoPayload{}, fmt.Errorf("unmarshal video payload: %w", err)
	}
	return p, nil
}

// AudioPayload decodes this job's Payload as an AudioPayload. Callers
// should check Type == TypeAudioAnalysis first — this doesn't.
func (j AnalysisJob) AudioPayload() (AudioPayload, error) {
	var p AudioPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return AudioPayload{}, fmt.Errorf("unmarshal audio payload: %w", err)
	}
	return p, nil
}

// WithFailure returns a copy of j with Attempt incremented and
// LastError set — the shape a future retry scheduler (Phase 7.5) is
// expected to re-enqueue after a failed attempt. AnalysisJob values
// are never mutated in place; every transform returns a copy, the same
// convention risk.Assessment and realtime.OutMessage already follow.
func (j AnalysisJob) WithFailure(reason string) AnalysisJob {
	updated := j
	updated.Attempt++
	updated.LastError = reason
	return updated
}

// HasAttemptsRemaining reports whether this job may still be retried
// under its own MaxAttempts budget. Phase 7.4 doesn't act on this
// (there's no retry scheduler yet) — it exists so 7.5's scheduler has
// something to check.
func (j AnalysisJob) HasAttemptsRemaining() bool {
	return j.Attempt < j.MaxAttempts
}

// ToQueueJob wraps this AnalysisJob as a queue.Job ready for
// Queue.Enqueue: the generic envelope (id/type/payload/created_at)
// carrying this whole record as its own opaque Payload, so the queue
// package never needs to know AnalysisJob exists.
func (j AnalysisJob) ToQueueJob() (queue.Job, error) {
	data, err := json.Marshal(j)
	if err != nil {
		return queue.Job{}, fmt.Errorf("marshal analysis job: %w", err)
	}
	return queue.Job{ID: j.ID, Type: string(j.Type), Payload: data, CreatedAt: j.CreatedAt}, nil
}

// FromQueueJob unwraps an AnalysisJob previously wrapped by ToQueueJob.
func FromQueueJob(qj queue.Job) (AnalysisJob, error) {
	var job AnalysisJob
	if err := json.Unmarshal(qj.Payload, &job); err != nil {
		return AnalysisJob{}, fmt.Errorf("unmarshal analysis job: %w", err)
	}
	return job, nil
}

// newID generates a random UUID v4 string — the same algorithm
// internal/realtime uses for session IDs, duplicated rather than
// shared across two packages with no other reason to depend on each
// other for eight lines of math/rand-adjacent code.
func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
