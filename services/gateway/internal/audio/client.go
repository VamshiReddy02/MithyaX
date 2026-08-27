// Package audio implements a client for the Python audio-detector service.
package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Kind classifies why a call to the audio-detector service failed, so
// callers can map it to an appropriate response.
type Kind int

const (
	// KindUnknown covers failures that don't fit another category.
	KindUnknown Kind = iota
	// KindTimeout means the request exceeded its deadline.
	KindTimeout
	// KindUnavailable means the audio-detector service could not be reached.
	KindUnavailable
	// KindInvalidAudio means the audio-detector rejected the input itself
	// (corrupt file, unsupported format, no audio frames, ...).
	KindInvalidAudio
	// KindDetectorError means the audio-detector reached the request but
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

// Result is the voice-detection analysis produced by the audio-detector
// for one uploaded audio file.
type Result struct {
	DurationSeconds float64 `json:"duration_seconds"`
	SampleRate      int     `json:"sample_rate"`
	Channels        int     `json:"channels"`
	Chunks          int     `json:"chunks"`
	Status          string  `json:"status"`
	FakeScore       float64 `json:"fake_score"`
	Verdict         string  `json:"verdict"`
}

// Client calls the Python audio-detector service over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a Client that talks to the audio-detector at baseURL,
// bounding every request to timeout.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

type errorBody struct {
	Detail string `json:"detail"`
	Error  string `json:"error"`
}

// Analyze uploads an audio file to the audio-detector and returns its
// voice-detection result. filename is passed through in the multipart
// upload (the audio-detector doesn't currently use it, but a future
// format-dispatch-by-extension step will).
func (c *Client) Analyze(ctx context.Context, filename string, data []byte) (*Result, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("audio", filename)
	if err != nil {
		return nil, &Error{Kind: KindUnknown, Message: err.Error()}
	}
	if _, err := part.Write(data); err != nil {
		return nil, &Error{Kind: KindUnknown, Message: err.Error()}
	}
	if err := writer.Close(); err != nil {
		return nil, &Error{Kind: KindUnknown, Message: err.Error()}
	}

	respBody, err := c.post(ctx, "/analyze-audio", writer.FormDataContentType(), body.Bytes())
	if err != nil {
		return nil, err
	}

	var result Result
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, &Error{Kind: KindDetectorError, Message: fmt.Sprintf("invalid audio-detector response: %v", err)}
	}
	return &result, nil
}

// post sends body to path on the audio-detector and returns the raw
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
			return nil, &Error{Kind: KindTimeout, Message: "audio-detector request timed out"}
		}
		return nil, &Error{Kind: KindUnavailable, Message: fmt.Sprintf("audio-detector unreachable: %v", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &Error{Kind: KindDetectorError, Message: fmt.Sprintf("failed to read audio-detector response: %v", err)}
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return respBody, nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, &Error{Kind: KindInvalidAudio, Message: extractMessage(respBody, resp.StatusCode)}

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
	return fmt.Sprintf("audio-detector returned status %d", status)
}
