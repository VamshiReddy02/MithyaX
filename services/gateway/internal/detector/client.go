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
	// KindInvalidVideo means the video-detector rejected the video itself
	// (unreachable URL, corrupt file, unsupported format, ...).
	KindInvalidVideo
	// KindDetectorError means the video-detector reached the request but
	// failed to process it (5xx or a malformed response).
	KindDetectorError
)

// Error is returned by Client.Analyze when the call does not succeed.
type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

// Result is the analysis produced by the video-detector's Xception model.
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
// inference result.
func (c *Client) Analyze(ctx context.Context, videoURL string) (*Result, error) {
	body, err := json.Marshal(analyzeRequest{VideoURL: videoURL})
	if err != nil {
		return nil, &Error{Kind: KindUnknown, Message: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/analyze", bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindUnknown, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

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
		var result Result
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, &Error{Kind: KindDetectorError, Message: fmt.Sprintf("invalid video-detector response: %v", err)}
		}
		return &result, nil

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
