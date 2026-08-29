package analysisjob_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
)

func TestNewVideoAnalysisJob(t *testing.T) {
	job, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}

	if job.ID == "" {
		t.Error("ID is empty")
	}
	if job.SessionID != "session-1" {
		t.Errorf("SessionID = %q, want %q", job.SessionID, "session-1")
	}
	if job.Type != analysisjob.TypeVideoAnalysis {
		t.Errorf("Type = %q, want %q", job.Type, analysisjob.TypeVideoAnalysis)
	}
	if job.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1 (a job's first attempt)", job.Attempt)
	}
	if job.MaxAttempts < 1 {
		t.Errorf("MaxAttempts = %d, want a positive default", job.MaxAttempts)
	}
	if job.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	payload, err := job.VideoPayload()
	if err != nil {
		t.Fatalf("VideoPayload() error = %v", err)
	}
	if payload.VideoURL != "https://example.com/video.mp4" {
		t.Errorf("VideoPayload().VideoURL = %q, want %q", payload.VideoURL, "https://example.com/video.mp4")
	}
}

func TestNewAudioAnalysisJob(t *testing.T) {
	job, err := analysisjob.NewAudioAnalysisJob("session-2", "https://example.com/audio.wav")
	if err != nil {
		t.Fatalf("NewAudioAnalysisJob() error = %v", err)
	}

	if job.Type != analysisjob.TypeAudioAnalysis {
		t.Errorf("Type = %q, want %q", job.Type, analysisjob.TypeAudioAnalysis)
	}

	payload, err := job.AudioPayload()
	if err != nil {
		t.Fatalf("AudioPayload() error = %v", err)
	}
	if payload.AudioURL != "https://example.com/audio.wav" {
		t.Errorf("AudioPayload().AudioURL = %q, want %q", payload.AudioURL, "https://example.com/audio.wav")
	}
}

func TestNewAnalysisJob_UniqueIDs(t *testing.T) {
	a, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/a.mp4")
	if err != nil {
		t.Fatalf("first NewVideoAnalysisJob() error = %v", err)
	}
	b, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/a.mp4")
	if err != nil {
		t.Fatalf("second NewVideoAnalysisJob() error = %v", err)
	}
	if a.ID == b.ID {
		t.Error("two jobs for the same session/URL got the same ID, want unique IDs per job instance")
	}
}

func TestAnalysisJob_ToFromQueueJob(t *testing.T) {
	original, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}

	qj, err := original.ToQueueJob()
	if err != nil {
		t.Fatalf("ToQueueJob() error = %v", err)
	}
	if qj.ID != original.ID {
		t.Errorf("queue.Job.ID = %q, want %q", qj.ID, original.ID)
	}
	if qj.Type != string(original.Type) {
		t.Errorf("queue.Job.Type = %q, want %q", qj.Type, original.Type)
	}

	roundTripped, err := analysisjob.FromQueueJob(qj)
	if err != nil {
		t.Fatalf("FromQueueJob() error = %v", err)
	}
	if roundTripped.ID != original.ID ||
		roundTripped.SessionID != original.SessionID ||
		roundTripped.Type != original.Type ||
		string(roundTripped.Payload) != string(original.Payload) ||
		roundTripped.Attempt != original.Attempt ||
		roundTripped.MaxAttempts != original.MaxAttempts {
		t.Errorf("round-tripped job = %+v, want %+v", roundTripped, original)
	}
	if !roundTripped.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("round-tripped CreatedAt = %v, want %v", roundTripped.CreatedAt, original.CreatedAt)
	}
}

func TestAnalysisJob_WithFailure(t *testing.T) {
	original, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}

	failed := original.WithFailure("video-detector timed out")

	if original.Attempt != 1 || original.LastError != "" {
		t.Errorf("WithFailure() mutated the receiver: original = %+v", original)
	}
	if failed.Attempt != 2 {
		t.Errorf("failed.Attempt = %d, want 2", failed.Attempt)
	}
	if failed.LastError != "video-detector timed out" {
		t.Errorf("failed.LastError = %q, want %q", failed.LastError, "video-detector timed out")
	}
}

func TestAnalysisJob_HasAttemptsRemaining(t *testing.T) {
	job, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}

	for job.HasAttemptsRemaining() {
		job = job.WithFailure("boom")
	}
	if job.Attempt != job.MaxAttempts {
		t.Errorf("Attempt = %d after exhausting retries, want it to equal MaxAttempts (%d)", job.Attempt, job.MaxAttempts)
	}
}

// TestAnalysisJob_ThroughRealQueue is the domain-level version of
// 7.4.6's "Enqueue → Dequeue → deserialize → same Job": an AnalysisJob
// enqueued through a real (miniredis-backed) Queue comes back out
// byte-for-byte equivalent, proving the full Go struct → JSON → Redis →
// JSON → Go struct path this package actually adds on top of the plain
// queue.Job envelope internal/queue already tests on its own.
func TestAnalysisJob_ThroughRealQueue(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()

	q := queue.NewRedis(client, "test:analysis", 10)

	original, err := analysisjob.NewAudioAnalysisJob("session-42", "https://example.com/audio.wav")
	if err != nil {
		t.Fatalf("NewAudioAnalysisJob() error = %v", err)
	}

	qj, err := original.ToQueueJob()
	if err != nil {
		t.Fatalf("ToQueueJob() error = %v", err)
	}
	if err := q.Enqueue(context.Background(), qj); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}

	roundTripped, err := analysisjob.FromQueueJob(d.Job)
	if err != nil {
		t.Fatalf("FromQueueJob() error = %v", err)
	}
	if roundTripped.ID != original.ID || roundTripped.SessionID != original.SessionID {
		t.Errorf("roundTripped = %+v, want ID/SessionID matching %+v", roundTripped, original)
	}
	payload, err := roundTripped.AudioPayload()
	if err != nil {
		t.Fatalf("AudioPayload() error = %v", err)
	}
	if payload.AudioURL != "https://example.com/audio.wav" {
		t.Errorf("AudioPayload().AudioURL = %q, want %q", payload.AudioURL, "https://example.com/audio.wav")
	}

	if err := q.Ack(context.Background(), d); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
}
