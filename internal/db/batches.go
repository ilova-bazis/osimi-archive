package db

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func (d *DB) TryRegisterBatch(ctx context.Context, batchPath, objectID string) (bool, error) {

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		INSERT INTO batch_ingests (batch_path, object_id, created_at)
		VALUES (?, ?, ?)
	`, batchPath, objectID, now)

	if err != nil {
		if isUniqueConstraint(err) {
			log.Printf("batch already registered: batch_path=%s object_id=%s err=%v\n", batchPath, objectID, err)
			return false, nil
		}
		log.Printf("failed to register batch: batch_path=%s object_id=%s err=%v\n", batchPath, objectID, err)
		return false, err
	}
	return true, nil
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return false
	}
	code := sqlErr.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY || (code&0xFF) == sqlite3.SQLITE_CONSTRAINT
}

func isUniqueConstraint_fragile(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return contains(msg, "UNIQUE") || contains(msg, "constraint failed") || contains(msg, "PRIMARY KEY")
}

func contains(s, sub string) bool {
	if len(s) < len(sub) {
		return false
	}
	return strings.Contains(s, sub)
}
