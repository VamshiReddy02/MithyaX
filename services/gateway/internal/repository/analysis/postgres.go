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

func (p *Postgres) UpsertVideoResult(ctx context.Context, sessionID string, videoScore float64, videoVerdict string, compute ComputeRisk) error {
	return p.upsertModality(ctx, sessionID, "video_fake_score", "video_verdict", videoScore, videoVerdict, compute)
}

func (p *Postgres) UpsertAudioResult(ctx context.Context, sessionID string, audioScore float64, audioVerdict string, compute ComputeRisk) error {
	return p.upsertModality(ctx, sessionID, "audio_fake_score", "audio_verdict", audioScore, audioVerdict, compute)
}

// upsertModality writes one modality's score/verdict into scoreColumn/
// verdictColumn (always "video_fake_score"/"video_verdict" or their
// audio equivalents — never user input, safe to interpolate) and
// recomputes the combined risk from the row's resulting state, all in
// one transaction.
//
// The row-level lock the UPDATE takes is what makes this safe against
// a concurrent UpsertVideoResult/UpsertAudioResult pair for the same
// session racing each other: the second call's UPDATE blocks until the
// first transaction commits, so it always computes risk from the
// first's already-written value rather than a stale read — no lost
// update.
func (p *Postgres) upsertModality(ctx context.Context, sessionID, scoreColumn, verdictColumn string, score float64, verdict string, compute ComputeRisk) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction for session %s: %w", sessionID, err)
	}
	defer tx.Rollback(ctx) // no-op once committed

	// Ensure a row exists before locking it — the first modality to
	// complete for this session creates it; the second (if any) finds
	// it already there and this is a no-op.
	_, err = tx.Exec(ctx, `
		INSERT INTO analysis_results (session_id, risk_score, risk_verdict, risk_reasons, created_at)
		VALUES ($1, 0, 'UNKNOWN', '{}', now())
		ON CONFLICT (session_id) DO NOTHING
	`, sessionID)
	if err != nil {
		return fmt.Errorf("ensure analysis row for session %s: %w", sessionID, err)
	}

	var videoScore, audioScore, temporalScore *float64
	query := fmt.Sprintf(`
		UPDATE analysis_results
		SET %s = $2, %s = $3
		WHERE session_id = $1
		RETURNING video_fake_score, audio_fake_score, temporal_score
	`, scoreColumn, verdictColumn)
	err = tx.QueryRow(ctx, query, sessionID, score, verdict).Scan(&videoScore, &audioScore, &temporalScore)
	if err != nil {
		return fmt.Errorf("update %s for session %s: %w", scoreColumn, sessionID, err)
	}

	riskScore, riskVerdict, riskReasons := compute(videoScore, audioScore, temporalScore)

	_, err = tx.Exec(ctx, `
		UPDATE analysis_results
		SET risk_score = $2, risk_verdict = $3, risk_reasons = $4
		WHERE session_id = $1
	`, sessionID, riskScore, riskVerdict, riskReasons)
	if err != nil {
		return fmt.Errorf("update risk assessment for session %s: %w", sessionID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit analysis upsert for session %s: %w", sessionID, err)
	}
	return nil
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
