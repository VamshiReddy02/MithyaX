package analysisworker_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/analysisworker"
	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
)

// Phase 7.7.7 — adversarial / defense-in-depth integration tests.
//
// Every test in this file assumes every earlier layer already failed:
// no POST /api/v1/analysis request ever happened, so none of that
// handler's checks (auth, rate limit, request size, URL length, or its
// own SSRF pre-validation) ever ran, and no row was ever written to
// the durable jobs table. The AnalysisJob is built and enqueued
// directly, the way an attacker with raw access to Redis — but nothing
// else — would. Every component wired up here (queue.Redis,
// security.Validator, security.SafeFetcher, Worker) is the real
// production type; only the video/audio detector clients and the
// analysis-results repository are fakes, since the entire point is to
// prove the worker boundary itself — not anything upstream of it —
// is what's actually stopping the attack.
//
// The video-detector fake here always fails the test if it's ever
// called at all: reaching it at all would mean SafeFetcher failed to
// block the fetch before any bytes were handed off.

// failIfCalledVideoAnalyzer fails the test if AnalyzeBytes is ever
// invoked. t.Errorf (not Fatal/FailNow) because this runs on a worker
// goroutine, not the test's own — see the testing package's own rule
// that FailNow must only be called from the test's goroutine.
type failIfCalledVideoAnalyzer struct{ t *testing.T }

func (f *failIfCalledVideoAnalyzer) AnalyzeBytes(ctx context.Context, filename string, data []byte) (*detector.Result, error) {
	f.t.Errorf("video-detector was called with filename=%q — the malicious URL should have been blocked before any fetch completed", filename)
	return &detector.Result{}, nil
}

type failIfCalledAudioAnalyzer struct{ t *testing.T }

func (f *failIfCalledAudioAnalyzer) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	f.t.Errorf("audio-detector was called with filename=%q — the malicious URL should have been blocked before any fetch completed", filename)
	return &audio.Result{}, nil
}

// waitForDeadLetter polls metrics until exactly one job has been
// dead-lettered, or fails the test after timeout.
func waitForDeadLetter(t *testing.T, metrics *analysisworker.Metrics, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsDeadLettered >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job was never dead-lettered within %v — the worker boundary did not hold", timeout)
}

// TestAdversarial_MaliciousLiteralIPJobInjectedDirectly_VideoBlocked is
// 7.7.7's centerpiece scenario: can an attacker bypass every API-layer
// protection by injecting a job straight onto the Redis queue? Here
// the "URL" targets the gateway's own Postgres port directly — no DNS
// involved, so even a validator with zero network access still catches
// it.
func TestAdversarial_MaliciousLiteralIPJobInjectedDirectly_VideoBlocked(t *testing.T) {
	q := newTestQueue(t, "adversarial:video:literal-ip")
	urlValidator := security.NewValidator()
	safeFetcher := security.NewSafeFetcher(urlValidator, security.Config{})
	videoFetcher := analysisworker.NewSafeURLFetcher(safeFetcher, analysisworker.MaxVideoFetchBytes, []string{"video/"})
	repo := newFakeAnalysisRepo()
	coordinator := analysisworker.NewCoordinator(newFakeJobsRepo())
	handler := analysisworker.NewVideoHandler(videoFetcher, &failIfCalledVideoAnalyzer{t: t}, repo, coordinator)

	metrics := analysisworker.NewMetrics()
	// Deliberately empty: this job never went through jobs.Create
	// either — a raw Redis injection wouldn't touch Postgres at all.
	// See TestAdversarial_JobWithNoDurableRecord_StillBlocked for why
	// that specifically matters.
	jobs := newFakeJobsRepo()
	worker := analysisworker.NewWorker(q, handler, jobs, metrics, testLogger())

	job, err := analysisjob.NewVideoAnalysisJob("attacker-session", "http://127.0.0.1:5432/")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer worker.Stop()
	defer cancel()

	waitForDeadLetter(t, metrics, 2*time.Second)

	if _, ok := repo.get("attacker-session"); ok {
		t.Error("a result was persisted for the malicious job — it should never have reached the detector or the repository")
	}
}

