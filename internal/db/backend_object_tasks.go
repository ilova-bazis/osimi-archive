package db

import (
	"context"
	"database/sql"
	"log"
	"time"
)

type BackendObjectTask struct {
	TaskID        int64
	ObjectID      string
	ActionType    string
	Reason        string
	State         string
	Attempts      int
	NextAttemptAt string
	LastError     sql.NullString
	CreatedAt     string
	UpdatedAt     string
}

func (d *DB) EnqueueBackendObjectTask(ctx context.Context, objectID, actionType, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := d.ExecContext(ctx, `
		UPDATE backend_object_tasks
		SET
			reason = ?,
			state = CASE WHEN state = 'failed' THEN 'pending' ELSE state END,
			next_attempt_at = CASE WHEN state = 'failed' THEN ? ELSE next_attempt_at END,
			last_error = CASE WHEN state = 'failed' THEN NULL ELSE last_error END,
			updated_at = ?
		WHERE object_id = ?
		  AND action_type = ?
		  AND state IN ('pending', 'processing', 'failed')
	`, reason, now, now, objectID, actionType)
	if err != nil {
		log.Printf("db EnqueueBackendObjectTask update failed: %v", err)
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		log.Printf("db EnqueueBackendObjectTask rows affected failed: %v", err)
		return err
	}
	if rows > 0 {
		return nil
	}

	_, err = d.ExecContext(ctx, `
		INSERT INTO backend_object_tasks (
			object_id,
			action_type,
			reason,
			state,
			attempts,
			next_attempt_at,
			created_at,
			updated_at
		) VALUES (?, ?, ?, 'pending', 0, ?, ?, ?)
	`, objectID, actionType, reason, now, now, now)
	if err != nil {
		log.Printf("db EnqueueBackendObjectTask insert failed: %v", err)
	}
	return err
}

func (d *DB) ClaimNextBackendObjectTask(ctx context.Context) (*BackendObjectTask, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("db ClaimNextBackendObjectTask begin tx failed: %v", err)
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	var task BackendObjectTask
	err = tx.QueryRowContext(ctx, `
		SELECT task_id, object_id, action_type, reason, state, attempts, next_attempt_at, last_error, created_at, updated_at
		FROM backend_object_tasks
		WHERE state IN ('pending', 'failed')
		  AND next_attempt_at <= ?
		ORDER BY created_at ASC
		LIMIT 1
	`, now).Scan(
		&task.TaskID,
		&task.ObjectID,
		&task.ActionType,
		&task.Reason,
		&task.State,
		&task.Attempts,
		&task.NextAttemptAt,
		&task.LastError,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		log.Printf("db ClaimNextBackendObjectTask scan failed: %v", err)
		return nil, err
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE backend_object_tasks
		SET state = 'processing', updated_at = ?
		WHERE task_id = ? AND state IN ('pending', 'failed')
	`, now, task.TaskID)
	if err != nil {
		log.Printf("db ClaimNextBackendObjectTask update failed: %v", err)
		return nil, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		log.Printf("db ClaimNextBackendObjectTask rows affected failed: %v", err)
		return nil, err
	}
	if rows == 0 {
		return nil, nil
	}

	task.State = "processing"
	task.UpdatedAt = now

	if err := tx.Commit(); err != nil {
		log.Printf("db ClaimNextBackendObjectTask commit failed: %v", err)
		return nil, err
	}

	return &task, nil
}

func (d *DB) MarkBackendObjectTaskSent(ctx context.Context, taskID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		UPDATE backend_object_tasks
		SET state = 'sent', last_error = NULL, updated_at = ?
		WHERE task_id = ?
	`, now, taskID)
	if err != nil {
		log.Printf("db MarkBackendObjectTaskSent failed: %v", err)
	}
	return err
}

func (d *DB) MarkBackendObjectTaskFailed(ctx context.Context, taskID int64, errMsg, nextAttemptAt string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		UPDATE backend_object_tasks
		SET
			state = 'failed',
			attempts = attempts + 1,
			last_error = ?,
			next_attempt_at = ?,
			updated_at = ?
		WHERE task_id = ?
	`, errMsg, nextAttemptAt, now, taskID)
	if err != nil {
		log.Printf("db MarkBackendObjectTaskFailed failed: %v", err)
	}
	return err
}
