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
