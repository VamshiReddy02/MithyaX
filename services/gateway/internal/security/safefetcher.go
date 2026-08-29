package security

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

// FetchErrorKind classifies why SafeFetcher.Fetch failed, so a caller
// (a worker's Handler — see internal/analysisworker) can decide
// whether retrying makes sense the same way it already does for
// detector.Error/audio.Error: a bad URL will fail identically on
// retry, a 5xx or a network blip might not.
type FetchErrorKind int

const (
	FetchErrorUnknown FetchErrorKind = iota
	// FetchErrorBlocked means rawURL, or a redirect target it led to,
	// failed SSRF validation. Never worth retrying — the same URL
	// validates the same way every time (modulo genuine DNS changes,
	// which is exactly why validation runs again on every attempt, not
	// why a single attempt should retry itself).
	FetchErrorBlocked
	// FetchErrorTooManyRedirects means the redirect chain exceeded
	// Config.MaxRedirects.
	FetchErrorTooManyRedirects
	// FetchErrorResponseTooLarge means the response body (or a
	// Content-Length that already announced it) exceeded
	// Config.MaxBytes.
	FetchErrorResponseTooLarge
	// FetchErrorUnacceptableStatus means the final (non-redirect) response
	// carried a status Fetch doesn't treat as success. StatusCode on
	// FetchError distinguishes a 4xx (permanent — retrying the same
	// request won't help) from a 5xx (likely transient).
	FetchErrorUnacceptableStatus
	// FetchErrorUnacceptableContentType means the response's
	// Content-Type didn't match Config.AllowedContentTypes.
	FetchErrorUnacceptableContentType
	// FetchErrorTimeout means the request (or the overall fetch,
	// across redirects) exceeded its deadline — either the caller's
	// context or Config.RequestTimeout.
	FetchErrorTimeout
	// FetchErrorNetwork covers everything else that can go wrong
	// actually talking to a server that already passed SSRF validation
	// (connection refused, connection reset, malformed response, ...).
	FetchErrorNetwork
)

// FetchError is returned by SafeFetcher.Fetch on any failure.
type FetchError struct {
	Kind FetchErrorKind
	// StatusCode is set only for FetchErrorUnacceptableStatus.
	StatusCode int
	Message    string
	// Err, if non-nil, is the underlying error FetchError wraps —
	// exposed via Unwrap so errors.Is/As still reach it (e.g. an
	// ErrPrivateAddress from the validator, for FetchErrorBlocked).
	Err error
}

func (e *FetchError) Error() string { return e.Message }
func (e *FetchError) Unwrap() error { return e.Err }

// IsPermanent reports whether retrying the exact same fetch is
// pointless — the same shape of check internal/analysisworker's
// Handler implementations already do for detector.Error/audio.Error.
func (e *FetchError) IsPermanent() bool {
	switch e.Kind {
	case FetchErrorBlocked, FetchErrorTooManyRedirects, FetchErrorResponseTooLarge, FetchErrorUnacceptableContentType:
		return true
	case FetchErrorUnacceptableStatus:
		return e.StatusCode >= 400 && e.StatusCode < 500
	default: // Timeout, Network, Unknown
		return false
	}
}

// Response is a fetched resource that has passed every SafeFetcher
// check — status, content type, and size — and is safe to hand
// straight to a detector.
type Response struct {
	Body        []byte
	ContentType string
	StatusCode  int
	// FinalURL is the URL actually fetched — the original rawURL, or
	// wherever a validated chain of redirects ultimately led.
	FinalURL string
}

// redirectStatuses are the response codes Fetch treats as "follow a
// new, independently-validated request" rather than a final answer.
var redirectStatuses = map[int]bool{
	http.StatusMovedPermanently:  true,
	http.StatusFound:             true,
	http.StatusSeeOther:          true,
	http.StatusTemporaryRedirect: true,
	http.StatusPermanentRedirect: true,
}

// FetchOptions configures one Fetch call — the limits that differ by
// what's being fetched (7.7.5: "Video: 100MB, Audio: 25MB" are
// per-call choices, not fetcher-wide ones).
type FetchOptions struct {
	// MaxBytes bounds the response body — checked against
	// Content-Length up front when present (fast-fail without
	// downloading), and enforced again against the actual bytes read
	// regardless, since a server can omit or lie about Content-Length.
	MaxBytes int64
	// AllowedContentTypes, if non-empty, restricts the response's
	// Content-Type (its MIME type, ignoring parameters like charset) to
	// one of these exact values or, if an entry ends in "/", anything
	// under that prefix (e.g. "video/" matches "video/mp4"). Empty
	// means any Content-Type is accepted — some legitimate media
	// origins answer with application/octet-stream.
	AllowedContentTypes []string
}

