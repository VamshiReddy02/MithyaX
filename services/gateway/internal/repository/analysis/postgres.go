package analysis

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres implements Repository against PostgreSQL via pgx.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres builds a Postgres repository backed by pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func (p *Postgres) Create(ctx context.Context, result Result) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO analysis_results (
			session_id,
			video_fake_score, video_verdict,
			audio_fake_score, audio_verdict,
			temporal_score,
			risk_score, risk_verdict, risk_reasons,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		result.SessionID,
		result.VideoFakeScore, nullString(result.VideoVerdict),
		result.AudioFakeScore, nullString(result.AudioVerdict),
		result.TemporalScore,
		result.RiskScore, result.RiskVerdict, result.RiskReasons,
		result.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert analysis result for session %s: %w", result.SessionID, err)
	}
	return nil
}

func (p *Postgres) GetBySessionID(ctx context.Context, sessionID string) (*Result, error) {
	var r Result
	r.SessionID = sessionID
	var videoVerdict, audioVerdict *string

	err := p.pool.QueryRow(ctx, `
		SELECT
			video_fake_score, video_verdict,
			audio_fake_score, audio_verdict,
			temporal_score,
			risk_score, risk_verdict, risk_reasons,
			created_at
		FROM analysis_results
		WHERE session_id = $1
	`, sessionID).Scan(
		&r.VideoFakeScore, &videoVerdict,
		&r.AudioFakeScore, &audioVerdict,
		&r.TemporalScore,
		&r.RiskScore, &r.RiskVerdict, &r.RiskReasons,
		&r.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get analysis result for session %s: %w", sessionID, err)
	}

	if videoVerdict != nil {
		r.VideoVerdict = *videoVerdict
	}
	if audioVerdict != nil {
		r.AudioVerdict = *audioVerdict
	}

	return &r, nil
}

// nullString turns an empty string into a nil *string, so an absent
// per-modality verdict is stored as SQL NULL rather than "" — the same
// "missing means absent" distinction risk.Signals already draws for the
// scores themselves.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
