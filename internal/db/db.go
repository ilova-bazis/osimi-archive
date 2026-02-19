package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct{ *sql.DB }

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	d, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_busy_timeout=5000&_foreign_keys=on&_pragma=journal_mode(WAL)", path))

	if err != nil {
		return nil, err
	}
	if err = d.Ping(); err != nil {
		return nil, err
	}
	return &DB{DB: d}, nil
}

func (d *DB) GetObjectRoot(ctx context.Context, objectID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	q := `SELECT object_root FROM objects WHERE object_id = ?`

	var root string
	err := d.DB.QueryRowContext(ctx, q, objectID).Scan(&root)
	if err != nil {
		return "", fmt.Errorf("get object root, %w", err)
	}
	return root, nil
}
