package jobs

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

func (p *Postgres) Create(ctx context.Context, job Job) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO analysis_jobs (id, session_id, type, status, attempt, max_attempts, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, job.ID, job.SessionID, job.Type, job.Status, job.Attempt, job.MaxAttempts, job.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert job %s: %w", job.ID, err)
	}
	return nil
}

func (p *Postgres) Get(ctx context.Context, id string) (*Job, error) {
	return p.scanOne(ctx, `
		SELECT id, session_id, type, status, attempt, max_attempts, last_error, created_at, started_at, completed_at
		FROM analysis_jobs
		WHERE id = $1
	`, id)
}

func (p *Postgres) GetLatestBySessionAndType(ctx context.Context, sessionID, jobType string) (*Job, error) {
	return p.scanOne(ctx, `
		SELECT id, session_id, type, status, attempt, max_attempts, last_error, created_at, started_at, completed_at
		FROM analysis_jobs
		WHERE session_id = $1 AND type = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID, jobType)
}

func (p *Postgres) scanOne(ctx context.Context, query string, args ...any) (*Job, error) {
	var j Job
	var lastError *string
	err := p.pool.QueryRow(ctx, query, args...).Scan(
		&j.ID, &j.SessionID, &j.Type, &j.Status, &j.Attempt, &j.MaxAttempts,
		&lastError, &j.CreatedAt, &j.StartedAt, &j.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query job: %w", err)
	}
	if lastError != nil {
		j.LastError = *lastError
	}
	return &j, nil
}

func (p *Postgres) MarkProcessing(ctx context.Context, id string, attempt int) error {
	return p.update(ctx, id, `
		UPDATE analysis_jobs
		SET status = $2, attempt = $3, started_at = COALESCE(started_at, now())
		WHERE id = $1
	`, StatusProcessing, attempt)
}

func (p *Postgres) MarkCompleted(ctx context.Context, id string) error {
	return p.update(ctx, id, `
		UPDATE analysis_jobs
		SET status = $2, completed_at = now()
		WHERE id = $1
	`, StatusCompleted)
}

func (p *Postgres) MarkFailed(ctx context.Context, id string, attempt int, lastError string) error {
	return p.update(ctx, id, `
		UPDATE analysis_jobs
		SET status = $2, attempt = $3, last_error = $4
		WHERE id = $1
	`, StatusFailed, attempt, lastError)
}

func (p *Postgres) MarkDeadLetter(ctx context.Context, id string, lastError string) error {
	return p.update(ctx, id, `
		UPDATE analysis_jobs
		SET status = $2, last_error = $3, completed_at = now()
		WHERE id = $1
	`, StatusDeadLetter, lastError)
}

// update runs one status-transition statement and treats "no row
// affected" as ErrNotFound, the same way internal/repository/sessions
// does for its own Complete.
func (p *Postgres) update(ctx context.Context, id, query string, args ...any) error {
	tag, err := p.pool.Exec(ctx, query, append([]any{id}, args...)...)
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
