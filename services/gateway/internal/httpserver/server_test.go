package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/httpserver"
)

// testAuthToken/testAdminAuthToken are the GATEWAY_AUTH_TOKEN/
// GATEWAY_ADMIN_AUTH_TOKEN this test configures the server with
// (7.7.1/7.7.2) — every /api/v1/* request below must carry one of
// them, since that whole group now requires authentication; /health
// does not (see its own request below).
const (
	testAuthToken      = "test-server-auth-token"
	testAdminAuthToken = "test-server-admin-auth-token"
)

// doAuthed sends an authenticated request to url — a plain http.Get/
// http.Post can't set headers, and every /api/v1/* route needs the
// bearer token now that internal/auth's middleware guards the group.
func doAuthed(t *testing.T, method, url, contentType string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("build request for %s %s: %v", method, url, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func TestServer_Routes(t *testing.T) {
	fakeDetector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(detector.Result{
			Video:     "video.mp4",
			FakeScore: 0.9,
			Verdict:   "fake",
		})
	}))
	defer fakeDetector.Close()

	fakeAudioDetector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(audio.Result{
			DurationSeconds: 4.6,
			SampleRate:      16000,
			Channels:        1,
			Chunks:          2,
			Status:          "processed",
			FakeScore:       0.97,
			Verdict:         "fake",
		})
	}))
	defer fakeAudioDetector.Close()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := httpserver.New(config.Config{
		Port:                 "0",
		Environment:          "test",
		DetectorBaseURL:      fakeDetector.URL,
		DetectorTimeout:      5 * time.Second,
		AudioDetectorBaseURL: fakeAudioDetector.URL,
		AudioDetectorTimeout: 5 * time.Second,
		WorkerCount:          2,
		WorkerQueueSize:      8,
		RedisURL:             "redis://" + mr.Addr(),
		JobTTL:               time.Hour,
		AuthToken:            testAuthToken,
		AdminAuthToken:       testAdminAuthToken,
	}, logger)
	if err != nil {
		t.Fatalf("httpserver.New() error = %v", err)
	}
	// Deferred in reverse of desired shutdown order: Redis must stay open
	// until both pools have actually stopped touching it.
	defer srv.Redis.Close()
	defer srv.StopWorkers()
	defer srv.Pool.Shutdown(context.Background())

	ts := httptest.NewServer(srv.HTTP.Handler)
	defer ts.Close()

	// /health now actually pings Postgres and Redis (see
	// handlers.NewHealth) rather than always answering 200 — this test
	// doesn't configure a real DatabaseURL (that'd depend on whatever
	// Postgres happens to be reachable on the machine running the
	// tests), so it only checks the route is wired to the right handler
	// shape. The health-check logic itself — healthy/unhealthy for each
	// dependency independently — is covered deterministically with fakes
	// in internal/handlers/health_test.go.
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /health status = %d, want %d or %d", resp.StatusCode, http.StatusOK, http.StatusServiceUnavailable)
	}
	var health handlers.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode GET /health response: %v", err)
	}
	if _, ok := health.Checks["redis"]; !ok {
		t.Error(`GET /health response missing "redis" check`)
	}
	if health.Checks["redis"] != "healthy" {
		t.Errorf(`Checks["redis"] = %q, want "healthy" (a real miniredis is configured for this test)`, health.Checks["redis"])
	}

	resp2 := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analyze", "application/json", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /api/v1/analyze with no body status = %d, want %d", resp2.StatusCode, http.StatusBadRequest)
	}

	resp3 := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analyze", "application/json",
		strings.NewReader(`{"video_url":"https://example.com/video.mp4"}`),
	)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusAccepted {
		t.Errorf("POST /api/v1/analyze status = %d, want %d", resp3.StatusCode, http.StatusAccepted)
	}

	var queued handlers.AnalyzeResponse
	if err := json.NewDecoder(resp3.Body).Decode(&queued); err != nil {
		t.Fatalf("decode POST /api/v1/analyze response: %v", err)
	}
	if queued.ID == "" {
		t.Fatal("queued.ID is empty")
	}

	// Poll the status endpoint until the worker pool actually completes
	// the job — proves the full enqueue -> worker -> detector -> store
	// -> GET path works end to end, not just that POST returns 202.
	deadline := time.Now().Add(2 * time.Second)
	var status handlers.JobStatusResponse
	for time.Now().Before(deadline) {
		resp4 := doAuthed(t, http.MethodGet, ts.URL+"/api/v1/analyze/"+queued.ID, "", nil)
		if err := json.NewDecoder(resp4.Body).Decode(&status); err != nil {
			resp4.Body.Close()
			t.Fatalf("decode GET /api/v1/analyze/%s response: %v", queued.ID, err)
		}
		resp4.Body.Close()

		if status.Status == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if status.Status != "completed" {
		t.Fatalf("job status = %q, want %q (last seen: %+v)", status.Status, "completed", status)
	}
	if status.Result == nil || status.Result.Verdict != "fake" {
		t.Errorf("Result = %+v, want Verdict=fake", status.Result)
	}

	resp5 := doAuthed(t, http.MethodGet, ts.URL+"/api/v1/analyze/does-not-exist", "", nil)
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/v1/analyze/does-not-exist status = %d, want %d", resp5.StatusCode, http.StatusNotFound)
	}

	// POST /api/v1/analyze-audio: real multipart upload through the real
	// server, proving the gateway -> Python audio-detector relay works
	// end to end, not just that the handler/client units work in isolation.
	audioBody := &bytes.Buffer{}
	audioWriter := multipart.NewWriter(audioBody)
	part, err := audioWriter.CreateFormFile("audio", "clip.wav")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("fake-wav-bytes")); err != nil {
		t.Fatalf("part.Write: %v", err)
	}
	if err := audioWriter.Close(); err != nil {
		t.Fatalf("audioWriter.Close: %v", err)
	}

	resp6 := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analyze-audio", audioWriter.FormDataContentType(), audioBody)
	defer resp6.Body.Close()
	if resp6.StatusCode != http.StatusOK {
		t.Errorf("POST /api/v1/analyze-audio status = %d, want %d", resp6.StatusCode, http.StatusOK)
	}

	var audioResult audio.Result
	if err := json.NewDecoder(resp6.Body).Decode(&audioResult); err != nil {
		t.Fatalf("decode POST /api/v1/analyze-audio response: %v", err)
	}
	if audioResult.Verdict != "fake" || audioResult.Chunks != 2 {
		t.Errorf("audioResult = %+v, want Verdict=fake Chunks=2", audioResult)
	}
}

