package analysis_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/database"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
)

// newTestRepositories connects to a real PostgreSQL instance named by
// GATEWAY_TEST_DATABASE_URL, runs migrations, and returns both
// repositories backed by it — skipping (not failing) the test if that
// isn't set or nothing is reachable, so `go test ./...` stays green
// without Docker running. It returns the sessions repository too because
// analysis_results.session_id is a foreign key: every test here needs a
// real parent session row before it can insert an analysis result for
// it, the same as production code does via NewSessionWebSocket.
func newTestRepositories(t *testing.T) (analysisrepo.Repository, sessionrepo.Repository) {
	t.Helper()

	testDatabaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if testDatabaseURL == "" {
		t.Skip("GATEWAY_TEST_DATABASE_URL not set; skipping test that needs a real PostgreSQL instance (see deployments/docker/.env.example)")
	}

	db, err := database.New(context.Background(), testDatabaseURL)
	if err != nil {
		t.Fatalf("database.New() error = %v", err)
	}
	t.Cleanup(db.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.HealthCheck(ctx); err != nil {
		t.Skipf("PostgreSQL not reachable at %s: %v", testDatabaseURL, err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	return analysisrepo.NewPostgres(db.Pool), sessionrepo.NewPostgres(db.Pool)
}

// newTestSessionID returns an ID unique enough that concurrent runs
// against the same shared dev database never collide.
func newTestSessionID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return fmt.Sprintf("test-%x", buf)
}

// createParentSession inserts the session row analysis_results'
// foreign key requires, and returns its ID.
func createParentSession(t *testing.T, sessions sessionrepo.Repository) string {
	t.Helper()
	id := newTestSessionID(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := sessions.Create(context.Background(), sessionrepo.Session{ID: id, Status: "active", CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatalf("failed to create parent session: %v", err)
	}
	return id
}

func TestPostgres_CreateAndGetBySessionID(t *testing.T) {
	analyses, sessions := newTestRepositories(t)
	ctx := context.Background()
	sessionID := createParentSession(t, sessions)

	videoScore, temporalScore := 0.83, 0.61
	created := time.Now().UTC().Truncate(time.Millisecond)
	result := analysisrepo.Result{
		SessionID:      sessionID,
		VideoFakeScore: &videoScore,
		VideoVerdict:   "fake",
		// AudioFakeScore/AudioVerdict deliberately left unset — this
		// session never got an audio signal, and that should round-trip
		// as absent, not as a false 0.0/"".
		TemporalScore: &temporalScore,
		RiskScore:     0.82,
		RiskVerdict:   "LIKELY_FAKE",
		RiskReasons:   []string{"Video signal indicates likely synthetic or manipulated content"},
		CreatedAt:     created,
	}

	if err := analyses.Create(ctx, result); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := analyses.GetBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetBySessionID() error = %v", err)
	}
	if got.VideoFakeScore == nil || *got.VideoFakeScore != videoScore {
		t.Errorf("VideoFakeScore = %v, want %v", got.VideoFakeScore, videoScore)
	}
	if got.VideoVerdict != "fake" {
		t.Errorf("VideoVerdict = %q, want %q", got.VideoVerdict, "fake")
	}
	if got.AudioFakeScore != nil {
		t.Errorf("AudioFakeScore = %v, want nil (no audio signal was ever gathered)", got.AudioFakeScore)
	}
	if got.AudioVerdict != "" {
		t.Errorf("AudioVerdict = %q, want empty (no audio signal was ever gathered)", got.AudioVerdict)
	}
	if got.TemporalScore == nil || *got.TemporalScore != temporalScore {
		t.Errorf("TemporalScore = %v, want %v", got.TemporalScore, temporalScore)
	}
	if got.RiskScore != 0.82 || got.RiskVerdict != "LIKELY_FAKE" {
		t.Errorf("RiskScore/RiskVerdict = %v/%q, want 0.82/LIKELY_FAKE", got.RiskScore, got.RiskVerdict)
	}
	if len(got.RiskReasons) != 1 || got.RiskReasons[0] != "Video signal indicates likely synthetic or manipulated content" {
		t.Errorf("RiskReasons = %v, want a single matching reason", got.RiskReasons)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
}

func TestPostgres_GetBySessionID_NotFound(t *testing.T) {
	analyses, _ := newTestRepositories(t)

	_, err := analyses.GetBySessionID(context.Background(), "does-not-exist")
	if !errors.Is(err, analysisrepo.ErrNotFound) {
		t.Errorf("GetBySessionID() error = %v, want ErrNotFound", err)
	}
}

// meanCompute is a minimal, deterministic stand-in for the real risk
// engine's weighting — this package doesn't depend on internal/risk
// (see ComputeRisk's doc comment), so tests supply their own simple
// combiner instead of a real one.
func meanCompute(video, audio, temporal *float64) (float64, string, []string) {
	var sum float64
	var n int
	for _, s := range []*float64{video, audio, temporal} {
		if s != nil {
			sum += *s
			n++
		}
	}
	if n == 0 {
		return 0, "UNKNOWN", nil
	}
	score := sum / float64(n)
	verdict := "LIKELY_AUTHENTIC"
	if score >= 0.6 {
		verdict = "LIKELY_FAKE"
	}
	return score, verdict, []string{fmt.Sprintf("combined %d signal(s)", n)}
}

func TestPostgres_UpsertVideoResult_CreatesRow(t *testing.T) {
	analyses, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	if err := analyses.UpsertVideoResult(ctx, sessionID, 0.9, "fake", meanCompute); err != nil {
		t.Fatalf("UpsertVideoResult() error = %v", err)
	}

	got, err := analyses.GetBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetBySessionID() error = %v", err)
	}
	if got.VideoFakeScore == nil || *got.VideoFakeScore != 0.9 || got.VideoVerdict != "fake" {
		t.Errorf("video result = (%v, %q), want (0.9, fake)", got.VideoFakeScore, got.VideoVerdict)
	}
	if got.AudioFakeScore != nil {
		t.Errorf("AudioFakeScore = %v, want nil — no audio job has run for this session", got.AudioFakeScore)
	}
	if got.RiskScore != 0.9 || got.RiskVerdict != "LIKELY_FAKE" {
		t.Errorf("risk = (%v, %q), want (0.9, LIKELY_FAKE) — computed from video alone", got.RiskScore, got.RiskVerdict)
	}
}

// TestPostgres_UpsertVideoThenAudio_Merges is the core proof behind
// 7.5.2/7.5.3's independent VideoWorker/AudioWorker: two separate async
// jobs for the same session, completing in sequence, must merge into
// one row rather than the second overwriting the first's contribution.
func TestPostgres_UpsertVideoThenAudio_Merges(t *testing.T) {
	analyses, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	if err := analyses.UpsertVideoResult(ctx, sessionID, 0.8, "fake", meanCompute); err != nil {
		t.Fatalf("UpsertVideoResult() error = %v", err)
	}
	if err := analyses.UpsertAudioResult(ctx, sessionID, 0.4, "real", meanCompute); err != nil {
		t.Fatalf("UpsertAudioResult() error = %v", err)
	}

	got, err := analyses.GetBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetBySessionID() error = %v", err)
	}
	if got.VideoFakeScore == nil || *got.VideoFakeScore != 0.8 {
		t.Errorf("VideoFakeScore = %v, want 0.8 (the audio upsert must not clobber it)", got.VideoFakeScore)
	}
	if got.AudioFakeScore == nil || *got.AudioFakeScore != 0.4 {
		t.Errorf("AudioFakeScore = %v, want 0.4", got.AudioFakeScore)
	}
	wantRisk := (0.8 + 0.4) / 2
	if got.RiskScore != wantRisk {
		t.Errorf("RiskScore = %v, want %v (mean of both signals)", got.RiskScore, wantRisk)
	}
}

// TestPostgres_UpsertVideoResult_Idempotent proves re-running the same
// upsert (simulating a redelivered job re-executing after its Ack was
// lost — see 7.5.8) leaves the row exactly as it was, not duplicated or
// corrupted.
func TestPostgres_UpsertVideoResult_Idempotent(t *testing.T) {
	analyses, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := analyses.UpsertVideoResult(ctx, sessionID, 0.7, "fake", meanCompute); err != nil {
			t.Fatalf("UpsertVideoResult() call #%d error = %v", i+1, err)
		}
	}

	got, err := analyses.GetBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetBySessionID() error = %v", err)
	}
	if got.VideoFakeScore == nil || *got.VideoFakeScore != 0.7 {
		t.Errorf("VideoFakeScore = %v, want 0.7 after 3 identical upserts", got.VideoFakeScore)
	}
	if got.RiskScore != 0.7 {
		t.Errorf("RiskScore = %v, want 0.7 (repeated upserts must not accumulate/compound)", got.RiskScore)
	}
}

