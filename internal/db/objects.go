package db

import (
	"context"
	"log"
	"time"
)

func (d *DB) InsertObject(ctx context.Context, objectID, objectRoot string, year, month int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		INSERT INTO objects (
			object_id, object_root, year, month,
			processing_state, curation_state,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, 'queued', 'needs_review', ?, ?)
	`, objectID, objectRoot, year, month, now, now)
	if err != nil {
		log.Printf("db InsertObject failed: %v", err)
	}
	return err
}
func (d *DB) SetObjectProcessingState(ctx context.Context, objectID string, state string, clearError bool) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339)

	if clearError {
		_, err := d.ExecContext(ctx, `
			UPDATE objects
			SET processing_state = ?,
			    last_error = NULL,
			    last_error_at = NULL,
			    updated_at = ?
			WHERE object_id = ?
		`, state, now, objectID)
		if err != nil {
			log.Printf("db SetObjectProcessingState failed: %v", err)
		}

		return err
	}

	_, err := d.ExecContext(ctx, `
		UPDATE objects
		SET processing_state = ?,
		    updated_at = ?
		WHERE object_id = ?
	`, state, now, objectID)
	if err != nil {
		log.Printf("db SetObjectProcessingState failed: %v", err)
	}
	return err
}

func (d *DB) SetObjectCurationState(ctx context.Context, objectID, state string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	q := `
		UPDATE objects SET
			curation_state = ?,
			updated_at = ?
		WHERE
			object_id = ?
	`
	_, err := d.ExecContext(ctx, q, state, now, objectID)
	if err != nil {
		log.Printf("db SetObjectCurationState failed: %v", err)
	}
	return err

}

func (d *DB) SetObjectError(ctx context.Context, objectID, errMsg string, markFailedState bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	q := `
		UPDATE objects SET
	`
	if markFailedState {
		q += `	
			processing_state = 'processing_failed',
		`
	}
	q += `
			last_error = ?,
			last_error_at = ?,
			updated_at = ?
		WHERE object_id = ?
	`
	_, err := d.ExecContext(ctx, q, errMsg, now, now, objectID)
	if err != nil {
		log.Printf("db SetObjectError failed: %v", err)
	}
	return err

}

func (d *DB) MarkObjectIngested(ctx context.Context, objectID string, pageCount int) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := d.ExecContext(ctx, `
		UPDATE objects SET
			page_count = ?,
			processing_state = 'ingested',
			last_error = NULL,
			last_error_at = NULL,
			updated_at = ?
		WHERE object_id = ?
	`, pageCount, now, objectID)
	if err != nil {
		log.Printf("db MarkObjectIngested failed: %v", err)
	}
	return err
}
