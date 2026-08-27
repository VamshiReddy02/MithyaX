package temporal_test

import (
	"math"
	"strings"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
)

const floatEpsilon = 1e-9

func stableFrames() []temporal.Frame {
	scores := []float64{0.08, 0.09, 0.10, 0.09, 0.08, 0.09}
	frames := make([]temporal.Frame, len(scores))
	for i, s := range scores {
		frames[i] = temporal.Frame{Timestamp: float64(i), FakeScore: s, FaceDetected: true}
	}
	return frames
}

func TestAnalyze_EmptyFrames_ReturnsNil(t *testing.T) {
	got := temporal.NewAnalyzer().Analyze(nil)
	if got != nil {
		t.Errorf("Analyze(nil) = %+v, want nil", got)
	}

	got = temporal.NewAnalyzer().Analyze([]temporal.Frame{})
	if got != nil {
		t.Errorf("Analyze([]Frame{}) = %+v, want nil", got)
	}
}

func TestAnalyze_SingleFrame_InsufficientData(t *testing.T) {
	got := temporal.NewAnalyzer().Analyze([]temporal.Frame{
		{Timestamp: 0, FakeScore: 0.5, FaceDetected: true},
	})
	if got == nil {
		t.Fatal("Analyze() = nil, want a non-nil result flagged as insufficient")
	}
	if got.FramesAnalyzed != 1 {
		t.Errorf("FramesAnalyzed = %d, want 1", got.FramesAnalyzed)
	}
	if got.Score != 0 || got.FaceConsistency != 0 || got.ScoreVariance != 0 {
		t.Errorf("Score/FaceConsistency/ScoreVariance = %v/%v/%v, want all 0 (not meaningful for one frame)",
			got.Score, got.FaceConsistency, got.ScoreVariance)
	}
	if len(got.Reasons) != 1 {
		t.Fatalf("Reasons = %v, want exactly one reason", got.Reasons)
	}
}

func TestAnalyze_StableFrames_LowRisk(t *testing.T) {
	got := temporal.NewAnalyzer().Analyze(stableFrames())
	if got == nil {
		t.Fatal("Analyze() = nil, want a result")
	}
	if got.FramesAnalyzed != 6 {
		t.Errorf("FramesAnalyzed = %d, want 6", got.FramesAnalyzed)
	}
	if got.FaceConsistency != 1 {
		t.Errorf("FaceConsistency = %v, want 1 (face present in every frame)", got.FaceConsistency)
	}
	if got.Score > 0.1 {
		t.Errorf("Score = %v, want a low temporal risk score for stable frames", got.Score)
	}
	if len(got.Reasons) != 0 {
		t.Errorf("Reasons = %v, want none for stable frames", got.Reasons)
	}
}

// TestAnalyze_ScoreSpike_HigherRiskThanStable is the example from the
// task: a single frame spikes far above its low, steady neighbors.
//
//	Frame:       1    2    3    4    5    6
//	Fake score: .08  .09  .10  .82  .11  .09
//	                       ↑
//	                  suspicious spike
func TestAnalyze_ScoreSpike_HigherRiskThanStable(t *testing.T) {
	scores := []float64{0.08, 0.09, 0.10, 0.82, 0.11, 0.09}
	frames := make([]temporal.Frame, len(scores))
	for i, s := range scores {
		frames[i] = temporal.Frame{Timestamp: float64(i), FakeScore: s, FaceDetected: true}
	}

	got := temporal.NewAnalyzer().Analyze(frames)
	stable := temporal.NewAnalyzer().Analyze(stableFrames())

	if got == nil || stable == nil {
		t.Fatal("Analyze() = nil, want results for both cases")
	}
	if got.Score <= stable.Score {
		t.Errorf("spike Score = %v, want it higher than the stable baseline %v", got.Score, stable.Score)
	}

	found := false
	for _, r := range got.Reasons {
		if strings.Contains(r, "spike") {
			found = true
		}
	}
	if !found {
		t.Errorf("Reasons = %v, want a reason calling out the spike at frame 4", got.Reasons)
	}
}

func TestAnalyze_FaceDisappearsOften_HigherRiskThanStable(t *testing.T) {
	scores := []float64{0.08, 0.09, 0.10, 0.09, 0.08, 0.09, 0.08, 0.09, 0.10, 0.09}
	presence := []bool{true, false, true, false, true, false, true, false, true, false}
	frames := make([]temporal.Frame, len(scores))
	for i, s := range scores {
		frames[i] = temporal.Frame{Timestamp: float64(i), FakeScore: s, FaceDetected: presence[i]}
	}

	got := temporal.NewAnalyzer().Analyze(frames)
	stable := temporal.NewAnalyzer().Analyze(stableFrames())

	if got == nil || stable == nil {
		t.Fatal("Analyze() = nil, want results for both cases")
	}
	if got.FaceConsistency != 0.5 {
		t.Errorf("FaceConsistency = %v, want 0.5 (face present in half the frames)", got.FaceConsistency)
	}
	if got.Score <= stable.Score {
		t.Errorf("flickering-face Score = %v, want it higher than the stable baseline %v", got.Score, stable.Score)
	}
	if len(got.Reasons) == 0 {
		t.Error("Reasons is empty, want a reason calling out inconsistent face detection")
	}
}

func TestAnalyze_ScoreVariance_MatchesPopulationVariance(t *testing.T) {
	// Two frames, scores 0 and 1: mean 0.5, population variance
	// ((0-0.5)^2 + (1-0.5)^2) / 2 = 0.25 — the maximum possible.
	frames := []temporal.Frame{
		{Timestamp: 0, FakeScore: 0, FaceDetected: true},
		{Timestamp: 1, FakeScore: 1, FaceDetected: true},
	}

	got := temporal.NewAnalyzer().Analyze(frames)
	if got == nil {
		t.Fatal("Analyze() = nil, want a result")
	}
	if math.Abs(got.ScoreVariance-0.25) > floatEpsilon {
		t.Errorf("ScoreVariance = %v, want 0.25", got.ScoreVariance)
	}
}
