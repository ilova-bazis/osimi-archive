package db

import (
	"context"
	"log"
	"strings"
	"time"
)

func (d *DB) AddJobEvent(ctx context.Context, jobID, objectID, level, msg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx, `
		INSERT INTO job_events (job_id, object_id, level, message, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, jobID, objectID, level, msg, now)
	if err != nil {
		log.Printf("db AddJobEvent failed: %v", err)
	}
	return err
}

func (d *DB) RecordJobError(ctx context.Context, jobID, objectID string, err error, force bool) bool {
	if err == nil {
		return false
	}
	if !shouldRecordJobError(err, force) {
		return false
	}
	if logErr := d.AddJobEvent(ctx, jobID, objectID, "error", err.Error()); logErr != nil {
		return false
	}
	return true
}

func shouldRecordJobError(err error, force bool) bool {
	if force {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, expected := range expectedErrorSubstrings {
		if strings.Contains(msg, expected) {
			return false
		}
	}
	return true
}

var expectedErrorSubstrings = []string{
	"catalog.json is missing",
	"catalog.json missing",
	"invalid catalog.json",
	"unsupported file in batch",
	"batch contains mixed media types",
	"batch has no supported media files",
	"batch has no image files",
	"batch media type",
	"no pages found",
	"no media files found",
	"derivatives not supported",
	"unsupported media type",
	"item_kind is not supported",
}
