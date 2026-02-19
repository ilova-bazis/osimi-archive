package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ilova-bazis/osimi-archive/internal/db"
)

type Watcher struct {
	DB          *db.DB
	IngestDrop  string
	DoneMarker  string
	ArchiveRoot string
}

func (w *Watcher) Walker(ctx context.Context, path string, de os.DirEntry, err error) error {
	if err != nil {
		// TODO: Temporary return nil, add logic to handle error logic
		return err
	}

	if !de.IsDir() {
		return nil
	}

	_, err = os.Stat(filepath.Join(path, w.DoneMarker))

	if err != nil {
		return nil
	}

	if _, _, err := detectBatchMedia(path); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(path, "ENQUEUED")); err == nil {
		return nil
	}

	// Create object_id + target root
	objectID, year, month, err := w.DB.NextObjectID(ctx)
	if err != nil {
		return err
	}

	objectRoot := filepath.Join(w.ArchiveRoot, "objects", fmt.Sprintf("%04d", year), fmt.Sprintf("%02d", month), objectID)

	// Dedupe by batch path (single insert). If already exists, skip.
	ok, err := w.DB.TryRegisterBatch(ctx, path, objectID)
	if err != nil || !ok {
		if err == nil {
			return fmt.Errorf("failed to register batch, method returned 'false'")
		}
		return err
	}

	// Insert object row
	if err := w.DB.InsertObject(ctx, objectID, objectRoot, year, month); err != nil {
		// If object insert fails, remove batch_ingests row so it can retry later (v1 best-effort)
		_, _ = w.DB.ExecContext(ctx, `DELETE FROM batch_ingests WHERE batch_path = ?`, path)
		return err
	}

	// Insert ingest job
	jobID := "JOB-" + objectID // v1; later: UUID
	payloadBytes, err := json.Marshal(EnqueuePayload{BatchPath: path})
	if err != nil {
		return err
	}

	payloadStr := string(payloadBytes)
	if err := w.DB.InsertJob(ctx, jobID, objectID, "ingest", &payloadStr); err != nil {
		// rollback object + batch mapping to allow retry
		_, _ = w.DB.ExecContext(ctx, `DELETE FROM objects WHERE object_id = ?`, objectID)
		_, _ = w.DB.ExecContext(ctx, `DELETE FROM batch_ingests WHERE batch_path = ?`, path)
		return err
	}

	if err := w.DB.SignalOrchestratorWakeup(ctx); err != nil {
		return err
	}

	err = w.DB.AddJobEvent(ctx, jobID, objectID, "info", "batch enqueued for ingest")
	if err != nil {
		return fmt.Errorf("failed to add job event: %w", err)
	}

	// Create ENQUEUED marker for humans
	err = os.WriteFile(filepath.Join(path, "ENQUEUED"), []byte(objectID+"\n"), 0644)

	if err != nil {
		return err
	}

	return nil
}

func (w *Watcher) ScanAndEnqueue(ctx context.Context) error {
	return filepath.WalkDir(w.IngestDrop, func(path string, de os.DirEntry, err error) error {
		er := w.Walker(ctx, path, de, err)
		if er != nil {
			fmt.Printf("an error occured during dir scanning: %v\n", er)
			return nil
		}
		return nil
	})
}

func listImageFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)

	if err != nil {
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		switch ext {
		case ".tif", ".tiff", ".png", ".jpg", ".jpeg":
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}

	sort.Strings(files)
	return files, nil
}
