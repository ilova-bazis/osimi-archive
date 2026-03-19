package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log"
	"strings"
)

//go:embed schema.sql
var schemaSQL string

func (d *DB) EnsureSchema(ctx context.Context) error {
	if d == nil || d.DB == nil {
		return fmt.Errorf("db not initialized")
	}

	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		log.Printf("db EnsureSchema begin tx failed: %v", err)
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `PRAGMA foreign_keys = ON;`); err != nil {
		log.Printf("db EnsureSchema pragma failed: %v", err)
		return err
	}

	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		log.Printf("db EnsureSchema apply schema failed: %v", err)
		return fmt.Errorf("apply schema: %w", err)
	}

	if err := ensureColumnExists(ctx, tx, "vps_events", "ingestion_item_id", "TEXT"); err != nil {
		log.Printf("db EnsureSchema ensure vps_events.ingestion_item_id failed: %v", err)
		return err
	}

	if err := tx.Commit(); err != nil {
		log.Printf("db EnsureSchema commit failed: %v", err)
		return err
	}
	return nil

}

func ensureColumnExists(ctx context.Context, tx *sql.Tx, tableName, columnName, columnDDL string) error {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			typeName   string
			notNull    int
			defaultV   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultV, &primaryKey); err != nil {
			return err
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, columnDDL))
	return err
}