// TestServer_AuthWiring proves 7.7.1's routing decision itself: the
// entire /api/v1 group requires the configured bearer token, and
// /health is deliberately exempt so a Kubernetes probe or load
// balancer can call it without one. internal/auth/middleware_test.go
// covers the middleware's own logic in isolation; this test is only
// about which routes server.go actually applied it to.
func TestServer_AuthWiring(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := httpserver.New(config.Config{
		Port:                 "0",
		Environment:          "test",
		DetectorTimeout:      5 * time.Second,
		AudioDetectorTimeout: 5 * time.Second,
		WorkerCount:          1,
		WorkerQueueSize:      1,
		RedisURL:             "redis://" + mr.Addr(),
		JobTTL:               time.Hour,
		AuthToken:            testAuthToken,
		AdminAuthToken:       testAdminAuthToken,
	}, logger)
	if err != nil {
		t.Fatalf("httpserver.New() error = %v", err)
	}
	defer srv.Redis.Close()
	defer srv.StopWorkers()
	defer srv.Pool.Shutdown(context.Background())

	ts := httptest.NewServer(srv.HTTP.Handler)
	defer ts.Close()

	// /health: no Authorization header at all, must not be rejected for
	// that reason (it may still report 503 if a dependency is down —
	// what matters here is it's never 401).
	healthResp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode == http.StatusUnauthorized {
		t.Error("GET /health returned 401 — /health must stay public")
	}

	// A protected route with no Authorization header must be rejected
	// before it ever reaches the handler.
	noAuthResp, err := http.Post(ts.URL+"/api/v1/analysis", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/v1/analysis (no auth): %v", err)
	}
	defer noAuthResp.Body.Close()
	if noAuthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /api/v1/analysis with no Authorization header status = %d, want %d", noAuthResp.StatusCode, http.StatusUnauthorized)
	}

	// Same route, wrong token — also rejected, and still before the
	// handler (a handler-level 400 for the empty body would prove the
	// middleware let it through, which must not happen).
	wrongAuthReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/analysis", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	wrongAuthReq.Header.Set("Authorization", "Bearer wrong-token")
	wrongAuthResp, err := http.DefaultClient.Do(wrongAuthReq)
	if err != nil {
		t.Fatalf("POST /api/v1/analysis (wrong auth): %v", err)
	}
	defer wrongAuthResp.Body.Close()
	if wrongAuthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /api/v1/analysis with wrong token status = %d, want %d", wrongAuthResp.StatusCode, http.StatusUnauthorized)
	}

	// The same route with the correct token reaches the handler — an
	// empty body now fails validation (400), not authentication (401),
	// proving the middleware actually let it through.
	validAuthResp := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analysis", "application/json", strings.NewReader(`{}`))
	defer validAuthResp.Body.Close()
	if validAuthResp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /api/v1/analysis with valid token, empty body status = %d, want %d (validation, not auth, should reject this)", validAuthResp.StatusCode, http.StatusBadRequest)
	}

	// 7.7.2: GET /sessions/metrics is the one admin-only route so far.
	// A plain user token is authenticated but not authorized for it —
	// 403, not 401.
	userMetricsResp := doAuthed(t, http.MethodGet, ts.URL+"/api/v1/sessions/metrics", "", nil)
	defer userMetricsResp.Body.Close()
	if userMetricsResp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /sessions/metrics with user token status = %d, want %d", userMetricsResp.StatusCode, http.StatusForbidden)
	}

	// The admin token reaches it.
	adminMetricsReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/sessions/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	adminMetricsReq.Header.Set("Authorization", "Bearer "+testAdminAuthToken)
	adminMetricsResp, err := http.DefaultClient.Do(adminMetricsReq)
	if err != nil {
		t.Fatalf("GET /sessions/metrics (admin auth): %v", err)
	}
	defer adminMetricsResp.Body.Close()
	if adminMetricsResp.StatusCode != http.StatusOK {
		t.Errorf("GET /sessions/metrics with admin token status = %d, want %d", adminMetricsResp.StatusCode, http.StatusOK)
	}

	// No token at all on the admin-only route: still 401, never 403 —
	// authentication failure and authorization failure must stay
	// distinguishable even on a route that also has a role requirement.
	noAuthMetricsResp, err := http.Get(ts.URL + "/api/v1/sessions/metrics")
	if err != nil {
		t.Fatalf("GET /sessions/metrics (no auth): %v", err)
	}
	defer noAuthMetricsResp.Body.Close()
	if noAuthMetricsResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /sessions/metrics with no token status = %d, want %d", noAuthMetricsResp.StatusCode, http.StatusUnauthorized)
	}
}

