package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openRunTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		_ = os.Remove(path)
	})
	if err := d.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return d
}

func TestRecordIngestionItemCompletedEmitsAggregateOnce(t *testing.T) {
	ctx := context.Background()
	d := openRunTestDB(t)
	ingestionID := "ing-1"
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.ExecContext(ctx, `
		INSERT INTO ingestion_lease_tokens (ingestion_id, lease_id, lease_token, lease_expires_at, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)
	`, ingestionID, "lease-1", "tok-1", now, now, now); err != nil {
		t.Fatalf("insert lease token: %v", err)
	}
	if err := d.SeedIngestionRun(ctx, ingestionID, "lease-1", []string{"item-1", "item-2"}); err != nil {
		t.Fatalf("seed ingestion run: %v", err)
	}

	aggregate, err := d.RecordIngestionItemCompleted(ctx, ingestionID, "item-1", "OBJ-20260317-000001", "evt-item-1", `{"step":"ingest"}`, "evt-object-1", `{"object_id":"OBJ-20260317-000001"}`, "evt-agg-1")
	if err != nil {
		t.Fatalf("record first completed item: %v", err)
	}
	if aggregate {
		t.Fatalf("did not expect aggregate event after first item")
	}

	aggregate, err = d.RecordIngestionItemCompleted(ctx, ingestionID, "item-2", "OBJ-20260317-000002", "evt-item-2", `{"step":"ingest"}`, "evt-object-2", `{"object_id":"OBJ-20260317-000002"}`, "evt-agg-2")
	if err != nil {
		t.Fatalf("record second completed item: %v", err)
	}
	if !aggregate {
		t.Fatalf("expected aggregate event after final item")
	}

	var aggregateCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM vps_events WHERE ingestion_id = ? AND event_type = 'INGESTION_COMPLETED'`, ingestionID).Scan(&aggregateCount); err != nil {
		t.Fatalf("count aggregate events: %v", err)
	}
	if aggregateCount != 1 {
		t.Fatalf("expected one aggregate event, got %d", aggregateCount)
	}

	var leaseState string
	if err := d.QueryRowContext(ctx, `SELECT state FROM ingestion_lease_tokens WHERE ingestion_id = ?`, ingestionID).Scan(&leaseState); err != nil {
		t.Fatalf("query lease state: %v", err)
	}
	if leaseState != "completed" {
		t.Fatalf("expected lease state completed, got %s", leaseState)
	}
}

func TestRecordIngestionItemFailurePreventsAggregateCompletion(t *testing.T) {
	ctx := context.Background()
	d := openRunTestDB(t)
	ingestionID := "ing-2"
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.ExecContext(ctx, `
		INSERT INTO ingestion_lease_tokens (ingestion_id, lease_id, lease_token, lease_expires_at, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)
	`, ingestionID, "lease-2", "tok-2", now, now, now); err != nil {
		t.Fatalf("insert lease token: %v", err)
	}
	if err := d.SeedIngestionRun(ctx, ingestionID, "lease-2", []string{"item-1", "item-2"}); err != nil {
		t.Fatalf("seed ingestion run: %v", err)
	}

	if err := d.RecordIngestionItemFailed(ctx, ingestionID, "item-1", "evt-failed-1", `{"step":"ingest","error":"boom"}`); err != nil {
		t.Fatalf("record failed item: %v", err)
	}
	aggregate, err := d.RecordIngestionItemCompleted(ctx, ingestionID, "item-2", "OBJ-20260317-000003", "evt-item-3", `{"step":"ingest"}`, "evt-object-3", `{"object_id":"OBJ-20260317-000003"}`, "evt-agg-3")
	if err != nil {
		t.Fatalf("record completed item: %v", err)
	}
	if aggregate {
		t.Fatalf("did not expect aggregate event when one item failed")
	}

	var aggregateCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM vps_events WHERE ingestion_id = ? AND event_type = 'INGESTION_COMPLETED'`, ingestionID).Scan(&aggregateCount); err != nil {
		t.Fatalf("count aggregate events: %v", err)
	}
	if aggregateCount != 0 {
		t.Fatalf("expected zero aggregate events, got %d", aggregateCount)
	}
}
