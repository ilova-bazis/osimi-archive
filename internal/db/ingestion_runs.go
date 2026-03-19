package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

func (d *DB) SeedIngestionRun(ctx context.Context, ingestionID, leaseID string, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return fmt.Errorf("itemIDs must not be empty")
	}

	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return retryOnBusy(qctx, 4, 25*time.Millisecond, func() error {
		tx, err := d.BeginTx(qctx, &sql.TxOptions{})
		if err != nil {
			return err
		}
		defer tx.Rollback()

		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(qctx, `
			INSERT INTO ingestion_runs (
				ingestion_id, lease_id, expected_items, terminal_items, succeeded_items, failed_items, aggregate_emitted, created_at, updated_at
			) VALUES (?, ?, ?, 0, 0, 0, 0, ?, ?)
			ON CONFLICT(ingestion_id) DO UPDATE SET
				lease_id = excluded.lease_id,
				expected_items = excluded.expected_items,
				terminal_items = 0,
				succeeded_items = 0,
				failed_items = 0,
				aggregate_emitted = 0,
				updated_at = excluded.updated_at
		`, ingestionID, leaseID, len(itemIDs), now, now); err != nil {
			return err
		}

		if _, err := tx.ExecContext(qctx, `DELETE FROM ingestion_run_items WHERE ingestion_id = ?`, ingestionID); err != nil {
			return err
		}

		for _, itemID := range itemIDs {
			if _, err := tx.ExecContext(qctx, `
				INSERT INTO ingestion_run_items (ingestion_id, ingestion_item_id, state, updated_at)
				VALUES (?, ?, 'pending', ?)
			`, ingestionID, itemID, now); err != nil {
				return err
			}
		}

		return tx.Commit()
	})
}

func (d *DB) RecordIngestionItemProcessing(ctx context.Context, ingestionID, ingestionItemID, eventID, payloadJSON string) error {
	return d.recordIngestionItemState(ctx, ingestionID, ingestionItemID, eventID, "INGESTION_ITEM_PROCESSING", payloadJSON, sql.NullString{}, false)
}

func (d *DB) RecordIngestionItemFailed(ctx context.Context, ingestionID, ingestionItemID, eventID, payloadJSON string) error {
	return d.recordIngestionItemState(ctx, ingestionID, ingestionItemID, eventID, "INGESTION_ITEM_FAILED", payloadJSON, sql.NullString{}, true)
}

func (d *DB) RecordIngestionItemCompleted(ctx context.Context, ingestionID, ingestionItemID, objectID string, itemEventID, itemPayloadJSON, objectEventID, objectPayloadJSON, aggregateEventID string) (bool, error) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var aggregateInserted bool
	err := retryOnBusy(qctx, 4, 25*time.Millisecond, func() error {
		tx, err := d.BeginTx(qctx, &sql.TxOptions{})
		if err != nil {
			return err
		}
		defer tx.Rollback()

		now := time.Now().UTC().Format(time.RFC3339)
		res, err := tx.ExecContext(qctx, `
			UPDATE ingestion_run_items
			SET state = 'completed', object_id = ?, updated_at = ?
			WHERE ingestion_id = ? AND ingestion_item_id = ? AND state IN ('pending', 'processing')
		`, objectID, now, ingestionID, ingestionItemID)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return tx.Commit()
		}

		if err := insertVPSEventTx(qctx, tx, VPSEvent{
			EventID:         itemEventID,
			IngestionID:     ingestionID,
			IngestionItemID: sql.NullString{String: ingestionItemID, Valid: true},
			ObjectID:        sql.NullString{String: objectID, Valid: objectID != ""},
			EventType:       "INGESTION_ITEM_COMPLETED",
			PayloadJSON:     itemPayloadJSON,
		}); err != nil {
			return err
		}
		if err := insertVPSEventTx(qctx, tx, VPSEvent{
			EventID:         objectEventID,
			IngestionID:     ingestionID,
			IngestionItemID: sql.NullString{String: ingestionItemID, Valid: true},
			ObjectID:        sql.NullString{String: objectID, Valid: objectID != ""},
			EventType:       "OBJECT_CREATED",
			PayloadJSON:     objectPayloadJSON,
		}); err != nil {
			return err
		}

		total, completed, failed, err := ingestionRunCountsTx(qctx, tx, ingestionID)
		if err != nil {
			return err
		}
		if err := updateIngestionRunCountsTx(qctx, tx, ingestionID, total, completed, failed, now); err != nil {
			return err
		}

		if total > 0 && completed == total {
			payloadJSON, err := json.Marshal(map[string]any{
				"step":                  "ingest",
				"item_count":            total,
				"successful_item_count": completed,
			})
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(qctx, `
				UPDATE ingestion_runs
				SET aggregate_emitted = 1, updated_at = ?
				WHERE ingestion_id = ? AND aggregate_emitted = 0
			`, now, ingestionID)
			if err != nil {
				return err
			}
			rowsAffected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if rowsAffected == 1 {
				aggregateInserted = true
				if err := insertVPSEventTx(qctx, tx, VPSEvent{
					EventID:     aggregateEventID,
					IngestionID: ingestionID,
					EventType:   "INGESTION_COMPLETED",
					PayloadJSON: string(payloadJSON),
				}); err != nil {
					return err
				}
				if _, err := tx.ExecContext(qctx, `
					UPDATE ingestion_lease_tokens
					SET state = 'completed', updated_at = ?
					WHERE ingestion_id = ?
				`, now, ingestionID); err != nil {
					return err
				}
			}
		}

		return tx.Commit()
	})
	if err != nil {
		return false, err
	}
	return aggregateInserted, nil
}

