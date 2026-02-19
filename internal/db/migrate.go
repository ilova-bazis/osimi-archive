package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log"
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

	if err := tx.Commit(); err != nil {
		log.Printf("db EnsureSchema commit failed: %v", err)
		return err
	}
	return nil

}
