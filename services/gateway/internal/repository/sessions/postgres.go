package sessions

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

func (p *Postgres) Create(ctx context.Context, session Session) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO sessions (id, status, created_at, started_at)
		VALUES ($1, $2, $3, $4)
	`, session.ID, session.Status, session.CreatedAt, session.StartedAt)
	if err != nil {
		return fmt.Errorf("insert session %s: %w", session.ID, err)
	}
	return nil
}

func (p *Postgres) Get(ctx context.Context, id string) (*Session, error) {
	var s Session
	// verdict is NULL until a session completes; Session.Verdict is a
	// plain string (there's no "verdict not yet known" case worth
	// distinguishing from "" the way RiskScore's *float64 distinguishes
	// "no score yet" from a genuine 0.0), so it's scanned through a
	// nullable intermediate rather than changing the field's shape.
	var verdict *string
	err := p.pool.QueryRow(ctx, `
		SELECT id, status, created_at, started_at, ended_at, risk_score, verdict
		FROM sessions
		WHERE id = $1
	`, id).Scan(&s.ID, &s.Status, &s.CreatedAt, &s.StartedAt, &s.EndedAt, &s.RiskScore, &verdict)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", id, err)
	}
	if verdict != nil {
		s.Verdict = *verdict
	}
	return &s, nil
}

func (p *Postgres) Complete(ctx context.Context, id string, result Result) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE sessions
		SET status = 'completed', ended_at = $2, risk_score = $3, verdict = $4
		WHERE id = $1
	`, id, result.CompletedAt, result.RiskScore, result.Verdict)
	if err != nil {
		return fmt.Errorf("complete session %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
