// Package database owns the gateway's PostgreSQL connection: the
// persistent source of truth for completed session records (see
// internal/repository/sessions), kept separate from the in-memory
// realtime.Store that holds active live-session state — we don't want
// PostgreSQL handling every video frame, only what a session looked like
// once it's done.
package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps the connection pool every repository is built on.
type DB struct {
	Pool *pgxpool.Pool
}

// New builds a connection pool for databaseURL. Like the gateway's Redis
// client, this doesn't eagerly dial — pgxpool.New only parses the DSN
// and establishes connections lazily as they're needed, so a malformed
// URL fails immediately while an unreachable-but-well-formed one only
// surfaces once something actually queries it. Call HealthCheck to
// verify connectivity explicitly (e.g. at startup, before Migrate).
func New(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// HealthCheck reports whether PostgreSQL is currently reachable.
func (db *DB) HealthCheck(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

// Close releases every connection in the pool. Safe to call once during
// shutdown.
func (db *DB) Close() {
	db.Pool.Close()
}