// TestAdversarial_MaliciousLiteralIPJobInjectedDirectly_AudioBlocked
// mirrors the video case for AudioHandler — both modalities share the
// exact same SafeURLFetcher-backed defense (7.7.5), so both need to
// independently prove it holds.
func TestAdversarial_MaliciousLiteralIPJobInjectedDirectly_AudioBlocked(t *testing.T) {
	q := newTestQueue(t, "adversarial:audio:literal-ip")
	urlValidator := security.NewValidator()
	safeFetcher := security.NewSafeFetcher(urlValidator, security.Config{})
	audioFetcher := analysisworker.NewSafeURLFetcher(safeFetcher, analysisworker.MaxAudioFetchBytes, []string{"audio/"})
	repo := newFakeAnalysisRepo()
	coordinator := analysisworker.NewCoordinator(newFakeJobsRepo())
	handler := analysisworker.NewAudioHandler(audioFetcher, &failIfCalledAudioAnalyzer{t: t}, repo, coordinator)

	metrics := analysisworker.NewMetrics()
	jobs := newFakeJobsRepo()
	worker := analysisworker.NewWorker(q, handler, jobs, metrics, testLogger())

	job, err := analysisjob.NewAudioAnalysisJob("attacker-session", "http://169.254.169.254/latest/meta-data")
	if err != nil {
		t.Fatalf("NewAudioAnalysisJob() error = %v", err)
	}
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer worker.Stop()
	defer cancel()

	waitForDeadLetter(t, metrics, 2*time.Second)

	if _, ok := repo.get("attacker-session"); ok {
		t.Error("a result was persisted for the malicious job — it should never have reached the detector or the repository")
	}
}

// injectResolver is a security.Resolver returning a fixed, attacker-
// chosen answer for every hostname — standing in for a DNS record an
// attacker controls, or one that changed (rebinding) between when a
// (hypothetical) API check ran and when the worker actually fetches.
type injectResolver struct{ ip net.IP }

func (r injectResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: r.ip}}, nil
}

// TestAdversarial_DNSResolvedPrivateIPJobInjectedDirectly_Blocked
// covers the case that matters most for the "attacker got past the
// API" framing: a hostname-based URL (not a literal IP, so it can't be
// caught by string-matching alone) that resolves to a private address.
// Whether that's because the attacker controls the DNS record or
// because it genuinely rebound after some earlier check, the worker's
// own resolution — done fresh, right here, right before the fetch —
// is what has to catch it.
func TestAdversarial_DNSResolvedPrivateIPJobInjectedDirectly_Blocked(t *testing.T) {
	q := newTestQueue(t, "adversarial:video:dns-private")
	urlValidator := security.NewValidator(security.WithResolver(injectResolver{ip: net.ParseIP("10.0.0.9")}))
	safeFetcher := security.NewSafeFetcher(urlValidator, security.Config{})
	videoFetcher := analysisworker.NewSafeURLFetcher(safeFetcher, analysisworker.MaxVideoFetchBytes, []string{"video/"})
	repo := newFakeAnalysisRepo()
	coordinator := analysisworker.NewCoordinator(newFakeJobsRepo())
	handler := analysisworker.NewVideoHandler(videoFetcher, &failIfCalledVideoAnalyzer{t: t}, repo, coordinator)

	metrics := analysisworker.NewMetrics()
	worker := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	job, err := analysisjob.NewVideoAnalysisJob("attacker-session", "https://attacker-controlled.example/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer worker.Stop()
	defer cancel()

	waitForDeadLetter(t, metrics, 2*time.Second)
}

// TestAdversarial_JobWithNoDurableRecord_StillBlocked makes explicit
// what every test above already relies on: the worker boundary's SSRF
// protection is entirely independent of whether a Postgres jobs row
// exists for this job at all. A real attacker with raw Redis access
// has no reason to also write a matching row to Postgres — Worker's
// own status-tracking writes (MarkProcessing/MarkDeadLetter) are
// documented as best-effort exactly because of cases like this one
// (see Worker's doc comment): they fail silently (logged, not fatal)
// against a fakeJobsRepo that was never told this job ID exists, and
// the fetch is still blocked regardless.
func TestAdversarial_JobWithNoDurableRecord_StillBlocked(t *testing.T) {
	q := newTestQueue(t, "adversarial:video:no-record")
	urlValidator := security.NewValidator()
	safeFetcher := security.NewSafeFetcher(urlValidator, security.Config{})
	videoFetcher := analysisworker.NewSafeURLFetcher(safeFetcher, analysisworker.MaxVideoFetchBytes, []string{"video/"})
	handler := analysisworker.NewVideoHandler(videoFetcher, &failIfCalledVideoAnalyzer{t: t}, newFakeAnalysisRepo(), analysisworker.NewCoordinator(newFakeJobsRepo()))

	metrics := analysisworker.NewMetrics()
	emptyJobs := newFakeJobsRepo() // no put(), no Create() — genuinely knows nothing about this job
	worker := analysisworker.NewWorker(q, handler, emptyJobs, metrics, testLogger())

	job, err := analysisjob.NewVideoAnalysisJob("no-such-session", "http://127.0.0.1/internal")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer worker.Stop()
	defer cancel()

	waitForDeadLetter(t, metrics, 2*time.Second)

	if _, err := emptyJobs.Get(context.Background(), job.ID); err == nil {
		t.Error("a jobs-table record materialized for a job that was never Create()'d — the fake (or the worker) is doing something unexpected")
	}
}

// panicOnFetch is a URLFetcher that panics instead of returning an
// error — simulating a worker crashing mid-fetch (a nil pointer, an
// out-of-bounds slice access, any real-world bug) rather than a clean
// SafeFetcher rejection.
type panicOnFetch struct{}

func (panicOnFetch) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	panic("simulated crash during fetch")
}