// TestPostgres_ConcurrentUpserts_DoNotRace fires video and audio
// upserts for the same session from concurrent goroutines and checks
// the final row reflects both — proving the row-level lock in
// upsertModality actually serializes the two rather than one clobbering
// the other's read of "what's currently there."
func TestPostgres_ConcurrentUpserts_DoNotRace(t *testing.T) {
	analyses, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	errCh := make(chan error, 2)
	go func() { errCh <- analyses.UpsertVideoResult(ctx, sessionID, 0.75, "fake", meanCompute) }()
	go func() { errCh <- analyses.UpsertAudioResult(ctx, sessionID, 0.25, "real", meanCompute) }()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent upsert error = %v", err)
		}
	}

	got, err := analyses.GetBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetBySessionID() error = %v", err)
	}
	if got.VideoFakeScore == nil || *got.VideoFakeScore != 0.75 {
		t.Errorf("VideoFakeScore = %v, want 0.75", got.VideoFakeScore)
	}
	if got.AudioFakeScore == nil || *got.AudioFakeScore != 0.25 {
		t.Errorf("AudioFakeScore = %v, want 0.25", got.AudioFakeScore)
	}
	wantRisk := (0.75 + 0.25) / 2
	if got.RiskScore != wantRisk {
		t.Errorf("RiskScore = %v, want %v — whichever upsert finished last must have seen the other's committed write", got.RiskScore, wantRisk)
	}
}

