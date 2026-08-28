package sessions_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/database"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
)

// newTestRepository connects to a real PostgreSQL instance named by
// GATEWAY_TEST_DATABASE_URL, runs migrations, and returns a Postgres
// repository backed by it — skipping (not failing) the test if that
// isn't set or nothing is reachable, so `go test ./...` stays green
// without Docker running. There's deliberately no hardcoded fallback
// DSN here: any real value belongs in your own .env (see
// deployments/docker/.env.example), never in a source file.
func newTestRepository(t *testing.T) sessionrepo.Repository {
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

	return sessionrepo.NewPostgres(db.Pool)
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

func TestPostgres_CreateAndGet(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	id := newTestSessionID(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	session := sessionrepo.Session{ID: id, Status: "active", CreatedAt: now, StartedAt: now}

	if err := repo.Create(ctx, session); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != id || got.Status != "active" {
		t.Errorf("Get() = %+v, want ID=%s Status=active", got, id)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
	if got.EndedAt != nil {
		t.Errorf("EndedAt = %v, want nil for an active session", got.EndedAt)
	}
}

func TestPostgres_Get_NotFound(t *testing.T) {
	repo := newTestRepository(t)

	_, err := repo.Get(context.Background(), "does-not-exist")
	if !errors.Is(err, sessionrepo.ErrNotFound) {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestPostgres_Complete(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	id := newTestSessionID(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := repo.Create(ctx, sessionrepo.Session{ID: id, Status: "active", CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	completedAt := now.Add(30 * time.Second)
	result := sessionrepo.Result{RiskScore: 0.82, Verdict: "LIKELY_FAKE", CompletedAt: completedAt}
	if err := repo.Complete(ctx, id, result); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() after Complete() error = %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	if got.RiskScore == nil || *got.RiskScore != 0.82 {
		t.Errorf("RiskScore = %v, want 0.82", got.RiskScore)
	}
	if got.Verdict != "LIKELY_FAKE" {
		t.Errorf("Verdict = %q, want %q", got.Verdict, "LIKELY_FAKE")
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(completedAt) {
		t.Errorf("EndedAt = %v, want %v", got.EndedAt, completedAt)
	}
}

func TestPostgres_Complete_NotFound(t *testing.T) {
	repo := newTestRepository(t)

	err := repo.Complete(context.Background(), "does-not-exist", sessionrepo.Result{
		RiskScore:   0.5,
		Verdict:     "SUSPICIOUS",
		CompletedAt: time.Now(),
	})
	if !errors.Is(err, sessionrepo.ErrNotFound) {
		t.Errorf("Complete() error = %v, want ErrNotFound", err)
	}
}
