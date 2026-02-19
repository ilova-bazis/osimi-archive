package db

import (
	"context"
	"log"
	"time"
)

func (d *DB) SignalOrchestratorWakeup(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	_, err := d.ExecContext(ctx, `
		INSERT INTO orchestrator_wakeup (id, last_signal_at)
		VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET last_signal_at = excluded.last_signal_at
	`, now)
	if err != nil {
		log.Printf("db SignalOrchestratorWakeup failed: %v", err)
	}
	return err
}

func (d *DB) GetOrchestratorWakeup(ctx context.Context) (time.Time, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	row := d.QueryRowContext(ctx, `SELECT last_signal_at FROM orchestrator_wakeup WHERE id = 1`)
	var ts string
	if err := row.Scan(&ts); err != nil {
		return time.Time{}, false, err
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}, false, err
	}
	return parsed, true, nil
}
