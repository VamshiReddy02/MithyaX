package session

import (
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
)

// TemporalAnalyzer computes a temporal analysis from a sequence of
// frames. temporal.Analyzer implements it.
//
// Unlike VideoAnalyzer/AudioAnalyzer, this takes no context and returns
// no error: temporal analysis runs locally against frame metadata
// already in hand rather than calling out to an external service, so
// there's nothing to time out or fail to reach.
type TemporalAnalyzer interface {
	Analyze(frames []temporal.Frame) *temporal.TemporalResult
}

// TemporalResult is the temporal side of a combined analysis session —
// a session-local copy of temporal.TemporalResult, the same way
// VideoResult/AudioResult copy detector.Result/audio.Result rather than
// embedding them directly.
type TemporalResult struct {
	Score           float64  `json:"score"`
	FramesAnalyzed  int      `json:"frames_analyzed"`
	FaceConsistency float64  `json:"face_consistency"`
	ScoreVariance   float64  `json:"score_variance"`
	Reasons         []string `json:"reasons"`
}

// framesForTemporal decides which frames feed the temporal branch: an
// explicit req.Frames always wins (useful for exercising temporal
// without a real video call), otherwise it falls back to whatever
// per-frame metadata the video branch itself returned. video.err being
// set, or video never having been requested, both leave frameMetadata
// nil, which framesFromMetadata turns into nil frames — the same "not
// requested" state runTemporal already treats an empty slice as.
func framesForTemporal(req AnalyzeRequest, video videoOutcome) []temporal.Frame {
	if len(req.Frames) > 0 {
		return req.Frames
	}
	return framesFromMetadata(video.frameMetadata)
}

// framesFromMetadata converts the video detector's frame metadata into
// the flat []temporal.Frame the temporal analyzer expects. The two
// types already share the same shape (see detector.FrameMetadata), so
// this is a straightforward field-by-field copy rather than a real
// transformation.
func framesFromMetadata(metadata []detector.FrameMetadata) []temporal.Frame {
	if len(metadata) == 0 {
		return nil
	}

	frames := make([]temporal.Frame, len(metadata))
	for i, m := range metadata {
		frames[i] = temporal.Frame{
			Timestamp:    m.Timestamp,
			FakeScore:    m.FakeScore,
			FaceDetected: m.FaceDetected,
			FaceX:        m.FaceX,
			FaceY:        m.FaceY,
			FaceWidth:    m.FaceWidth,
			FaceHeight:   m.FaceHeight,
		}
	}
	return frames
}

// runTemporal analyzes frames and adapts the result into the session's
// own TemporalResult. It returns nil when frames is empty — temporal
// wasn't requested, exactly like an empty VideoURL or AudioData means
// video/audio weren't requested — and there's no failure case to report
// alongside it, since temporal.Analyzer.Analyze never errors.
func (s *Service) runTemporal(frames []temporal.Frame) *TemporalResult {
	if len(frames) == 0 {
		return nil
	}

	result := s.temporalAnalyzer.Analyze(frames)
	if result == nil {
		return nil
	}

	return &TemporalResult{
		Score:           result.Score,
		FramesAnalyzed:  result.FramesAnalyzed,
		FaceConsistency: result.FaceConsistency,
		ScoreVariance:   result.ScoreVariance,
		Reasons:         result.Reasons,
	}
}
