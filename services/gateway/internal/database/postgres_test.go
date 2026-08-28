package database_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/database"
)

// mustConnect connects to a real PostgreSQL instance named by
// GATEWAY_TEST_DATABASE_URL, skipping (not failing) the test if that
// isn't set or nothing is reachable — so `go test ./...` stays green
// without Docker running, the same way the rest of this codebase uses
// miniredis to avoid needing real Redis rather than skipping, just
// applied to a database with no in-process equivalent. There's
// deliberately no hardcoded fallback DSN here: any real value, dev-only
// or not, belongs in your own .env (see deployments/docker/.env.example),
// never in a source file.
func mustConnect(t *testing.T) *database.DB {
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

	return db
}

func TestHealthCheck(t *testing.T) {
	db := mustConnect(t)

	if err := db.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck() error = %v", err)
	}
}

func TestMigrate_CreatesSessionsTable(t *testing.T) {
	db := mustConnect(t)
	ctx := context.Background()

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// A restarted gateway re-runs every migration on startup (see
	// cmd/gateway) — applying an already-applied migration again must be
	// a no-op, not an error.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() second run error = %v", err)
	}

	var exists bool
	err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'sessions')
	`).Scan(&exists)
	if err != nil {
		t.Fatalf("query information_schema: %v", err)
	}
	if !exists {
		t.Error("sessions table does not exist after Migrate()")
	}
}
