package analysisworker

import (
	"context"
	"errors"
	"fmt"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
)

// Coordinator decides whether a just-finished modality result should
// trigger the combined risk calculation now, or wait for the other
// modality (7.6.6) — "the worker shouldn't blindly calculate risk
// after only one modality unless that's explicitly desired." It's a
// thin read against the jobs repository, the durable source of truth
// for what jobs exist and where they stand (7.6.3): if the other
// modality was never requested for this session, or has already
// reached a terminal state (completed or dead-lettered) itself,
// there's nothing left to wait for; otherwise, hold off.
//
// This makes the final result order-independent — video finishing
// first or audio finishing first both funnel through the same check.
type Coordinator struct {
	jobs jobsrepo.Repository
}

// NewCoordinator builds a Coordinator backed by jobs.
func NewCoordinator(jobs jobsrepo.Repository) *Coordinator {
	return &Coordinator{jobs: jobs}
}

// ShouldFinalize reports whether, after completed's job for sessionID
// reached a terminal state (success or dead-letter), the combined risk
// should be computed now.
func (c *Coordinator) ShouldFinalize(ctx context.Context, sessionID string, completed analysisjob.Type) (bool, error) {
	other := otherModality(completed)

	otherJob, err := c.jobs.GetLatestBySessionAndType(ctx, sessionID, string(other))
	if errors.Is(err, jobsrepo.ErrNotFound) {
		return true, nil // the other modality was never requested — nothing to wait for
	}
	if err != nil {
		return false, fmt.Errorf("check for outstanding %s job: %w", other, err)
	}
	return otherJob.Status.IsTerminal(), nil
}

// otherModality returns the modality that isn't t — this package only
// ever deals in exactly two (video, audio), so there's no need for a
// more general lookup.
func otherModality(t analysisjob.Type) analysisjob.Type {
	if t == analysisjob.TypeVideoAnalysis {
		return analysisjob.TypeAudioAnalysis
	}
	return analysisjob.TypeVideoAnalysis
}
