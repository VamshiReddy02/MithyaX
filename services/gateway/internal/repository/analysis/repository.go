// Package analysis persists the per-modality breakdown behind a
// completed session's risk assessment — video/audio/temporal scores,
// each modality's own verdict, and the reasons the risk engine gave —
// so "why did MithyaX classify this meeting as HIGH RISK?" can be
// answered after the fact, not just "what was the final score."
//
// This is deliberately a separate table (and package) from
// internal/repository/sessions rather than more columns bolted onto
// sessions: sessions answers "what happened to session X" at a glance,
// this answers "why," and the two are written together but read for
// different reasons.
package analysis

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by GetBySessionID when no analysis result
// exists for the given session ID.
var ErrNotFound = errors.New("analysis result not found")

// Result is the persisted analysis record for one completed session.
// Per-modality fields are pointers/empty-string because a session can
// complete having only ever gathered some of its signals (e.g. video
// never detected a face) — the same "missing means absent, not a
// confident zero" rule risk.Signals already follows.
type Result struct {
	SessionID string `json:"session_id"`

	VideoFakeScore *float64 `json:"video_fake_score,omitempty"`
	VideoVerdict   string   `json:"video_verdict,omitempty"`

	AudioFakeScore *float64 `json:"audio_fake_score,omitempty"`
	AudioVerdict   string   `json:"audio_verdict,omitempty"`

	TemporalScore *float64 `json:"temporal_score,omitempty"`

	RiskScore   float64  `json:"risk_score"`
	RiskVerdict string   `json:"risk_verdict"`
	RiskReasons []string `json:"risk_reasons"`

	CreatedAt time.Time `json:"created_at"`
}

// Repository persists analysis results. *Postgres is the only
// implementation; handlers depend on this interface instead, the same
// separation internal/repository/sessions uses.
type Repository interface {
	// Create records a completed session's full analysis. Since this is
	// 1:1 with sessions (see the schema comment in
	// 0002_create_analysis_results.sql), calling it twice for the same
	// SessionID is a bug in the caller, not a case to silently handle.
	Create(ctx context.Context, result Result) error
	// GetBySessionID looks up the analysis for a session. Returns
	// ErrNotFound if none exists yet (the session hasn't completed) or
	// ever will (the session ID is unknown).
	GetBySessionID(ctx context.Context, sessionID string) (*Result, error)
}
