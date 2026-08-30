package realtime

// Type identifies what kind of message this is, in either direction of
// a live session's WebSocket.
type Type string

const (
	// TypeFrame is sent by the browser, carrying one JPEG frame to
	// analyze (base64 in Data) — the same capture-a-frame-from-canvas
	// step web/call.js already does for /api/v1/analyze-frame, now fed
	// into a persistent session instead of a one-shot HTTP POST.
	TypeFrame Type = "frame"
	// TypeAudioChunk is sent by the browser, carrying one chunk of raw
	// audio to analyze (base64 in Data).
	TypeAudioChunk Type = "audio_chunk"
	// TypeEndSession is sent by the browser to end the session while
	// still connected, rather than just closing the socket.
	TypeEndSession Type = "end_session"

	// TypeSessionStarted is sent by the gateway once the WebSocket
	// connects, confirming the session is live.
	TypeSessionStarted Type = "session_started"
	// TypeVideoResult is sent by the gateway after analyzing one frame.
	TypeVideoResult Type = "video_result"
	// TypeAudioResult is sent by the gateway after analyzing one audio
	// chunk.
	TypeAudioResult Type = "audio_result"
	// TypeTemporalResult is sent by the gateway once enough frames have
	// accumulated for the temporal analyzer to say something about
	// frame-to-frame behavior (see temporal.Analyzer.Analyze).
	TypeTemporalResult Type = "temporal_result"
	// TypeRiskUpdate is sent by the gateway after every video_result,
	// audio_result, and temporal_result, reflecting every signal
	// gathered so far — the same risk.Engine used by the uploaded-video
	// pipeline, fed incrementally instead of all at once.
	TypeRiskUpdate Type = "risk_update"
	// TypeSessionEnded is sent by the gateway in response to an
	// explicit end_session.
	TypeSessionEnded Type = "session_ended"
	// TypeError is sent by the gateway when a frame or audio chunk
	// couldn't be analyzed (e.g. the video-detector was unreachable) —
	// the session stays open; only that one message failed.
	TypeError Type = "error"
)

// InMessage is a message sent by the browser.
type InMessage struct {
	Type Type `json:"type"`
	// Data is a base64-encoded binary payload: a JPEG frame for
	// TypeFrame, raw audio bytes for TypeAudioChunk.
	Data string `json:"data,omitempty"`
	// Filename accompanies TypeAudioChunk — passed through to the
	// audio-detector the same way the upload flow's AudioFilename is.
	Filename string `json:"filename,omitempty"`
}

// OutMessage is a message sent by the gateway. Only the fields relevant
// to Type are populated. Numeric fields are pointers so a genuine zero
// value (a 0.0 risk score, 0 frames_analyzed) can be told apart from
// "not applicable to this message type".
type OutMessage struct {
	Type Type `json:"type"`

	ID     string `json:"id,omitempty"`
	Status string `json:"status,omitempty"`

	// video_result / audio_result
	FakeScore    *float64 `json:"fake_score,omitempty"`
	FaceDetected *bool    `json:"face_detected,omitempty"`
	Verdict      string   `json:"verdict,omitempty"`

	// temporal_result
	Score           *float64 `json:"score,omitempty"`
	FramesAnalyzed  *int     `json:"frames_analyzed,omitempty"`
	FaceConsistency *float64 `json:"face_consistency,omitempty"`
	ScoreVariance   *float64 `json:"score_variance,omitempty"`

	// risk_update
	RiskScore *float64 `json:"risk_score,omitempty"`

	// temporal_result and risk_update both carry reasons.
	Reasons []string `json:"reasons,omitempty"`

	// error
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ErrCodeOverloaded is the error Code sent when a session's video or
// audio queue genuinely can't accept more work right now — see
// Session.SubmitFrame/SubmitAudioChunk.
const ErrCodeOverloaded = "overloaded"

// ErrCodeSessionLimitExceeded is the error Code sent immediately
// before a session is ended for exceeding one of its 7.7.6 resource
// caps (maximum frames or audio chunks over its lifetime) — distinct
// from ErrCodeOverloaded because this is permanent: the session is
// over, not just momentarily busy. See Session.countLimitExceeded.
const ErrCodeSessionLimitExceeded = "session_limit_exceeded"
