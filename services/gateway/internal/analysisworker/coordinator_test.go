package analysisworker_test

import (
	"context"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/analysisworker"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
)

// TestCoordinator_OtherModalityNeverRequested proves 7.6.6's simplest
// case: a video-only session has nothing to wait for, so its video job
// finishing should trigger the combined risk calculation immediately.
func TestCoordinator_OtherModalityNeverRequested(t *testing.T) {
	repo := newFakeJobsRepo()
	c := analysisworker.NewCoordinator(repo)

	ready, err := c.ShouldFinalize(context.Background(), "session-video-only", analysisjob.TypeVideoAnalysis)
	if err != nil {
		t.Fatalf("ShouldFinalize() error = %v", err)
	}
	if !ready {
		t.Error("ShouldFinalize() = false, want true — the audio modality was never requested for this session")
	}
}

// TestCoordinator_OtherModalityStillOutstanding proves the "video
// result → audio missing → wait" half of 7.6.6.
func TestCoordinator_OtherModalityStillOutstanding(t *testing.T) {
	repo := newFakeJobsRepo()
	repo.put(jobsrepo.Job{ID: "audio-job", SessionID: "session-1", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusProcessing, CreatedAt: time.Now()})
	c := analysisworker.NewCoordinator(repo)

	ready, err := c.ShouldFinalize(context.Background(), "session-1", analysisjob.TypeVideoAnalysis)
	if err != nil {
		t.Fatalf("ShouldFinalize() error = %v", err)
	}
	if ready {
		t.Error("ShouldFinalize() = true, want false — the audio job is still processing")
	}
}

// TestCoordinator_OtherModalityCompleted proves the "audio exists →
// calculate risk" half of 7.6.6, and that it's order-independent: this
// is the exact same check regardless of which modality asks it.
func TestCoordinator_OtherModalityCompleted(t *testing.T) {
	repo := newFakeJobsRepo()
	repo.put(jobsrepo.Job{ID: "video-job", SessionID: "session-1", Type: string(analysisjob.TypeVideoAnalysis), Status: jobsrepo.StatusCompleted, CreatedAt: time.Now()})
	c := analysisworker.NewCoordinator(repo)

	ready, err := c.ShouldFinalize(context.Background(), "session-1", analysisjob.TypeAudioAnalysis)
	if err != nil {
		t.Fatalf("ShouldFinalize() error = %v", err)
	}
	if !ready {
		t.Error("ShouldFinalize() = false, want true — the video job already completed")
	}
}

// TestCoordinator_OtherModalityDeadLettered proves a permanently-failed
// sibling still unblocks finalization — otherwise a session where one
// modality dead-letters would wait forever for a risk assessment
// nothing will ever trigger.
func TestCoordinator_OtherModalityDeadLettered(t *testing.T) {
	repo := newFakeJobsRepo()
	repo.put(jobsrepo.Job{ID: "audio-job", SessionID: "session-1", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusDeadLetter, CreatedAt: time.Now()})
	c := analysisworker.NewCoordinator(repo)

	ready, err := c.ShouldFinalize(context.Background(), "session-1", analysisjob.TypeVideoAnalysis)
	if err != nil {
		t.Fatalf("ShouldFinalize() error = %v", err)
	}
	if !ready {
		t.Error("ShouldFinalize() = false, want true — a dead-lettered sibling is terminal too")
	}
}

// TestCoordinator_UsesLatestJobForType proves GetLatestBySessionAndType
// semantics matter here: an earlier attempt's terminal dead_letter
// status must not be what's consulted once a retry is in flight.
func TestCoordinator_UsesLatestJobForType(t *testing.T) {
	repo := newFakeJobsRepo()
	repo.put(jobsrepo.Job{ID: "audio-job-old", SessionID: "session-1", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusDeadLetter, CreatedAt: time.Now().Add(-time.Hour)})
	repo.put(jobsrepo.Job{ID: "audio-job-new", SessionID: "session-1", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusProcessing, CreatedAt: time.Now()})
	c := analysisworker.NewCoordinator(repo)

	ready, err := c.ShouldFinalize(context.Background(), "session-1", analysisjob.TypeVideoAnalysis)
	if err != nil {
		t.Fatalf("ShouldFinalize() error = %v", err)
	}
	if ready {
		t.Error("ShouldFinalize() = true, want false — the latest audio job is still processing, regardless of an older attempt's outcome")
	}
}
