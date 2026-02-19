package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ilova-bazis/osimi-archive/internal/db"
)

type DerivativesWorker struct {
	DB                *db.DB
	ImageProcessor    ImageProcessor
	AudioProcessor    AudioProcessor
	VideoProcessor    VideoProcessor
	DocumentProcessor DocumentProcessor
}

func (w *DerivativesWorker) RunOnce(ctx context.Context) (bool, error) {
	if w.ImageProcessor == nil || w.AudioProcessor == nil || w.VideoProcessor == nil || w.DocumentProcessor == nil {
		return false, fmt.Errorf("derivatives processors not configured")
	}
	job, err := w.DB.ClaimNextJob(ctx, "derivatives")
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	log.Printf("derivatives job started: %s", job.JobID)
	if err := w.processDerivatives(ctx, job.ObjectID, job.JobID); err != nil {
		log.Printf("derivatives job failed: %s", job.JobID)
		objectRoot, rootErr := w.DB.GetObjectRoot(ctx, job.ObjectID)
		if rootErr != nil {
			log.Printf("derivatives root error: %v", rootErr)
		} else {
			recordJobError(ctx, w.DB, objectRoot, job.ObjectID, job.JobID, "derivatives", err, false)
		}
		if markErr := w.DB.MarkJobFailed(ctx, job.JobID, err.Error()); markErr != nil {
			log.Printf("derivatives worker MarkJobFailed failed: %v", markErr)
		}
		if setErr := w.DB.SetObjectError(ctx, job.ObjectID, err.Error(), true); setErr != nil {
			log.Printf("derivatives worker SetObjectError failed: %v", setErr)
		}
		return true, nil
	}

	if err := w.DB.MarkJobSucceeded(ctx, job.JobID); err != nil {
		return true, err
	}
	log.Printf("derivatives job completed: %s", job.JobID)
	return true, nil
}

func (w *DerivativesWorker) processDerivatives(ctx context.Context, objectID, jobID string) error {
	if err := w.DB.SetObjectProcessingState(ctx, objectID, "derivatives_running", true); err != nil {
		return err
	}
	objectRoot, err := w.DB.GetObjectRoot(ctx, objectID)
	if err != nil {
		return err
	}
	if err := w.DB.AddJobEvent(ctx, jobID, objectID, "info", "derivatives started"); err != nil {
		log.Printf("derivatives worker AddJobEvent failed: %v", err)
	}
	if err := writeObjectEvent(objectRoot, objectID, jobID, "derivatives", "derivatives_started", "info", "derivatives started"); err != nil {
		log.Printf("derivatives worker event error: %v", err)
	}

	catalogPath := filepath.Join(objectRoot, "meta", "catalog.json")
	catalog, err := readCatalogForDerivatives(catalogPath)
	if err != nil {
		return err
	}

	itemKind := resolveDerivativesKind(catalog)
	if itemKind == "" {
		return fmt.Errorf("unable to resolve item_kind for derivatives")
	}

	switch itemKind {
	case "scanned_document":
		pagesDir := filepath.Join(objectRoot, "original", "pages")
		sourceFiles, err := listPageFiles(pagesDir)
		if err != nil {
			return err
		}
		if len(sourceFiles) == 0 {
			return fmt.Errorf("no pages found in %s", pagesDir)
		}
		if err := buildAccessPDF(ctx, objectRoot, sourceFiles, w.ImageProcessor); err != nil {
			return err
		}
	case "photo":
		pagesDir := filepath.Join(objectRoot, "original", "pages")
		sourceFiles, err := listPageFiles(pagesDir)
		if err != nil {
			return err
		}
		if len(sourceFiles) == 0 {
			return fmt.Errorf("no pages found in %s", pagesDir)
		}
		if err := buildPhotoDerivatives(ctx, objectRoot, sourceFiles, w.ImageProcessor); err != nil {
			return err
		}
	case "audio":
		audioDir := filepath.Join(objectRoot, "original", "audio")
		sourceFiles, err := listMediaFiles(audioDir, []string{".wav", ".flac", ".aiff", ".mp3", ".m4a"})
		if err != nil {
			return err
		}
		if err := buildAudioDerivatives(ctx, objectRoot, sourceFiles, w.AudioProcessor); err != nil {
			return err
		}
	case "video":
		videoDir := filepath.Join(objectRoot, "original", "video")
		sourceFiles, err := listMediaFiles(videoDir, []string{".mov", ".mp4", ".mkv", ".avi", ".webm"})
		if err != nil {
			return err
		}
		if err := buildVideoDerivatives(ctx, objectRoot, sourceFiles, w.VideoProcessor); err != nil {
			return err
		}
	case "document":
		docDir := filepath.Join(objectRoot, "original", "document")
		sourceFiles, err := listMediaFiles(docDir, []string{".pdf", ".doc", ".docx"})
		if err != nil {
			return err
		}
		if err := buildDocumentDerivatives(ctx, objectRoot, sourceFiles, w.DocumentProcessor); err != nil {
			return err
		}
	default:
		return fmt.Errorf("derivatives not supported for item_kind=%s", itemKind)
	}

	if err := w.DB.SetObjectProcessingState(ctx, objectID, "derivatives_done", true); err != nil {
		return err
	}
	if err := w.DB.AddJobEvent(ctx, jobID, objectID, "info", "derivatives completed"); err != nil {
		log.Printf("derivatives worker AddJobEvent failed: %v", err)
	}
	if err := writeObjectEvent(objectRoot, objectID, jobID, "derivatives", "derivatives_completed", "info", "derivatives completed"); err != nil {
		log.Printf("derivatives worker event error: %v", err)
	}
	return nil
}

