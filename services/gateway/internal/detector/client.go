// Package detector implements a client for the Python video-detector service.
package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Kind classifies why a call to the video-detector service failed, so
// callers can map it to an appropriate response.
type Kind int

const (
	// KindUnknown covers failures that don't fit another category.
	KindUnknown Kind = iota
	// KindTimeout means the request exceeded its deadline.
	KindTimeout
	// KindUnavailable means the video-detector service could not be reached.
	KindUnavailable
	// KindInvalidVideo means the video-detector rejected the input itself
	// (unreachable URL, corrupt file/frame, unsupported format, ...).
	KindInvalidVideo
	// KindDetectorError means the video-detector reached the request but
	// failed to process it (5xx or a malformed response).
	KindDetectorError
)

// Error is returned by Client methods when the call does not succeed.
type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

// Result is the analysis produced by the video-detector's Xception model
// over a whole video.
type Result struct {
	Video           string  `json:"video"`
	Frames          int     `json:"frames"`
	FacesDetected   int     `json:"faces_detected"`
	FakeScore       float64 `json:"fake_score"`
	FakeMean        float64 `json:"fake_mean"`
	FakeMedian      float64 `json:"fake_median"`
	FakeP75         float64 `json:"fake_p75"`
	FakeP90         float64 `json:"fake_p90"`
	FakeMax         float64 `json:"fake_max"`
	EmbeddingFrames int     `json:"embedding_frames"`
	Verdict         string  `json:"verdict"`
	// FrameMetadata is a downsampled, evenly-spaced subset of the
	// frames the video-detector actually processed (see the Python
	// service's sample_frame_metadata) — not every frame in the video.
	FrameMetadata []FrameMetadata `json:"frame_metadata"`
}

// FrameMetadata is one analyzed frame's metadata, as returned by the
// video-detector's /analyze endpoint. Its fields mirror
// internal/temporal.Frame's flat shape one for one, even though the
// wire format nests the face box under a "face" object (see
// UnmarshalJSON) — that nesting is a JSON presentation detail, not
// something callers of this package need to know about. FaceX/Y/Width/
// Height are 0 when FaceDetected is false: there's no box to report.
type FrameMetadata struct {
	Timestamp    float64
	FakeScore    float64
	FaceDetected bool
	FaceX        float64
	FaceY        float64
	FaceWidth    float64
	FaceHeight   float64
}

// frameMetadataWire is FrameMetadata's actual wire shape: the video-
// detector nests the bounding box under a "face" object (null when no
// face was found) rather than flattening it.
type frameMetadataWire struct {
	Timestamp    float64 `json:"timestamp"`
	FakeScore    float64 `json:"fake_score"`
	FaceDetected bool    `json:"face_detected"`
	Face         *struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"face"`
}

// UnmarshalJSON flattens the wire format's nested "face" object into
// FrameMetadata's flat FaceX/Y/Width/Height fields.
func (f *FrameMetadata) UnmarshalJSON(data []byte) error {
	var wire frameMetadataWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	*f = FrameMetadata{
		Timestamp:    wire.Timestamp,
		FakeScore:    wire.FakeScore,
		FaceDetected: wire.FaceDetected,
	}

	if wire.Face != nil {
		f.FaceX = wire.Face.X
		f.FaceY = wire.Face.Y
		f.FaceWidth = wire.Face.Width
		f.FaceHeight = wire.Face.Height
	}

	return nil
}

// FrameResult is the analysis produced by the video-detector's Xception
// model over a single still frame.
type FrameResult struct {
	FaceDetected    bool    `json:"face_detected"`
	FakeProbability float64 `json:"fake_probability"`
	Verdict         string  `json:"verdict"`
}

// Client calls the Python video-detector service over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a Client that talks to the video-detector at baseURL,
// bounding every request to timeout.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

type analyzeRequest struct {
	VideoURL string `json:"video_url"`
}

type errorBody struct {
	Detail string `json:"detail"`
	Error  string `json:"error"`
}

// Analyze sends videoURL to the video-detector and returns its Xception
// inference result over the whole video.
func (c *Client) Analyze(ctx context.Context, videoURL string) (*Result, error) {
	body, err := json.Marshal(analyzeRequest{VideoURL: videoURL})
	if err != nil {
		return nil, &Error{Kind: KindUnknown, Message: err.Error()}
	}

	respBody, err := c.post(ctx, "/analyze", "application/json", body)
	if err != nil {
		return nil, err
	}

	var result Result
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, &Error{Kind: KindDetectorError, Message: fmt.Sprintf("invalid video-detector response: %v", err)}
	}
	return &result, nil
}

// AnalyzeFrame sends a single JPEG frame to the video-detector and returns
// its Xception inference result for that frame alone.
func (c *Client) AnalyzeFrame(ctx context.Context, jpeg []byte) (*FrameResult, error) {
	respBody, err := c.post(ctx, "/analyze-frame", "image/jpeg", jpeg)
	if err != nil {
		return nil, err
	}

	var result FrameResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, &Error{Kind: KindDetectorError, Message: fmt.Sprintf("invalid video-detector response: %v", err)}
	}
	return &result, nil
}

// post sends body to path on the video-detector and returns the raw
// response bytes on success, classifying any failure into an *Error.
func (c *Client) post(ctx context.Context, path, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindUnknown, Message: err.Error()}
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &Error{Kind: KindTimeout, Message: "video-detector request timed out"}
		}
		return nil, &Error{Kind: KindUnavailable, Message: fmt.Sprintf("video-detector unreachable: %v", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &Error{Kind: KindDetectorError, Message: fmt.Sprintf("failed to read video-detector response: %v", err)}
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return respBody, nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, &Error{Kind: KindInvalidVideo, Message: extractMessage(respBody, resp.StatusCode)}

	default:
		return nil, &Error{Kind: KindDetectorError, Message: extractMessage(respBody, resp.StatusCode)}
	}
}

func extractMessage(body []byte, status int) string {
	var e errorBody
	if err := json.Unmarshal(body, &e); err == nil {
		if e.Detail != "" {
			return e.Detail
		}
		if e.Error != "" {
			return e.Error
		}
	}
	return fmt.Sprintf("video-detector returned status %d", status)
}