func (d *DB) recordIngestionItemState(ctx context.Context, ingestionID, ingestionItemID, eventID, eventType, payloadJSON string, objectID sql.NullString, terminal bool) error {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return retryOnBusy(qctx, 4, 25*time.Millisecond, func() error {
		tx, err := d.BeginTx(qctx, &sql.TxOptions{})
		if err != nil {
			return err
		}
		defer tx.Rollback()

		now := time.Now().UTC().Format(time.RFC3339)
		state := "processing"
		allowedFrom := "pending"
		if terminal {
			state = "failed"
			allowedFrom = "pending', 'processing"
		}

		query := fmt.Sprintf(`
			UPDATE ingestion_run_items
			SET state = '%s', object_id = ?, updated_at = ?
			WHERE ingestion_id = ? AND ingestion_item_id = ? AND state IN ('%s')
		`, state, allowedFrom)
		res, err := tx.ExecContext(qctx, query, objectID, now, ingestionID, ingestionItemID)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return tx.Commit()
		}

		if err := insertVPSEventTx(qctx, tx, VPSEvent{
			EventID:         eventID,
			IngestionID:     ingestionID,
			IngestionItemID: sql.NullString{String: ingestionItemID, Valid: true},
			ObjectID:        objectID,
			EventType:       eventType,
			PayloadJSON:     payloadJSON,
		}); err != nil {
			return err
		}

		total, completed, failed, err := ingestionRunCountsTx(qctx, tx, ingestionID)
		if err != nil {
			return err
		}
		if err := updateIngestionRunCountsTx(qctx, tx, ingestionID, total, completed, failed, now); err != nil {
			return err
		}

		return tx.Commit()
	})
}

func ingestionRunCountsTx(ctx context.Context, tx *sql.Tx, ingestionID string) (int, int, int, error) {
	var total, completed, failed int
	err := tx.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END),
			SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END)
		FROM ingestion_run_items
		WHERE ingestion_id = ?
	`, ingestionID).Scan(&total, &completed, &failed)
	return total, completed, failed, err
}

func updateIngestionRunCountsTx(ctx context.Context, tx *sql.Tx, ingestionID string, total, completed, failed int, now string) error {
	terminal := completed + failed
	_, err := tx.ExecContext(ctx, `
		UPDATE ingestion_runs
		SET expected_items = ?, terminal_items = ?, succeeded_items = ?, failed_items = ?, updated_at = ?
		WHERE ingestion_id = ?
	`, total, terminal, completed, failed, now, ingestionID)
	return err
}

func insertVPSEventTx(ctx context.Context, tx *sql.Tx, ev VPSEvent) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if ev.CreatedAt == "" {
		ev.CreatedAt = now
	}
	if !ev.NextAttemptAt.Valid {
		ev.NextAttemptAt = sql.NullString{String: now, Valid: true}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO vps_events (event_id, ingestion_id, ingestion_item_id, object_id, event_type, payload_json, created_at, attempts, next_attempt_at, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')
	`, ev.EventID, ev.IngestionID, ev.IngestionItemID, ev.ObjectID, ev.EventType, ev.PayloadJSON, ev.CreatedAt, ev.Attempts, ev.NextAttemptAt)
	if err != nil {
		log.Printf("db insertVPSEventTx failed: %v", err)
	}
	return err
}
