package session

// Status describes how an analysis session concluded.
type Status string

const (
	// StatusCompleted means every requested analysis (video and/or
	// audio) succeeded.
	StatusCompleted Status = "completed"
	// StatusPartial means at least one requested analysis succeeded and
	// at least one failed.
	StatusPartial Status = "partial"
	// StatusFailed means every requested analysis failed.
	StatusFailed Status = "failed"
)

// VideoResult is the video side of a combined analysis session.
type VideoResult struct {
	FakeScore float64 `json:"fake_score"`
	Verdict   string  `json:"verdict"`
}

// AudioResult is the audio side of a combined analysis session.
type AudioResult struct {
	FakeScore float64 `json:"fake_score"`
	Verdict   string  `json:"verdict"`
}

// AnalysisSession is the combined result of running video and audio
// analysis concurrently against the same submission. Video/Audio are
// nil if that branch wasn't requested or didn't succeed — check
// VideoError/AudioError to tell those two cases apart.
type AnalysisSession struct {
	ID         string       `json:"id"`
	Status     Status       `json:"status"`
	Video      *VideoResult `json:"video,omitempty"`
	Audio      *AudioResult `json:"audio,omitempty"`
	VideoError string       `json:"video_error,omitempty"`
	AudioError string       `json:"audio_error,omitempty"`
}