// TestAdversarial_WorkerCrashDuringFetch_DeadLettersWithoutCrashingProcess
// proves a panic originating from inside the fetch step specifically —
// not just inside a handler's own logic, which internal/analysisworker's
// worker_test.go already covers — is still caught by Worker's panic
// recovery (safeHandle) and dead-lettered, rather than taking down the
// worker goroutine or the process.
func TestAdversarial_WorkerCrashDuringFetch_DeadLettersWithoutCrashingProcess(t *testing.T) {
	q := newTestQueue(t, "adversarial:video:panic")
	handler := analysisworker.NewVideoHandler(panicOnFetch{}, &failIfCalledVideoAnalyzer{t: t}, newFakeAnalysisRepo(), analysisworker.NewCoordinator(newFakeJobsRepo()))

	metrics := analysisworker.NewMetrics()
	worker := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	job, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer worker.Stop()
	defer cancel()

	waitForDeadLetter(t, metrics, 2*time.Second)

	// The worker goroutine must have survived the panic and still be
	// consuming jobs — proven by enqueuing a second job (through the
	// same always-panicking handler, so it dead-letters too) and
	// confirming it actually gets picked up, rather than the worker
	// having silently stopped after the first panic.
	job2, _ := analysisjob.NewVideoAnalysisJob("session-2", "https://example.com/video2.mp4")
	enqueueJob(t, q, job2)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsDeadLettered >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("second job was never dead-lettered (got %d) — the worker did not survive the panic", metrics.Snapshot().JobsDeadLettered)
}

// timeoutThenSucceedVideoAnalyzer simulates a detector that times out
// on its first call (a transient failure, unrelated to SafeFetcher —
// the fetch itself succeeded fine) and succeeds on a retry.
type timeoutThenSucceedVideoAnalyzer struct {
	calls atomic.Int32
}

func (f *timeoutThenSucceedVideoAnalyzer) AnalyzeBytes(ctx context.Context, filename string, data []byte) (*detector.Result, error) {
	if f.calls.Add(1) == 1 {
		return nil, &detector.Error{Kind: detector.KindTimeout, Message: "video-detector request timed out"}
	}
	return &detector.Result{FakeScore: 0.4, Verdict: "real"}, nil
}

// TestAdversarial_DetectorTimeout_RetriedThenSucceeds proves a
// detector-level timeout (as opposed to a SafeFetcher-level one,
// already covered in internal/security) is correctly classified as
// transient — VideoHandler.IsPermanent only treats KindInvalidVideo
// and a blocked fetch as permanent — so the job is retried rather than
// dead-lettered on the first failure, and a subsequent success
// completes it normally.
func TestAdversarial_DetectorTimeout_RetriedThenSucceeds(t *testing.T) {
	q := newTestQueue(t, "adversarial:video:detector-timeout")
	fetcher := &fakeVideoFetcher{data: []byte("video-bytes")}
	det := &timeoutThenSucceedVideoAnalyzer{}
	repo := newFakeAnalysisRepo()
	handler := analysisworker.NewVideoHandler(fetcher, det, repo, analysisworker.NewCoordinator(newFakeJobsRepo()))

	metrics := analysisworker.NewMetrics()
	worker := analysisworker.NewWorker(q, handler, newFakeJobsRepo(), metrics, testLogger())

	job, err := analysisjob.NewVideoAnalysisJob("session-timeout", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}
	enqueueJob(t, q, job)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	defer worker.Stop()
	defer cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.Snapshot().JobsCompleted == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := metrics.Snapshot()
	if snap.JobsCompleted != 1 {
		t.Fatalf("JobsCompleted = %d, want 1 (the retry after a detector timeout should have succeeded)", snap.JobsCompleted)
	}
	if snap.JobsDeadLettered != 0 {
		t.Errorf("JobsDeadLettered = %d, want 0 — a timeout must be retried, not treated as permanent", snap.JobsDeadLettered)
	}
	if det.calls.Load() != 2 {
		t.Errorf("detector called %d times, want exactly 2 (the failed attempt and the retry)", det.calls.Load())
	}
	if _, ok := repo.get("session-timeout"); !ok {
		t.Error("no result persisted after the retry succeeded")
	}
}
