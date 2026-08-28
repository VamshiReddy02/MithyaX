// Package sessions persists the durable record of a session — when it
// started and ended, and its final risk score and verdict — answering
// "what happened to session X" after the fact. This is deliberately
// separate from realtime.Store, which holds a live session's in-memory
// working state (queues, accumulated frames, the WebSocket) for exactly
// as long as its connection is open; a Session record here outlives
// that by design.
package sessions

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get and Complete when no session exists
// under the given ID.
var ErrNotFound = errors.New("session not found")

// Session is a persisted session record.
type Session struct {
	ID        string
	Status    string
	CreatedAt time.Time
	StartedAt time.Time
	EndedAt   *time.Time
	RiskScore *float64
	Verdict   string
}

// Result is the final risk assessment recorded when a session completes.
// Verdict is a plain string (not risk.Verdict) so this package doesn't
// need to depend on the risk engine's types — callers convert.
type Result struct {
	RiskScore   float64
	Verdict     string
	CompletedAt time.Time
}

// Repository persists session records. *Postgres is the only
// implementation; handlers and services depend on this interface
// instead, so business logic doesn't need to know it's PostgreSQL at
// all (see postgres.go).
type Repository interface {
	// Create records a newly-started session.
	Create(ctx context.Context, session Session) error
	// Get looks up a session by ID. Returns ErrNotFound if none exists.
	Get(ctx context.Context, id string) (*Session, error)
	// Complete records a session's final risk assessment and marks it
	// completed. Returns ErrNotFound if no session exists under id.
	Complete(ctx context.Context, id string, result Result) error
}
