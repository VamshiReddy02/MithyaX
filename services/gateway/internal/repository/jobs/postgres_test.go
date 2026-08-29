package jobs_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/database"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
)

// newTestRepositories connects to a real PostgreSQL instance named by
// GATEWAY_TEST_DATABASE_URL, runs migrations, and returns both
// repositories backed by it — skipping (not failing) if that isn't set
// or nothing is reachable. See internal/repository/analysis's tests for
// the same pattern; analysis_jobs.session_id is a foreign key, same as
// analysis_results.session_id, so a real parent session is needed here
// too.
func newTestRepositories(t *testing.T) (jobsrepo.Repository, sessionrepo.Repository) {
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

	return jobsrepo.NewPostgres(db.Pool), sessionrepo.NewPostgres(db.Pool)
}

func newTestSessionID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return fmt.Sprintf("test-%x", buf)
}

func createParentSession(t *testing.T, sessions sessionrepo.Repository) string {
	t.Helper()
	id := newTestSessionID(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := sessions.Create(context.Background(), sessionrepo.Session{ID: id, Status: "active", CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatalf("failed to create parent session: %v", err)
	}
	return id
}

func newTestJob(t *testing.T, sessionID, jobType string) jobsrepo.Job {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return jobsrepo.Job{
		ID:          fmt.Sprintf("job-%x", buf),
		SessionID:   sessionID,
		Type:        jobType,
		Status:      jobsrepo.StatusQueued,
		Attempt:     1,
		MaxAttempts: 3,
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestPostgres_CreateAndGet(t *testing.T) {
	jobs, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	job := newTestJob(t, sessionID, "VIDEO_ANALYSIS")
	if err := jobs.Create(ctx, job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.SessionID != sessionID || got.Type != "VIDEO_ANALYSIS" {
		t.Errorf("Get() = %+v, want SessionID=%s Type=VIDEO_ANALYSIS", got, sessionID)
	}
	if got.Status != jobsrepo.StatusQueued {
		t.Errorf("Status = %q, want %q", got.Status, jobsrepo.StatusQueued)
	}
	if got.Attempt != 1 || got.MaxAttempts != 3 {
		t.Errorf("Attempt/MaxAttempts = %d/%d, want 1/3", got.Attempt, got.MaxAttempts)
	}
	if got.StartedAt != nil {
		t.Error("StartedAt is set, want nil for a job that hasn't started")
	}
}

func TestPostgres_Get_NotFound(t *testing.T) {
	jobs, _ := newTestRepositories(t)

	_, err := jobs.Get(context.Background(), "does-not-exist")
	if !errors.Is(err, jobsrepo.ErrNotFound) {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestPostgres_MarkProcessing_SetsStartedAtOnce(t *testing.T) {
	jobs, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	job := newTestJob(t, sessionID, "VIDEO_ANALYSIS")
	if err := jobs.Create(ctx, job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := jobs.MarkProcessing(ctx, job.ID, 1); err != nil {
		t.Fatalf("MarkProcessing() error = %v", err)
	}
	first, err := jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if first.Status != jobsrepo.StatusProcessing {
		t.Errorf("Status = %q, want %q", first.Status, jobsrepo.StatusProcessing)
	}
	if first.StartedAt == nil {
		t.Fatal("StartedAt is nil, want it set")
	}

	// A retry (attempt 2) re-marks processing, but StartedAt must stay
	// pinned to when work first began, not reset on every attempt.
	time.Sleep(10 * time.Millisecond)
	if err := jobs.MarkProcessing(ctx, job.ID, 2); err != nil {
		t.Fatalf("MarkProcessing() (retry) error = %v", err)
	}
	second, err := jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if second.Attempt != 2 {
		t.Errorf("Attempt = %d, want 2", second.Attempt)
	}
	if !second.StartedAt.Equal(*first.StartedAt) {
		t.Errorf("StartedAt changed on retry: %v -> %v, want it unchanged", first.StartedAt, second.StartedAt)
	}
}

func TestPostgres_MarkCompleted(t *testing.T) {
	jobs, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	job := newTestJob(t, sessionID, "AUDIO_ANALYSIS")
	if err := jobs.Create(ctx, job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := jobs.MarkCompleted(ctx, job.ID); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}

	got, err := jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != jobsrepo.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, jobsrepo.StatusCompleted)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt is nil, want it set")
	}
	if !got.Status.IsTerminal() {
		t.Error("IsTerminal() = false for a completed job, want true")
	}
}

func TestPostgres_MarkFailed_IsNotTerminal(t *testing.T) {
	jobs, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	job := newTestJob(t, sessionID, "VIDEO_ANALYSIS")
	if err := jobs.Create(ctx, job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := jobs.MarkFailed(ctx, job.ID, 2, "video-detector unreachable"); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}

	got, err := jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != jobsrepo.StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, jobsrepo.StatusFailed)
	}
	if got.Attempt != 2 {
		t.Errorf("Attempt = %d, want 2", got.Attempt)
	}
	if got.LastError != "video-detector unreachable" {
		t.Errorf("LastError = %q, want %q", got.LastError, "video-detector unreachable")
	}
	if got.Status.IsTerminal() {
		t.Error("IsTerminal() = true for a failed-but-retrying job, want false — it may still complete")
	}
}

func TestPostgres_MarkDeadLetter_IsTerminal(t *testing.T) {
	jobs, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	job := newTestJob(t, sessionID, "AUDIO_ANALYSIS")
	if err := jobs.Create(ctx, job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := jobs.MarkDeadLetter(ctx, job.ID, "malformed audio"); err != nil {
		t.Fatalf("MarkDeadLetter() error = %v", err)
	}

	got, err := jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != jobsrepo.StatusDeadLetter {
		t.Errorf("Status = %q, want %q", got.Status, jobsrepo.StatusDeadLetter)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt is nil, want it set — dead-lettering is also a final outcome")
	}
	if !got.Status.IsTerminal() {
		t.Error("IsTerminal() = false for a dead-lettered job, want true")
	}
}

func TestPostgres_MarkX_NotFound(t *testing.T) {
	jobs, _ := newTestRepositories(t)
	ctx := context.Background()

	if err := jobs.MarkProcessing(ctx, "does-not-exist", 1); !errors.Is(err, jobsrepo.ErrNotFound) {
		t.Errorf("MarkProcessing() error = %v, want ErrNotFound", err)
	}
	if err := jobs.MarkCompleted(ctx, "does-not-exist"); !errors.Is(err, jobsrepo.ErrNotFound) {
		t.Errorf("MarkCompleted() error = %v, want ErrNotFound", err)
	}
	if err := jobs.MarkFailed(ctx, "does-not-exist", 1, "x"); !errors.Is(err, jobsrepo.ErrNotFound) {
		t.Errorf("MarkFailed() error = %v, want ErrNotFound", err)
	}
	if err := jobs.MarkDeadLetter(ctx, "does-not-exist", "x"); !errors.Is(err, jobsrepo.ErrNotFound) {
		t.Errorf("MarkDeadLetter() error = %v, want ErrNotFound", err)
	}
}

// TestPostgres_GetLatestBySessionAndType is what the completion
// coordinator (7.6.6) relies on: given a session and modality, find
// the most recent job for it, or ErrNotFound if that modality was
// never requested at all.
func TestPostgres_GetLatestBySessionAndType(t *testing.T) {
	jobs, sessions := newTestRepositories(t)
	sessionID := createParentSession(t, sessions)
	ctx := context.Background()

	videoJob := newTestJob(t, sessionID, "VIDEO_ANALYSIS")
	if err := jobs.Create(ctx, videoJob); err != nil {
		t.Fatalf("Create(video) error = %v", err)
	}

	got, err := jobs.GetLatestBySessionAndType(ctx, sessionID, "VIDEO_ANALYSIS")
	if err != nil {
		t.Fatalf("GetLatestBySessionAndType(VIDEO_ANALYSIS) error = %v", err)
	}
	if got.ID != videoJob.ID {
		t.Errorf("GetLatestBySessionAndType() = %+v, want ID=%s", got, videoJob.ID)
	}

	_, err = jobs.GetLatestBySessionAndType(ctx, sessionID, "AUDIO_ANALYSIS")
	if !errors.Is(err, jobsrepo.ErrNotFound) {
		t.Errorf("GetLatestBySessionAndType(AUDIO_ANALYSIS) error = %v, want ErrNotFound — audio was never requested", err)
	}
}

func TestStatus_IsTerminal(t *testing.T) {
	cases := map[jobsrepo.Status]bool{
		jobsrepo.StatusQueued:     false,
		jobsrepo.StatusProcessing: false,
		jobsrepo.StatusFailed:     false,
		jobsrepo.StatusCompleted:  true,
		jobsrepo.StatusDeadLetter: true,
	}
	for status, want := range cases {
		if got := status.IsTerminal(); got != want {
			t.Errorf("Status(%q).IsTerminal() = %v, want %v", status, got, want)
		}
	}
}