// TestServer_RateLimitWiring proves 7.7.3's routing decision: POST
// /api/v1/analysis is capped at analysisCreateRateLimit (10/minute),
// stricter than every other /api/v1 route's defaultRateLimit
// (60/minute) — and an unauthenticated request never gets 429, only
// 401, since auth.Middleware runs first and rejects it before
// ratelimit.Middleware is ever reached. internal/ratelimit's own tests
// cover the limiter and its Gin middleware in isolation; this is only
// about which limit server.go actually applied to which route.
func TestServer_RateLimitWiring(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := httpserver.New(config.Config{
		Port:                 "0",
		Environment:          "test",
		DetectorTimeout:      5 * time.Second,
		AudioDetectorTimeout: 5 * time.Second,
		WorkerCount:          1,
		WorkerQueueSize:      1,
		RedisURL:             "redis://" + mr.Addr(),
		JobTTL:               time.Hour,
		AuthToken:            testAuthToken,
		AdminAuthToken:       testAdminAuthToken,
	}, logger)
	if err != nil {
		t.Fatalf("httpserver.New() error = %v", err)
	}
	defer srv.Redis.Close()
	defer srv.StopWorkers()
	defer srv.Pool.Shutdown(context.Background())

	ts := httptest.NewServer(srv.HTTP.Handler)
	defer ts.Close()

	// Unauthenticated request on the stricter route: 401, never 429 —
	// proven before ever touching the limit itself, since a client
	// racing straight for /api/v1/analysis without a token must never
	// see a rate-limit response.
	noAuthResp, err := http.Post(ts.URL+"/api/v1/analysis", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/v1/analysis (no auth): %v", err)
	}
	defer noAuthResp.Body.Close()
	if noAuthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /api/v1/analysis with no token status = %d, want %d", noAuthResp.StatusCode, http.StatusUnauthorized)
	}

	// 10 authenticated requests against POST /api/v1/analysis must all
	// avoid the rate limiter (whatever the handler itself does with an
	// empty body — no database is configured here, so it may well fail
	// validation or persistence, but that's a different status, never
	// 429). The 11th must be rate-limited.
	const analysisLimit = 10
	for i := 1; i <= analysisLimit; i++ {
		resp := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analysis", "application/json", strings.NewReader(`{}`))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("POST /api/v1/analysis request %d/%d status = %d, want anything but %d this early", i, analysisLimit, resp.StatusCode, http.StatusTooManyRequests)
		}
	}
	overLimitResp := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analysis", "application/json", strings.NewReader(`{}`))
	defer overLimitResp.Body.Close()
	if overLimitResp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("POST /api/v1/analysis request %d status = %d, want %d", analysisLimit+1, overLimitResp.StatusCode, http.StatusTooManyRequests)
	}
	if retryAfter := overLimitResp.Header.Get("Retry-After"); retryAfter == "" {
		t.Error("Retry-After header missing on a 429 response")
	}

	// A different route in the shared "default" scope, for the very
	// same (now analysis-create-exhausted) client, must still work —
	// the stricter scope's exhaustion must not bleed into the default
	// scope's own separate budget.
	otherRouteResp := doAuthed(t, http.MethodGet, ts.URL+"/api/v1/analysis/jobs/does-not-exist", "", nil)
	defer otherRouteResp.Body.Close()
	if otherRouteResp.StatusCode == http.StatusTooManyRequests {
		t.Error("GET /api/v1/analysis/jobs/:id status = 429 — exhausting POST /api/v1/analysis's own limit must not affect the default scope")
	}
}

