package ingest

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/ilova-bazis/osimi-archive/internal/db"
)

type eventRecord struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	JobID     string `json:"job_id"`
	JobType   string `json:"job_type"`
	ObjectID  string `json:"object_id"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

func writeObjectEvent(objectRoot, objectID, jobID, jobType, event, level, message string) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	fileTS := strings.ReplaceAll(ts, ":", "-")
	fileEvent := fmt.Sprintf("%s_%s_%s.json", fileTS, event, jobID)
	path := filepath.Join(objectRoot, "events", fileEvent)
	rec := eventRecord{
		Timestamp: ts,
		Event:     event,
		JobID:     jobID,
		JobType:   jobType,
		ObjectID:  objectID,
		Level:     level,
		Message:   message,
	}
	return writeJSONAtomic(path, rec)
}

func recordJobError(ctx context.Context, d *db.DB, objectRoot, objectID, jobID, jobType string, err error, force bool) {
	if err == nil {
		return
	}
	if !d.RecordJobError(ctx, jobID, objectID, err, force) {
		return
	}
	if eventErr := writeObjectEvent(objectRoot, objectID, jobID, jobType, jobType+"_failed", "error", err.Error()); eventErr != nil {
		log.Printf("event write error: %v", eventErr)
	}
}
