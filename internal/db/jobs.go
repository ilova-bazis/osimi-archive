package db

import (
	"context"
	"database/sql"
	"log"
	"time"
)

type Job struct {
	JobID       string
	ObjectID    string
	JobType     string
	State       string
	PayloadJSON sql.NullString
	Attempt     int
	MaxAttempts int
	QueuedAt    string
	StartedAt   sql.NullString
	FinishedAt  sql.NullString
	ErrorMsg    sql.NullString
}

type OriginKey struct{}

func (d *DB) ClaimNextJob(ctx context.Context, jobType string) (*Job, error) {

	qctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	query := `SELECT job_id, object_id, job_type, state, attempt, max_attempts, queued_at, started_at, finished_at, error_message, payload_json FROM jobs 
	WHERE job_type = ? AND state = 'queued' AND attempt < max_attempts
	ORDER BY queued_at ASC 
	LIMIT 1`

	var claimedJob *Job
	err := retryOnBusy(qctx, 4, 25*time.Millisecond, func() error {
		tx, err := d.BeginTx(qctx, &sql.TxOptions{})
		if err != nil {
			log.Printf("db ClaimNextJob begin tx failed: %v", err)
			return err
		}

		defer tx.Rollback()

		var job Job
		err = tx.QueryRowContext(qctx, query, jobType).Scan(
			&job.JobID,
			&job.ObjectID,
			&job.JobType,
			&job.State,
			&job.Attempt,
			&job.MaxAttempts,
			&job.QueuedAt,
			&job.StartedAt,
			&job.FinishedAt,
			&job.ErrorMsg,
			&job.PayloadJSON,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			log.Printf("db ClaimNextJob scan failed: %v", err)
			return err
		}

		now := time.Now().UTC().Format(time.RFC3339)
		updateQuery := `
			UPDATE jobs 
				SET state = 'running', attempt = attempt + 1, started_at = ?, error_message = NULL 
			WHERE job_id = ? AND state = 'queued'`
		res, err := tx.ExecContext(qctx, updateQuery, now, job.JobID)
		if err != nil {
			log.Printf("db ClaimNextJob update failed: %v", err)
			return err
		}

		affectedRows, err := res.RowsAffected()
		if err != nil {
			log.Printf("db ClaimNextJob rows affected failed: %v", err)
			return err
		}
		if affectedRows == 0 {
			return nil
		}
		if err := tx.Commit(); err != nil {
			log.Printf("db ClaimNextJob commit failed: %v", err)
			return err
		}
		job.Attempt += 1
		job.StartedAt = sql.NullString{String: now, Valid: true}
		job.ErrorMsg = sql.NullString{Valid: false}
		job.State = "running"
		claimedJob = &job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimedJob, nil
}

func (d *DB) InsertJob(ctx context.Context, jobID, objectID, jobType string, payloadJSON *string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	payload := sql.NullString{}
	if payloadJSON != nil {
		payload = sql.NullString{String: *payloadJSON, Valid: true}
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO jobs (
			job_id, object_id, job_type, state, attempt, max_attempts, queued_at, payload_json)
		VALUES (?, ?, ?, 'queued', 0, 3, ?, ?)
	`, jobID, objectID, jobType, now, payload)
	if err != nil {
		log.Printf("db InsertJob failed: %v", err)
	}
	return err
}

func (d *DB) HasActiveJob(ctx context.Context, objectID, jobType string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	row := d.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM jobs
		WHERE object_id = ? AND job_type = ? AND state IN ('queued', 'running')
	`, objectID, jobType)
	var count int
	if err := row.Scan(&count); err != nil {
		log.Printf("db HasActiveJob failed: %v", err)
		return false, err
	}
	return count > 0, nil
}

func (d *DB) MarkJobSucceeded(ctx context.Context, jobID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	q := `
		UPDATE jobs SET
			state = 'succeeded',
			finished_at = ?,
			error_message = NULL 
		WHERE job_id = ?
		`
	_, err := d.ExecContext(ctx, q, now, jobID)
	if err != nil {
		log.Printf("db MarkJobSucceeded failed: %v", err)
		return err
	}
	return nil
}

func (d *DB) MarkJobFailed(ctx context.Context, jobID string, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	q := `
		UPDATE jobs SET
			state = 'failed',
			finished_at = ?,
			error_message = ? 
		WHERE job_id = ?
		`
	_, err := d.ExecContext(ctx, q, now, errMsg, jobID)
	if err != nil {
		log.Printf("db MarkJobFailed failed: %v", err)
		return err
	}
	return nil
}
