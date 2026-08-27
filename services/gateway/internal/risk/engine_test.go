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

// weightedAvg computes the same weighted-average formula
// weightedScore() does, against risk.DefaultWeights, so test
// expectations stay correct if the weights are ever retuned instead of
// silently going stale.
func weightedAvg(videoScore, audioScore, temporalScore float64, hasVideo, hasAudio, hasTemporal bool) float64 {
	w := risk.DefaultWeights
	var sum, total float64
	if hasVideo {
		sum += videoScore * w.Video
		total += w.Video
	}
	if hasAudio {
		sum += audioScore * w.Audio
		total += w.Audio
	}
	if hasTemporal {
		sum += temporalScore * w.Temporal
		total += w.Temporal
	}
	return sum / total
}

func TestEngine_AssessSignals(t *testing.T) {
	tests := []struct {
		name         string
		sig          risk.Signals
		wantScore    float64
		wantVerdict  risk.Verdict
		wantVideo    *float64
		wantAudio    *float64
		wantTemporal *float64
		wantReasons  []string
	}{
		{
			name:         "video + audio + temporal, weighted average",
			sig:          risk.Signals{Video: 0.08, VideoOK: true, Audio: 0.91, AudioOK: true, Temporal: 0.3, TemporalOK: true},
			wantScore:    weightedAvg(0.08, 0.91, 0.3, true, true, true),
			wantVerdict:  risk.VerdictSuspicious,
			wantVideo:    float64Ptr(0.08),
			wantAudio:    float64Ptr(0.91),
			wantTemporal: float64Ptr(0.3),
			wantReasons:  []string{"Audio signal indicates likely synthetic speech"},
		},
		{
			name:         "video + audio + temporal, all high",
			sig:          risk.Signals{Video: 0.8, VideoOK: true, Audio: 0.95, AudioOK: true, Temporal: 0.7, TemporalOK: true},
			wantScore:    weightedAvg(0.8, 0.95, 0.7, true, true, true),
			wantVerdict:  risk.VerdictLikelyFake,
			wantVideo:    float64Ptr(0.8),
			wantAudio:    float64Ptr(0.95),
			wantTemporal: float64Ptr(0.7),
			wantReasons: []string{
				"Audio signal indicates likely synthetic speech",
				"Video signal indicates likely synthetic or manipulated content",
				"Temporal signal indicates suspicious frame-to-frame behavior",
			},
		},
		{
			name:         "video + audio, both low, likely authentic",
			sig:          risk.Signals{Video: 0.05, VideoOK: true, Audio: 0.1, AudioOK: true},
			wantScore:    weightedAvg(0.05, 0.1, 0, true, true, false),
			wantVerdict:  risk.VerdictLikelyAuthentic,
			wantVideo:    float64Ptr(0.05),
			wantAudio:    float64Ptr(0.1),
			wantTemporal: nil,
			wantReasons:  nil,
		},
		{
			name:         "video + temporal, audio missing",
			sig:          risk.Signals{Video: 0.6, VideoOK: true, Temporal: 0.7, TemporalOK: true, AudioError: "audio detector unreachable"},
			wantScore:    weightedAvg(0.6, 0, 0.7, true, false, true),
			wantVerdict:  risk.VerdictLikelyFake,
			wantVideo:    float64Ptr(0.6),
			wantAudio:    nil,
			wantTemporal: float64Ptr(0.7),
			wantReasons: []string{
				"Video signal indicates likely synthetic or manipulated content",
				"Temporal signal indicates suspicious frame-to-frame behavior",
				"Audio analysis unavailable: audio detector unreachable",
			},
		},
		{
			name:         "audio + temporal, video missing",
			sig:          risk.Signals{Audio: 0.2, AudioOK: true, Temporal: 0.1, TemporalOK: true, VideoError: "video detector unreachable"},
			wantScore:    weightedAvg(0, 0.2, 0.1, false, true, true),
			wantVerdict:  risk.VerdictLikelyAuthentic,
			wantVideo:    nil,
			wantAudio:    float64Ptr(0.2),
			wantTemporal: float64Ptr(0.1),
			wantReasons:  []string{"Video analysis unavailable: video detector unreachable"},
		},
		{
			name:         "temporal only",
			sig:          risk.Signals{Temporal: 0.65, TemporalOK: true, VideoError: "video detector unreachable", AudioError: "audio detector unreachable"},
			wantScore:    0.65,
			wantVerdict:  risk.VerdictLikelyFake,
			wantVideo:    nil,
			wantAudio:    nil,
			wantTemporal: float64Ptr(0.65),
			wantReasons: []string{
				"Temporal signal indicates suspicious frame-to-frame behavior",
				"Video analysis unavailable: video detector unreachable",
				"Audio analysis unavailable: audio detector unreachable",
			},
		},
		{
			name:         "temporal missing, video + audio only",
			sig:          risk.Signals{Video: 0.7, VideoOK: true, Audio: 0.2, AudioOK: true},
			wantScore:    weightedAvg(0.7, 0.2, 0, true, true, false),
			wantVerdict:  risk.VerdictSuspicious,
			wantVideo:    float64Ptr(0.7),
			wantAudio:    float64Ptr(0.2),
			wantTemporal: nil,
			wantReasons:  []string{"Video signal indicates likely synthetic or manipulated content"},
		},
		{
			name:         "all three missing",
			sig:          risk.Signals{VideoError: "video boom", AudioError: "audio boom", TemporalError: "temporal analysis failed"},
			wantScore:    0,
			wantVerdict:  risk.VerdictUnknown,
			wantVideo:    nil,
			wantAudio:    nil,
			wantTemporal: nil,
			wantReasons: []string{
				"Video analysis unavailable: video boom",
				"Audio analysis unavailable: audio boom",
				"Temporal analysis unavailable: temporal analysis failed",
			},
		},
		{
			name:         "all three missing, none requested",
			sig:          risk.Signals{},
			wantScore:    0,
			wantVerdict:  risk.VerdictUnknown,
			wantVideo:    nil,
			wantAudio:    nil,
			wantTemporal: nil,
			wantReasons:  []string{"No analysis signals were available"},
		},
		{
			name:         "all three at the suspicious threshold is suspicious",
			sig:          risk.Signals{Video: 0.3, VideoOK: true, Audio: 0.3, AudioOK: true, Temporal: 0.3, TemporalOK: true},
			wantScore:    0.3,
			wantVerdict:  risk.VerdictSuspicious,
			wantVideo:    float64Ptr(0.3),
			wantAudio:    float64Ptr(0.3),
			wantTemporal: float64Ptr(0.3),
			wantReasons:  nil,
		},
		{
			name:         "all three at the likely-fake threshold is likely fake",
			sig:          risk.Signals{Video: 0.6, VideoOK: true, Audio: 0.6, AudioOK: true, Temporal: 0.6, TemporalOK: true},
			wantScore:    0.6,
			wantVerdict:  risk.VerdictLikelyFake,
			wantVideo:    float64Ptr(0.6),
			wantAudio:    float64Ptr(0.6),
			wantTemporal: float64Ptr(0.6),
			wantReasons: []string{
				"Audio signal indicates likely synthetic speech",
				"Video signal indicates likely synthetic or manipulated content",
				"Temporal signal indicates suspicious frame-to-frame behavior",
			},
		},
		{
			name:         "temporal alone at the likely-fake threshold is likely fake",
			sig:          risk.Signals{Temporal: 0.6, TemporalOK: true},
			wantScore:    0.6,
			wantVerdict:  risk.VerdictLikelyFake,
			wantVideo:    nil,
			wantAudio:    nil,
			wantTemporal: float64Ptr(0.6),
			wantReasons:  []string{"Temporal signal indicates suspicious frame-to-frame behavior"},
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
			if !equalPtr(got.Signals.Temporal, tt.wantTemporal) {
				t.Errorf("Signals.Temporal = %v, want %v", derefOrNil(got.Signals.Temporal), derefOrNil(tt.wantTemporal))
			}
			if !equalReasons(got.Reasons, tt.wantReasons) {
				t.Errorf("Reasons = %v, want %v", got.Reasons, tt.wantReasons)
			}
		})
	}
}

// TestEngine_Assess proves the AnalysisSession → Signals adapter wires
// FakeScore/nil/error through correctly, on top of the AssessSignals
// coverage above. FromSession doesn't populate Temporal (AnalysisSession
// has no temporal result yet), so this only exercises video + audio.
func TestEngine_Assess(t *testing.T) {
	s := &session.AnalysisSession{
		ID:     "session-1",
		Status: session.StatusCompleted,
		Video:  &session.VideoResult{FakeScore: 0.08, Verdict: "real"},
		Audio:  &session.AudioResult{FakeScore: 0.91, Verdict: "fake"},
	}

	got := risk.NewEngine().Assess(s)

	wantScore := weightedAvg(0.08, 0.91, 0, true, true, false)
	if math.Abs(got.RiskScore-wantScore) > floatEpsilon {
		t.Errorf("RiskScore = %v, want %v", got.RiskScore, wantScore)
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
	if got.Signals.Temporal != nil {
		t.Errorf("Signals.Temporal = %v, want nil (AnalysisSession carries no temporal result yet)", *got.Signals.Temporal)
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
