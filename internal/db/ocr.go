package db

import (
	"context"
	"time"
)

func (d *DB) UpsertPageText(ctx context.Context, objectID string, pageNumber int, ocrVersion string, text string) error {

	now := time.Now().UTC().Format(time.RFC3339)

	ctx, cancel := context.WithTimeout(ctx, time.Second*3)
	defer cancel()
	_, err := d.ExecContext(ctx, `
		INSERT INTO page_text (object_id, page_number, ocr_version, text, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(object_id, page_number, ocr_version)
		DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at
	`, objectID, pageNumber, ocrVersion, text, now)
	return err
}

func (d *DB) MarkObjectOCRComplete(ctx context.Context, objectID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(ctx, time.Second*3)
	defer cancel()
	_, err := d.ExecContext(ctx, `
		UPDATE objects
		SET has_ocr = 1,
		    processing_state = 'ocr_done',
		    updated_at = ?
		WHERE object_id = ?
	`, now, objectID)
	return err
}