type derivativesCatalog struct {
	ItemKind       string `json:"item_kind,omitempty"`
	Classification struct {
		Type string `json:"type,omitempty"`
	} `json:"classification,omitempty"`
}

func readCatalogForDerivatives(path string) (derivativesCatalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return derivativesCatalog{}, err
	}
	var c derivativesCatalog
	if err := json.Unmarshal(b, &c); err != nil {
		return derivativesCatalog{}, err
	}
	return c, nil
}

func resolveDerivativesKind(c derivativesCatalog) string {
	kind := strings.TrimSpace(c.ItemKind)
	if kind != "" {
		return kind
	}
	switch strings.TrimSpace(c.Classification.Type) {
	case "photo":
		return "photo"
	case "audio":
		return "audio"
	case "video":
		return "video"
	case "document":
		return "document"
	case "book", "book_chapter", "newspaper_article", "magazine_article", "letter", "speech", "interview":
		return "scanned_document"
	default:
		return ""
	}
}

func buildAccessPDF(ctx context.Context, objectRoot string, sourceFiles []string, processor ImageProcessor) error {
	outDir := filepath.Join(objectRoot, "derivatives", "access")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	output := filepath.Join(outDir, "reading_v1.pdf")

	if _, err := os.Stat(output); err == nil {
		return nil
	}

	tool, err := exec.LookPath("img2pdf")
	if err != nil {
		return fmt.Errorf("img2pdf not found in PATH")
	}

	normalizedDir := filepath.Join(objectRoot, "derivatives", "tmp", "normalized")
	if err := os.MkdirAll(normalizedDir, 0755); err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(filepath.Join(objectRoot, "derivatives", "tmp"))
	}()

	var normalized []string
	for i, src := range sourceFiles {
		name := fmt.Sprintf("page_%04d.png", i+1)
		outPath := filepath.Join(normalizedDir, name)
		if err := processor.Normalize(ctx, src, outPath); err != nil {
			return err
		}
		normalized = append(normalized, outPath)
	}

	args := append([]string{"-o", output}, normalized...)
	cmd := exec.CommandContext(ctx, tool, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("img2pdf failed: %s", msg)
	}
	return nil
}

func buildPhotoDerivatives(ctx context.Context, objectRoot string, sourceFiles []string, processor ImageProcessor) error {
	webDir := filepath.Join(objectRoot, "derivatives", "images", "web")
	thumbDir := filepath.Join(objectRoot, "derivatives", "images", "thumb")
	if err := os.MkdirAll(webDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return err
	}

	for _, src := range sourceFiles {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		webOut := filepath.Join(webDir, base+".jpg")
		thumbOut := filepath.Join(thumbDir, base+".jpg")
		if err := processor.Resize(ctx, src, webOut, 2000); err != nil {
			return err
		}
		if err := processor.Resize(ctx, src, thumbOut, 400); err != nil {
			return err
		}
	}
	return nil
}

func buildAudioDerivatives(ctx context.Context, objectRoot string, sourceFiles []string, processor AudioProcessor) error {
	outDir := filepath.Join(objectRoot, "derivatives", "audio")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	for _, src := range sourceFiles {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		output := filepath.Join(outDir, fmt.Sprintf("access_v1_%s.m4a", base))
		if err := processor.EncodeAccess(ctx, src, output); err != nil {
			return err
		}
	}
	return nil
}

func buildVideoDerivatives(ctx context.Context, objectRoot string, sourceFiles []string, processor VideoProcessor) error {
	outDir := filepath.Join(objectRoot, "derivatives", "video")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	for _, src := range sourceFiles {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		output := filepath.Join(outDir, fmt.Sprintf("access_v1_%s.mp4", base))
		if err := processor.EncodeAccess(ctx, src, output); err != nil {
			return err
		}
	}
	return nil
}

func buildDocumentDerivatives(ctx context.Context, objectRoot string, sourceFiles []string, processor DocumentProcessor) error {
	outDir := filepath.Join(objectRoot, "derivatives", "access")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	for _, src := range sourceFiles {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		output := filepath.Join(outDir, fmt.Sprintf("access_v1_%s.pdf", base))
		ext := strings.ToLower(filepath.Ext(src))
		if ext == ".pdf" {
			if _, err := copyFileAtomic(src, output); err != nil {
				return err
			}
			continue
		}
		if err := processor.ToPDF(ctx, src, output); err != nil {
			return err
		}
	}
	return nil
}

func listMediaFiles(dir string, exts []string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		allowed[ext] = struct{}{}
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := allowed[ext]; ok {
			files = append(files, filepath.Join(dir, name))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no media files found in %s", dir)
	}
	return files, nil
}
