package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ilova-bazis/osimi-archive/internal/db"
)

type OCRWorker struct {
	DB *db.DB

	DefaultLang string
	OCRVersion  string
}

type ocrPayload struct {
	Language string `json:"language,omitempty"`
}

func (w *OCRWorker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.DB.ClaimNextJob(ctx, "ocr")
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	log.Printf("ocr job started: %s", job.JobID)
	batch, lang, err := w.parseOCRPayload(job.PayloadJSON)
	if err != nil {
		_ = w.DB.MarkJobFailed(ctx, job.JobID, err.Error())
		_ = w.DB.SetObjectError(ctx, job.ObjectID, err.Error(), true)
		return true, nil
	}

	if lang == "" {
		lang = w.DefaultLang
	}
	if w.OCRVersion == "" {
		w.OCRVersion = "v1"
	}

	if err := w.processOCR(ctx, job.ObjectID, job.JobID, lang, batch); err != nil {
		log.Printf("ocr job failed: %s", job.JobID)
		objectRoot, rootErr := w.DB.GetObjectRoot(ctx, job.ObjectID)
		if rootErr != nil {
			log.Printf("ocr root error: %v", rootErr)
		} else {
			recordJobError(ctx, w.DB, objectRoot, job.ObjectID, job.JobID, "ocr", err, false)
		}
		_ = w.DB.MarkJobFailed(ctx, job.JobID, err.Error())
		_ = w.DB.SetObjectError(ctx, job.ObjectID, err.Error(), true)
		return true, nil
	}

	if err := w.DB.MarkJobSucceeded(ctx, job.JobID); err != nil {
		return true, err
	}
	log.Printf("ocr job completed: %s", job.JobID)
	return true, nil
}

func (w *OCRWorker) parseOCRPayload(payload sql.NullString) (ocrPayload, string, error) {
	var p ocrPayload
	if payload.Valid && strings.TrimSpace(payload.String) != "" {
		if err := json.Unmarshal([]byte(payload.String), &p); err != nil {
			return ocrPayload{}, "", fmt.Errorf("invalid ocr payload_json: %w", err)
		}
	}
	return p, p.Language, nil
}

func (w *OCRWorker) processOCR(ctx context.Context, objectID, jobID, lang string, payload ocrPayload) error {
	if err := w.DB.SetObjectProcessingState(ctx, objectID, "ocr_running", true); err != nil {
		return err
	}
	objectRoot, err := w.DB.GetObjectRoot(ctx, objectID)
	if err != nil {
		return err
	}
	if err := w.DB.AddJobEvent(ctx, jobID, objectID, "info", "ocr_text started"); err != nil {
		log.Printf("ocr worker AddJobEvent failed: %v", err)
	}
	if err := writeObjectEvent(objectRoot, objectID, jobID, "ocr", "ocr_started", "info", "ocr started"); err != nil {
		log.Printf("ocr worker event error: %v", err)
	}

	pagesDir := filepath.Join(objectRoot, "original", "pages")
	ocrDir := filepath.Join(objectRoot, "ocr")
	if err := os.MkdirAll(ocrDir, 0755); err != nil {
		return err
	}

	pageFiles, err := listPageFiles(pagesDir)
	if err != nil {
		return err
	}

	if len(pageFiles) == 0 {
		return fmt.Errorf("no pages found in %s", pagesDir)
	}

	for i, pagePath := range pageFiles {
		pageNum := i + 1
		txtName := fmt.Sprintf("page_%04d.txt", pageNum)
		txtPath := filepath.Join(ocrDir, txtName)

		// Run tesseract → stdout
		text, err := runTesseractToText(ctx, pagePath, lang)
		if err != nil {
			return fmt.Errorf("tesseract page %d (%s): %w", pageNum, filepath.Base(pagePath), err)
		}

		// Write file atomically
		if err := writeFileAtomic(txtPath, []byte(text), 0644); err != nil {
			return fmt.Errorf("write ocr text %s: %w", txtName, err)
		}

		// Insert into DB (FTS triggers populate index)
		if err := w.DB.UpsertPageText(ctx, objectID, pageNum, w.OCRVersion, text); err != nil {
			return fmt.Errorf("db upsert page_text p=%d: %w", pageNum, err)
		}
	}
	// Mark OCR complete
	if err := w.DB.MarkObjectOCRComplete(ctx, objectID); err != nil {
		return err
	}

	markerPath := filepath.Join(objectRoot, "ocr", "OCR_DONE")
	if err := writeFileAtomic(markerPath, []byte("done\n"), 0644); err != nil {
		return fmt.Errorf("write OCR_DONE: %w", err)
	}

	if err := w.DB.AddJobEvent(ctx, jobID, objectID, "info", "ocr_text completed"); err != nil {
		log.Printf("ocr worker AddJobEvent failed: %v", err)
	}
	if err := writeObjectEvent(objectRoot, objectID, jobID, "ocr", "ocr_completed", "info", "ocr completed"); err != nil {
		log.Printf("ocr worker event error: %v", err)
	}
	return nil
}

func listPageFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// our ingest naming is page_0001.ext; keep it simple:
		if !strings.HasPrefix(name, "page_") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".tif", ".tiff", ".png", ".jpg", ".jpeg":
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Strings(files)
	return files, nil
}

func runTesseractToText(ctx context.Context, imagePath, lang string) (string, error) {
	// tesseract <image> stdout -l <lang>
	cmd := exec.CommandContext(ctx, "tesseract", imagePath, "stdout", "-l", lang)
	var out bytes.Buffer
	var errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}

	// normalize line endings lightly
	s := out.String()
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return s, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	// Best-effort fsync directory would be ideal later; v1 keep simple
	return os.Rename(tmp, path)
}