// Config configures a SafeFetcher.
type Config struct {
	// MaxRedirects bounds how many redirect hops Fetch will follow
	// before giving up — each hop is validated exactly like the
	// original URL (see Fetch's doc).
	MaxRedirects int
	// RequestTimeout bounds each individual HTTP attempt (connect
	// through reading the full body) — composes with, rather than
	// replaces, whatever deadline the caller's context already carries
	// (e.g. a worker's per-job timeout): whichever is tighter wins.
	RequestTimeout time.Duration
	// DialTimeout bounds establishing the TCP connection itself, a
	// tighter budget than RequestTimeout's whole-attempt one.
	DialTimeout time.Duration
}

const (
	defaultMaxRedirects   = 5
	defaultRequestTimeout = 30 * time.Second
	defaultDialTimeout    = 10 * time.Second
)

// SafeFetcher performs the actual outbound HTTP GET for a
// client-supplied URL (a video_url or audio_url — see
// internal/analysisworker), the way internal/security's package doc
// describes: validated, size-bounded, timeout-bounded, and safe
// against both a malicious redirect chain and DNS rebinding.
type SafeFetcher struct {
	validator *Validator
	cfg       Config
}

// NewSafeFetcher builds a SafeFetcher backed by validator (see
// NewValidator). Zero-value Config fields fall back to sane defaults.
func NewSafeFetcher(validator *Validator, cfg Config) *SafeFetcher {
	if cfg.MaxRedirects <= 0 {
		cfg.MaxRedirects = defaultMaxRedirects
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultDialTimeout
	}
	return &SafeFetcher{validator: validator, cfg: cfg}
}

// Fetch validates rawURL, requests it, and follows any redirect chain
// up to Config.MaxRedirects hops — independently re-validating (via
// the same Validator, including a fresh DNS lookup) every single
// redirect target before ever requesting it:
//
//	validate URL #1 -> GET -> 302 -> validate URL #2 -> GET -> 302 -> validate URL #3 -> ...
//
// A URL (or any hop in its redirect chain) that fails validation, a
// chain longer than MaxRedirects, a response over MaxBytes, an
// unacceptable status or Content-Type, or a request that outlives its
// deadline all fail with a *FetchError classifying which. Every error
// this returns unwraps (errors.As) to *FetchError.
//
// DNS rebinding: each hop resolves its host exactly once, via
// Validator.ResolveContext, and the actual TCP connection is dialed
// directly against that one already-validated IP (see dialerFor) — it
// is never handed back to the host string for the standard library's
// own dialer to resolve a second time, which is exactly the gap a
// rebinding attack needs (a name validated as public, then re-resolved
// moments later to something private, for the connection that
// actually matters).
func (f *SafeFetcher) Fetch(ctx context.Context, rawURL string, opts FetchOptions) (*Response, error) {
	currentURL := rawURL

	for hop := 0; ; hop++ {
		if hop > f.cfg.MaxRedirects {
			return nil, &FetchError{Kind: FetchErrorTooManyRedirects, Message: fmt.Sprintf("exceeded %d redirects", f.cfg.MaxRedirects)}
		}

		target, err := f.validator.ResolveContext(ctx, currentURL)
		if err != nil {
			return nil, &FetchError{Kind: FetchErrorBlocked, Message: fmt.Sprintf("blocked by SSRF validation: %v", err), Err: err}
		}

		resp, fetchErr := f.doOnce(ctx, target)
		if fetchErr != nil {
			return nil, fetchErr
		}

		if redirectStatuses[resp.StatusCode] {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return nil, &FetchError{Kind: FetchErrorNetwork, Message: fmt.Sprintf("redirect status %d with no Location header", resp.StatusCode)}
			}
			next, err := target.URL.Parse(location)
			if err != nil {
				return nil, &FetchError{Kind: FetchErrorNetwork, Message: fmt.Sprintf("invalid redirect Location %q: %v", location, err), Err: err}
			}
			currentURL = next.String()
			continue
		}

		return f.readResponse(resp, target.URL.String(), opts)
	}
}

