package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

func (d *DB) NextObjectID(ctx context.Context) (string, int, int, error) {
	now := time.Now().UTC()
	dateKey := now.Format("20060102")
	year := now.Year()
	month := int(now.Month())

	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		log.Printf("db NextObjectID begin tx failed: %v", err)
		return "", 0, 0, err
	}

	defer func() { _ = tx.Rollback() }()
	var n int
	err = tx.QueryRowContext(ctx, `
		INSERT INTO counters(date_key, last_value) VALUES (?, 1)
		ON CONFLICT(date_key) DO UPDATE SET last_value = last_value + 1
		RETURNING last_value
	`, dateKey).Scan(&n)

	if err != nil {
		log.Printf("db NextObjectID update failed: %v", err)
		return "", 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		log.Printf("db NextObjectID commit failed: %v", err)
		return "", 0, 0, err
	}

	objectID := fmt.Sprintf("OBJ-%s-%06d", dateKey, n)
	return objectID, year, month, nil
}