// TestServer_SSRFProtection_CannotBypass is 7.7.5's end-to-end proof,
// exercised through the real POST /api/v1/analysis handler (the actual
// API boundary a client talks to), that a malicious video_url/audio_url
// never gets anywhere close to a worker actually fetching it: every
// case here is rejected with 400 before a job is ever persisted or
// enqueued — the request never reaches Redis, let alone a worker or a
// detector. This is deliberately black-box (no internal hook into the
// validator or queue) to prove the property from a real attacker's own
// vantage point: submit the request, see what comes back.
//
// This complements, rather than replaces, two more targeted tests
// elsewhere: internal/analysisworker's
// TestVideoHandler_Handle_BlockedBySSRFValidation proves the *second*
// gate (the worker's own revalidation, for a URL that was safe when
// the job was created but isn't by the time a worker picks it up), and
// internal/security's TestFetch_RedirectToPrivateIP_Blocked proves
// SafeFetcher itself refuses to follow a redirect into a private
// network — something no amount of validating the original URL alone
// (what this API-boundary check does) could ever catch, which is
// exactly why both layers exist.
func TestServer_SSRFProtection_CannotBypass(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := httpserver.New(config.Config{
		Port:                 "0",
		Environment:          "test",
		DetectorTimeout:      5 * time.Second,
		AudioDetectorTimeout: 5 * time.Second,
		WorkerCount:          1,
		WorkerQueueSize:      1,
		RedisURL:             "redis://" + mr.Addr(),
		JobTTL:               time.Hour,
		AuthToken:            testAuthToken,
		AdminAuthToken:       testAdminAuthToken,
	}, logger)
	if err != nil {
		t.Fatalf("httpserver.New() error = %v", err)
	}
	defer srv.Redis.Close()
	defer srv.StopWorkers()
	defer srv.Pool.Shutdown(context.Background())

	ts := httptest.NewServer(srv.HTTP.Handler)
	defer ts.Close()

	maliciousURLs := []string{
		`http://127.0.0.1:5432/`,         // the gateway's own database port
		`http://localhost:8080/`,         // the gateway itself, by name
		`http://169.254.169.254/latest/`, // cloud metadata endpoint
		`http://10.0.0.5/internal`,       // RFC 1918 private range
		`http://0x7f000001/`,             // 127.0.0.1, hex-encoded (7.7.4's alternate-form check)
		`http://[::1]/`,                  // IPv6 loopback
		`file:///etc/passwd`,             // disallowed scheme
	}

	for _, malicious := range maliciousURLs {
		t.Run(malicious, func(t *testing.T) {
			body := fmt.Sprintf(`{"session_id":"session-1","video_url":%q}`, malicious)
			resp := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analysis", "application/json", strings.NewReader(body))
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("POST /api/v1/analysis with video_url=%q status = %d, want %d — this must never become a job", malicious, resp.StatusCode, http.StatusBadRequest)
			}

			// Confirm no job actually made it onto the Redis queue —
			// not just that the HTTP response looked like a rejection.
			if !mr.Exists("mithyax:jobs:video_analysis") {
				return // nothing was ever enqueued at all, which is the expected case
			}
			entries, err := mr.List("mithyax:jobs:video_analysis")
			if err != nil {
				t.Fatalf("mr.List() error = %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("video_url=%q: %d entries found on the video analysis queue, want 0 — nothing should have been enqueued", malicious, len(entries))
			}
		})
	}

	// The same protection applies to audio_url.
	body := `{"session_id":"session-1","audio_url":"http://169.254.169.254/latest/meta-data"}`
	resp := doAuthed(t, http.MethodPost, ts.URL+"/api/v1/analysis", "application/json", strings.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /api/v1/analysis with a blocked audio_url status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}