// TestPostgres_UpsertVideoResult_NilCompute_SkipsRisk is 7.6.6's core
// mechanism: a completion coordinator that knows the other modality is
// still outstanding passes a nil ComputeRisk, and the score is recorded
// without publishing a premature partial risk.
func TestPostgres_UpsertVideoResult_NilCompute_SkipsRisk(t *testing.T) {
	analyses, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	if err := analyses.UpsertVideoResult(ctx, sessionID, 0.9, "fake", nil); err != nil {
		t.Fatalf("UpsertVideoResult(nil compute) error = %v", err)
	}

	got, err := analyses.GetBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetBySessionID() error = %v", err)
	}
	if got.VideoFakeScore == nil || *got.VideoFakeScore != 0.9 {
		t.Errorf("VideoFakeScore = %v, want 0.9 — the score itself is still recorded", got.VideoFakeScore)
	}
	if got.RiskScore != 0 || got.RiskVerdict != "UNKNOWN" {
		t.Errorf("risk = (%v, %q), want the placeholder (0, UNKNOWN) — audio is still outstanding, nothing should have published a risk yet", got.RiskScore, got.RiskVerdict)
	}
}

// TestPostgres_FinalizeRisk_AfterDeadLetter proves the other half of
// 7.6.6: once a modality's job is dead-lettered rather than completed
// (see internal/analysisworker's OnDeadLetter), FinalizeRisk computes a
// final assessment from whatever did complete, rather than the session
// waiting forever for a result that's never coming.
func TestPostgres_FinalizeRisk_AfterDeadLetter(t *testing.T) {
	analyses, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	// Video completes but, per the coordinator, waits for audio.
	if err := analyses.UpsertVideoResult(ctx, sessionID, 0.85, "fake", nil); err != nil {
		t.Fatalf("UpsertVideoResult(nil compute) error = %v", err)
	}

	// Audio's job is dead-lettered instead of completing — finalize
	// using whatever's on record (video alone).
	if err := analyses.FinalizeRisk(ctx, sessionID, meanCompute); err != nil {
		t.Fatalf("FinalizeRisk() error = %v", err)
	}

	got, err := analyses.GetBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetBySessionID() error = %v", err)
	}
	if got.AudioFakeScore != nil {
		t.Errorf("AudioFakeScore = %v, want nil — audio never completed", got.AudioFakeScore)
	}
	if got.RiskScore != 0.85 {
		t.Errorf("RiskScore = %v, want 0.85 (video alone, since audio dead-lettered)", got.RiskScore)
	}
	if got.RiskVerdict != "LIKELY_FAKE" {
		t.Errorf("RiskVerdict = %q, want %q", got.RiskVerdict, "LIKELY_FAKE")
	}
}

func TestPostgres_FinalizeRisk_NotFound(t *testing.T) {
	analyses, _ := newTestRepositories(t)

	err := analyses.FinalizeRisk(context.Background(), "does-not-exist", meanCompute)
	if !errors.Is(err, analysisrepo.ErrNotFound) {
		t.Errorf("FinalizeRisk() error = %v, want ErrNotFound", err)
	}
}
