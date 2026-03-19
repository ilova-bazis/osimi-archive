package db

import (
	"context"
	"database/sql"
	"log"
	"time"
)

type VPSEvent struct {
	EventID         string
	IngestionID     string
	IngestionItemID sql.NullString
	ObjectID        sql.NullString
	EventType       string
	PayloadJSON     string
	CreatedAt       string
	Attempts        int
	NextAttemptAt   sql.NullString
	State           string
	LastError       sql.NullString
	SentAt          sql.NullString
}

func (d *DB) UpsertIngestionLease(ctx context.Context, ingestionID, leaseID, leaseToken, leaseExpiresAt string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		INSERT INTO ingestion_lease_tokens (ingestion_id, lease_id, lease_token, lease_expires_at, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(ingestion_id) DO UPDATE SET
			lease_id = excluded.lease_id,
			lease_token = excluded.lease_token,
			lease_expires_at = excluded.lease_expires_at,
			updated_at = excluded.updated_at
	`, ingestionID, leaseID, leaseToken, leaseExpiresAt, now, now)
	if err != nil {
		log.Printf("db UpsertIngestionLease failed: %v", err)
	}
	return err
}

func (d *DB) GetIngestionLease(ctx context.Context, ingestionID string) (leaseID, leaseToken, leaseExpiresAt, state string, err error) {
	err = d.QueryRowContext(ctx, `
		SELECT lease_id, lease_token, lease_expires_at, state
		FROM ingestion_lease_tokens
		WHERE ingestion_id = ?
	`, ingestionID).Scan(&leaseID, &leaseToken, &leaseExpiresAt, &state)
	return
}

func (d *DB) SetIngestionLeaseState(ctx context.Context, ingestionID, state string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		UPDATE ingestion_lease_tokens
		SET state = ?, updated_at = ?
		WHERE ingestion_id = ?
	`, state, now, ingestionID)
	if err != nil {
		log.Printf("db SetIngestionLeaseState failed: %v", err)
	}
	return err
}

func (d *DB) EnqueueVPSEvent(ctx context.Context, ev VPSEvent) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if ev.CreatedAt == "" {
		ev.CreatedAt = now
	}
	if !ev.NextAttemptAt.Valid {
		ev.NextAttemptAt = sql.NullString{String: now, Valid: true}
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO vps_events (event_id, ingestion_id, ingestion_item_id, object_id, event_type, payload_json, created_at, attempts, next_attempt_at, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')
	`, ev.EventID, ev.IngestionID, ev.IngestionItemID, ev.ObjectID, ev.EventType, ev.PayloadJSON, ev.CreatedAt, ev.Attempts, ev.NextAttemptAt)
	if err != nil {
		log.Printf("db EnqueueVPSEvent failed: %v", err)
	}
	return err
}

func (d *DB) FetchPendingVPSEvents(ctx context.Context, limit int) ([]VPSEvent, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT event_id, ingestion_id, ingestion_item_id, object_id, event_type, payload_json, created_at, attempts, next_attempt_at, state, last_error, sent_at
		FROM vps_events
		WHERE state = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY created_at ASC
		LIMIT ?
	`, time.Now().UTC().Format(time.RFC3339), limit)
	if err != nil {
		log.Printf("db FetchPendingVPSEvents failed: %v", err)
		return nil, err
	}
	defer rows.Close()

	var events []VPSEvent
	for rows.Next() {
		var ev VPSEvent
		if err := rows.Scan(&ev.EventID, &ev.IngestionID, &ev.IngestionItemID, &ev.ObjectID, &ev.EventType, &ev.PayloadJSON, &ev.CreatedAt, &ev.Attempts, &ev.NextAttemptAt, &ev.State, &ev.LastError, &ev.SentAt); err != nil {
			log.Printf("db FetchPendingVPSEvents scan failed: %v", err)
			continue
		}
		events = append(events, ev)
	}
	return events, nil
}

func (d *DB) MarkVPSEventSent(ctx context.Context, eventID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		UPDATE vps_events
		SET state = 'sent', sent_at = ?
		WHERE event_id = ?
	`, now, eventID)
	if err != nil {
		log.Printf("db MarkVPSEventSent failed: %v", err)
	}
	return err
}

func (d *DB) MarkVPSEventFailed(ctx context.Context, eventID, errMsg, nextAttemptAt string) error {
	_, err := d.ExecContext(ctx, `
		UPDATE vps_events
		SET state = 'failed', last_error = ?, next_attempt_at = ?, attempts = attempts + 1
		WHERE event_id = ?
	`, errMsg, nextAttemptAt, eventID)
	if err != nil {
		log.Printf("db MarkVPSEventFailed failed: %v", err)
	}
	return err
}

func (d *DB) HasPendingEvents(ctx context.Context, ingestionID string) (bool, error) {
	var count int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM vps_events
		WHERE ingestion_id = ? AND state = 'pending'
	`, ingestionID).Scan(&count)
	if err != nil {
		log.Printf("db HasPendingEvents failed: %v", err)
		return false, err
	}
	return count > 0, nil
}