// doOnce performs one HTTP GET against target's pinned IP, returning
// the raw *http.Response (redirect or terminal — the caller decides
// which) with its body still open, or a *FetchError. The caller is
// responsible for closing the body exactly once, however it's handled.
func (f *SafeFetcher) doOnce(ctx context.Context, target *ResolvedTarget) (*http.Response, *FetchError) {
	attemptCtx, cancel := context.WithTimeout(ctx, f.cfg.RequestTimeout)

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, target.URL.String(), nil)
	if err != nil {
		cancel()
		return nil, &FetchError{Kind: FetchErrorNetwork, Message: fmt.Sprintf("build request: %v", err), Err: err}
	}

	client := &http.Client{
		// Redirects are handled entirely by Fetch's own loop, one
		// independently-validated hop at a time — the client must never
		// chase one on its own.
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext: pinnedDialContext(target.IP, f.cfg.DialTimeout),
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || attemptCtx.Err() != nil {
			return nil, &FetchError{Kind: FetchErrorTimeout, Message: fmt.Sprintf("request timed out: %v", err), Err: err}
		}
		return nil, &FetchError{Kind: FetchErrorNetwork, Message: fmt.Sprintf("request failed: %v", err), Err: err}
	}

	// cancel is deliberately not called here: attemptCtx must stay live
	// until the body (still unread at this point) is fully consumed or
	// closed — see readResponse and the redirect-handling branch in
	// Fetch, both of which close the body exactly once, at which point
	// closeCancelBody releases it. Without this, reading the body after
	// this function returns would race a canceled context.
	resp.Body = &closeCancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// closeCancelBody wraps a response body so cancel (the per-attempt
// context's cancel func) always runs exactly once when the body is
// closed — whether that's readResponse finishing a successful read or
// the redirect branch discarding an intermediate hop's body.
type closeCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *closeCancelBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

// pinnedDialContext returns a DialContext that connects to ip
// regardless of what host the caller (http.Transport, driven by the
// request's own URL) asks to dial — see Fetch's doc for why pinning
// the already-validated IP, rather than letting this dial re-resolve
// the hostname itself, is what actually closes the DNS-rebinding gap.
// The port from the original address is preserved; TLS SNI/certificate
// validation is unaffected since Transport derives that from the
// request's URL, never from what DialContext actually connects to.
func pinnedDialContext(ip net.IP, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: timeout}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}

// readResponse validates status, Content-Type, and size on a terminal
// (non-redirect) response and, if everything checks out, reads its
// bounded body.
func (f *SafeFetcher) readResponse(resp *http.Response, finalURL string, opts FetchOptions) (*Response, error) {
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &FetchError{
			Kind:       FetchErrorUnacceptableStatus,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("unacceptable HTTP status %d", resp.StatusCode),
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if len(opts.AllowedContentTypes) > 0 && !contentTypeAllowed(contentType, opts.AllowedContentTypes) {
		return nil, &FetchError{
			Kind:    FetchErrorUnacceptableContentType,
			Message: fmt.Sprintf("unacceptable content type %q", contentType),
		}
	}

	if opts.MaxBytes > 0 && resp.ContentLength > opts.MaxBytes {
		return nil, &FetchError{
			Kind:    FetchErrorResponseTooLarge,
			Message: fmt.Sprintf("Content-Length %d exceeds limit of %d bytes", resp.ContentLength, opts.MaxBytes),
		}
	}

	body, err := readBounded(resp.Body, opts.MaxBytes)
	if err != nil {
		return nil, err
	}

	return &Response{
		Body:        body,
		ContentType: contentType,
		StatusCode:  resp.StatusCode,
		FinalURL:    finalURL,
	}, nil
}

// readBounded reads r fully, failing with FetchErrorResponseTooLarge
// if it ever exceeds maxBytes — the check that matters regardless of
// whatever (or however wrong) Content-Length claimed, since the actual
// byte count is the only thing this can't be lied to about. maxBytes
// <= 0 means unbounded.
func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, &FetchError{Kind: FetchErrorNetwork, Message: fmt.Sprintf("read response body: %v", err), Err: err}
		}
		return data, nil
	}

	limited := io.LimitReader(r, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		var kind FetchErrorKind = FetchErrorNetwork
		if errors.Is(err, context.DeadlineExceeded) {
			kind = FetchErrorTimeout
		}
		return nil, &FetchError{Kind: kind, Message: fmt.Sprintf("read response body: %v", err), Err: err}
	}
	if int64(len(data)) > maxBytes {
		return nil, &FetchError{Kind: FetchErrorResponseTooLarge, Message: fmt.Sprintf("response body exceeds %d bytes", maxBytes)}
	}
	return data, nil
}

// contentTypeAllowed reports whether contentType's MIME type (ignoring
// parameters like charset) matches one of allowed — an exact value, or
// a "prefix/" entry matching anything under that prefix.
func contentTypeAllowed(contentType string, allowed []string) bool {
	mimeType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mimeType = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	}
	for _, want := range allowed {
		if strings.HasSuffix(want, "/") {
			if strings.HasPrefix(mimeType, want) {
				return true
			}
			continue
		}
		if strings.EqualFold(mimeType, want) {
			return true
		}
	}
	return false
}
