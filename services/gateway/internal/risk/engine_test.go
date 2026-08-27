package risk_test

import (
	"math"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/session"
)

// floatEpsilon absorbs binary floating-point rounding (e.g. 0.05+0.1
// landing on 0.15000000000000002) in the score comparisons below.
const floatEpsilon = 1e-9

func float64Ptr(f float64) *float64 { return &f }

func TestEngine_AssessSignals(t *testing.T) {
	tests := []struct {
		name        string
		sig         risk.Signals
		wantScore   float64
		wantVerdict risk.Verdict
		wantVideo   *float64
		wantAudio   *float64
		wantReasons []string
	}{
		{
			name:        "both present, weighted average",
			sig:         risk.Signals{Video: 0.08, VideoOK: true, Audio: 0.91, AudioOK: true},
			wantScore:   0.495,
			wantVerdict: risk.VerdictSuspicious,
			wantVideo:   float64Ptr(0.08),
			wantAudio:   float64Ptr(0.91),
			wantReasons: []string{"Audio signal indicates likely synthetic speech"},
		},
		{
			name:        "both present, both low, likely authentic",
			sig:         risk.Signals{Video: 0.05, VideoOK: true, Audio: 0.1, AudioOK: true},
			wantScore:   0.075,
			wantVerdict: risk.VerdictLikelyAuthentic,
			wantVideo:   float64Ptr(0.05),
			wantAudio:   float64Ptr(0.1),
			wantReasons: nil,
		},
		{
			name:        "both present, both high, likely fake",
			sig:         risk.Signals{Video: 0.8, VideoOK: true, Audio: 0.95, AudioOK: true},
			wantScore:   0.875,
			wantVerdict: risk.VerdictLikelyFake,
			wantVideo:   float64Ptr(0.8),
			wantAudio:   float64Ptr(0.95),
			wantReasons: []string{
				"Audio signal indicates likely synthetic speech",
				"Video signal indicates likely synthetic or manipulated content",
			},
		},
		{
			name:        "only video present",
			sig:         risk.Signals{Video: 0.7, VideoOK: true, AudioError: "audio detector unreachable"},
			wantScore:   0.7,
			wantVerdict: risk.VerdictLikelyFake,
			wantVideo:   float64Ptr(0.7),
			wantAudio:   nil,
			wantReasons: []string{
				"Video signal indicates likely synthetic or manipulated content",
				"Audio analysis unavailable: audio detector unreachable",
			},
		},
		{
			name:        "only audio present",
			sig:         risk.Signals{Audio: 0.2, AudioOK: true, VideoError: "video detector unreachable"},
			wantScore:   0.2,
			wantVerdict: risk.VerdictLikelyAuthentic,
			wantVideo:   nil,
			wantAudio:   float64Ptr(0.2),
			wantReasons: []string{"Video analysis unavailable: video detector unreachable"},
		},
		{
			name:        "neither present, unknown",
			sig:         risk.Signals{VideoError: "video boom", AudioError: "audio boom"},
			wantScore:   0,
			wantVerdict: risk.VerdictUnknown,
			wantVideo:   nil,
			wantAudio:   nil,
			wantReasons: []string{
				"Video analysis unavailable: video boom",
				"Audio analysis unavailable: audio boom",
			},
		},
		{
			name:        "neither present, neither requested",
			sig:         risk.Signals{},
			wantScore:   0,
			wantVerdict: risk.VerdictUnknown,
			wantVideo:   nil,
			wantAudio:   nil,
			wantReasons: []string{"No analysis signals were available"},
		},
		{
			name:        "score exactly at suspicious threshold is suspicious",
			sig:         risk.Signals{Video: 0.3, VideoOK: true, Audio: 0.3, AudioOK: true},
			wantScore:   0.3,
			wantVerdict: risk.VerdictSuspicious,
			wantVideo:   float64Ptr(0.3),
			wantAudio:   float64Ptr(0.3),
			wantReasons: nil,
		},
		{
			name:        "score exactly at likely-fake threshold is likely fake",
			sig:         risk.Signals{Video: 0.6, VideoOK: true, Audio: 0.6, AudioOK: true},
			wantScore:   0.6,
			wantVerdict: risk.VerdictLikelyFake,
			wantVideo:   float64Ptr(0.6),
			wantAudio:   float64Ptr(0.6),
			wantReasons: []string{
				"Audio signal indicates likely synthetic speech",
				"Video signal indicates likely synthetic or manipulated content",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := risk.NewEngine().AssessSignals(tt.sig)

			if math.Abs(got.RiskScore-tt.wantScore) > floatEpsilon {
				t.Errorf("RiskScore = %v, want %v", got.RiskScore, tt.wantScore)
			}
			if got.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %v, want %v", got.Verdict, tt.wantVerdict)
			}
			if !equalPtr(got.Signals.Video, tt.wantVideo) {
				t.Errorf("Signals.Video = %v, want %v", derefOrNil(got.Signals.Video), derefOrNil(tt.wantVideo))
			}
			if !equalPtr(got.Signals.Audio, tt.wantAudio) {
				t.Errorf("Signals.Audio = %v, want %v", derefOrNil(got.Signals.Audio), derefOrNil(tt.wantAudio))
			}
			if !equalReasons(got.Reasons, tt.wantReasons) {
				t.Errorf("Reasons = %v, want %v", got.Reasons, tt.wantReasons)
			}
		})
	}
}

// TestEngine_Assess proves the AnalysisSession → Signals adapter wires
// FakeScore/nil/error through correctly, on top of the AssessSignals
// coverage above.
func TestEngine_Assess(t *testing.T) {
	s := &session.AnalysisSession{
		ID:     "session-1",
		Status: session.StatusCompleted,
		Video:  &session.VideoResult{FakeScore: 0.08, Verdict: "real"},
		Audio:  &session.AudioResult{FakeScore: 0.91, Verdict: "fake"},
	}

	got := risk.NewEngine().Assess(s)

	if math.Abs(got.RiskScore-0.495) > floatEpsilon {
		t.Errorf("RiskScore = %v, want 0.495", got.RiskScore)
	}
	if got.Verdict != risk.VerdictSuspicious {
		t.Errorf("Verdict = %v, want %v", got.Verdict, risk.VerdictSuspicious)
	}
	if !equalPtr(got.Signals.Video, float64Ptr(0.08)) {
		t.Errorf("Signals.Video = %v, want 0.08", derefOrNil(got.Signals.Video))
	}
	if !equalPtr(got.Signals.Audio, float64Ptr(0.91)) {
		t.Errorf("Signals.Audio = %v, want 0.91", derefOrNil(got.Signals.Audio))
	}
}

func TestEngine_Assess_PartialSessionOmitsFailedModality(t *testing.T) {
	s := &session.AnalysisSession{
		ID:         "session-2",
		Status:     session.StatusPartial,
		Audio:      &session.AudioResult{FakeScore: 0.91, Verdict: "fake"},
		VideoError: "video detector unreachable",
	}

	got := risk.NewEngine().Assess(s)

	if got.Signals.Video != nil {
		t.Errorf("Signals.Video = %v, want nil", *got.Signals.Video)
	}
	if !equalPtr(got.Signals.Audio, float64Ptr(0.91)) {
		t.Errorf("Signals.Audio = %v, want 0.91", derefOrNil(got.Signals.Audio))
	}
	if got.Verdict != risk.VerdictLikelyFake {
		t.Errorf("Verdict = %v, want %v", got.Verdict, risk.VerdictLikelyFake)
	}
}

func equalPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func derefOrNil(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func equalReasons(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
