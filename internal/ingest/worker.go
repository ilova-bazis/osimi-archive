package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ilova-bazis/osimi-archive/internal/db"
)

type Worker struct {
	DB *db.DB
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.DB.ClaimNextJob(ctx, "ingest")
	if err != nil {
		return false, err
	}

	if job == nil {
		return false, nil
	}

	batchPath, err := parseBatchPath(job.PayloadJSON)
	if err != nil {
		if markErr := w.DB.MarkJobFailed(ctx, job.JobID, err.Error()); markErr != nil {
			log.Printf("worker RunOnce MarkJobFailed failed: %v", markErr)
		}
		if setErr := w.DB.SetObjectError(ctx, job.ObjectID, err.Error(), true); setErr != nil {
			log.Printf("worker RunOnce SetObjectError failed: %v", setErr)
		}
		return true, nil
	}

	// Processing ingested job
	if err := w.processIngest(ctx, job.ObjectID, job.JobID, batchPath); err != nil {
		objectRoot, rootErr := w.DB.GetObjectRoot(ctx, job.ObjectID)
		if rootErr != nil {
			log.Printf("worker RunOnce root error: %v", rootErr)
		} else {
			recordJobError(ctx, w.DB, objectRoot, job.ObjectID, job.JobID, "ingest", err, false)
		}
		if markErr := w.DB.MarkJobFailed(ctx, job.JobID, err.Error()); markErr != nil {
			log.Printf("worker RunOnce MarkJobFailed failed: %v", markErr)
		}
		if setErr := w.DB.SetObjectError(ctx, job.ObjectID, err.Error(), true); setErr != nil {
			log.Printf("worker RunOnce SetObjectError failed: %v", setErr)
		}
		return true, nil
	}

	if err := w.DB.MarkJobSucceeded(ctx, job.JobID); err != nil {
		return true, err
	}

	return true, nil
}

func parseBatchPath(payload sql.NullString) (string, error) {
	if !payload.Valid || strings.TrimSpace(payload.String) == "" {
		return "", fmt.Errorf("job payload_json is missing")
	}
	var p EnqueuePayload
	if err := json.Unmarshal([]byte(payload.String), &p); err != nil {
		return "", fmt.Errorf("invalid payload_json: %w", err)
	}
	if strings.TrimSpace(p.BatchPath) == "" {
		return "", fmt.Errorf("batch_path is missing in payload")
	}
	return p.BatchPath, nil
}

type ingestCatalog struct {
	SchemaVersion string `json:"schema_version"`
	ObjectID      string `json:"object_id"`
	UpdatedAt     string `json:"updated_at"`
	ItemKind      string `json:"item_kind,omitempty"`
	Access        any    `json:"access"`
	Title         struct {
		Primary string `json:"primary"`
	} `json:"title"`
	Classification struct {
		Type     string `json:"type"`
		Language string `json:"language"`
	} `json:"classification"`
	Dates any `json:"dates"`
}

type mediaKind string

const (
	mediaImage    mediaKind = "image"
	mediaAudio    mediaKind = "audio"
	mediaVideo    mediaKind = "video"
	mediaDocument mediaKind = "document"
)

type fileEntry struct {
	Filename       string `json:"filename"`
	SourceFilename string `json:"source_filename"`
	MimeType       string `json:"mime_type"`
	Bytes          int64  `json:"bytes"`
}

func copyCatalogIfMissing(batchPath, objectRoot, objectID string) (string, error) {
	src := filepath.Join(batchPath, "catalog.json")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("catalog.json is missing")
		}
		return "", err
	}
	var catalog ingestCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return "", fmt.Errorf("invalid catalog.json: %w", err)
	}
	if strings.TrimSpace(catalog.SchemaVersion) == "" {
		return "", fmt.Errorf("catalog.json missing schema_version")
	}
	if strings.TrimSpace(catalog.ObjectID) != "" && catalog.ObjectID != objectID {
		return "", fmt.Errorf("catalog.json object_id %s does not match %s", catalog.ObjectID, objectID)
	}
	if strings.TrimSpace(catalog.UpdatedAt) == "" {
		return "", fmt.Errorf("catalog.json missing updated_at")
	}
	if catalog.Access == nil {
		return "", fmt.Errorf("catalog.json missing access block")
	}
	if strings.TrimSpace(catalog.Title.Primary) == "" {
		return "", fmt.Errorf("catalog.json missing title.primary")
	}
	if strings.TrimSpace(catalog.ItemKind) == "" && strings.TrimSpace(catalog.Classification.Type) == "" {
		return "", fmt.Errorf("catalog.json missing item_kind or classification.type")
	}
	if catalog.Dates == nil {
		return "", fmt.Errorf("catalog.json missing dates block")
	}

	itemKind := resolveIngestItemKind(catalog)
	if itemKind == "" {
		return "", fmt.Errorf("catalog.json item_kind is not supported")
	}

	dst := filepath.Join(objectRoot, "meta", "catalog.json")
	if _, err := os.Stat(dst); err == nil {
		return itemKind, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	_, err = copyFileAtomic(src, dst)
	return itemKind, err
}

func appendBatchError(batchPath string, err error) {
	if err == nil {
		return
	}
	path := filepath.Join(batchPath, "ERROR")
	line := fmt.Sprintf("%s %v\n", time.Now().UTC().Format(time.RFC3339), err)
	f, fileErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if fileErr != nil {
		log.Printf("worker append ERROR failed: %v", fileErr)
		return
	}
	defer f.Close()
	if _, fileErr := f.WriteString(line); fileErr != nil {
		log.Printf("worker append ERROR write failed: %v", fileErr)
	}
}

func resolveIngestItemKind(catalog ingestCatalog) string {
	kind := strings.TrimSpace(catalog.ItemKind)
	if kind != "" {
		return kind
	}
	kind = strings.TrimSpace(catalog.Classification.Type)
	switch kind {
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

func detectBatchMedia(batchPath string) (mediaKind, []string, error) {
	entries, err := os.ReadDir(batchPath)
	if err != nil {
		return "", nil, err
	}
	extMap := map[string]mediaKind{
		".png":  mediaImage,
		".jpg":  mediaImage,
		".jpeg": mediaImage,
		".tif":  mediaImage,
		".tiff": mediaImage,
		".wav":  mediaAudio,
		".flac": mediaAudio,
		".mp3":  mediaAudio,
		".m4a":  mediaAudio,
		".aiff": mediaAudio,
		".mov":  mediaVideo,
		".mp4":  mediaVideo,
		".mkv":  mediaVideo,
		".avi":  mediaVideo,
		".webm": mediaVideo,
		".pdf":  mediaDocument,
		".doc":  mediaDocument,
		".docx": mediaDocument,
	}
	ignored := map[string]struct{}{
		"catalog.json": {},
		"DONE":         {},
		"ENQUEUED":     {},
		"ERROR":        {},
	}
	var kinds []mediaKind
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, ok := ignored[name]; ok {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		kind, ok := extMap[ext]
		if !ok {
			return "", nil, fmt.Errorf("unsupported file in batch: %s", name)
		}
		files = append(files, filepath.Join(batchPath, name))
		if len(kinds) == 0 || kinds[0] != kind {
			kinds = append(kinds, kind)
		}
	}
	if len(files) == 0 {
		return "", nil, fmt.Errorf("batch has no supported media files")
	}
	if len(kinds) > 1 {
		return "", nil, fmt.Errorf("batch contains mixed media types")
	}
	sort.Strings(files)
	return kinds[0], files, nil
}

func matchesItemKind(itemKind string, kind mediaKind) bool {
	switch itemKind {
	case "scanned_document", "photo":
		return kind == mediaImage
	case "audio":
		return kind == mediaAudio
	case "video":
		return kind == mediaVideo
	case "document":
		return kind == mediaDocument
	default:
		return false
	}
}

func copyMediaFiles(files []string, dstDir, prefix string) ([]fileEntry, error) {
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil, err
	}
	entries := make([]fileEntry, 0, len(files))
	for i, src := range files {
		index := i + 1
		ext := strings.ToLower(filepath.Ext(src))
		dstName := fmt.Sprintf("%s_%04d%s", prefix, index, ext)
		dstPath := filepath.Join(dstDir, dstName)
		n, err := copyFileAtomic(src, dstPath)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fileEntry{
			Filename:       dstName,
			SourceFilename: filepath.Base(src),
			MimeType:       mimeFromExt(ext),
			Bytes:          n,
		})
	}
	return entries, nil
}

func (w *Worker) processIngest(ctx context.Context, objectID, jobID, batchPath string) error {

	objectRoot, err := w.DB.GetObjectRoot(ctx, objectID)

	if err != nil {
		appendBatchError(batchPath, err)
		return err
	}

	// Ensure folder structure
	if err := ensureObjectDirs(objectRoot); err != nil {
		appendBatchError(batchPath, err)
		return err
	}

	// Validate + copy catalog.json before heavy work
	itemKind, err := copyCatalogIfMissing(batchPath, objectRoot, objectID)
	if err != nil {
		appendBatchError(batchPath, err)
		return err
	}

	// Move object into ingesting state
	if err := w.DB.SetObjectProcessingState(ctx, objectID, "ingesting", true); err != nil {
		appendBatchError(batchPath, err)
		return err
	}
	if err := w.DB.AddJobEvent(ctx, jobID, objectID, "info", "ingest started"); err != nil {
		log.Printf("worker processIngest AddJobEvent failed: %v", err)
	}
	if err := writeObjectEvent(objectRoot, objectID, jobID, "ingest", "ingest_started", "info", "ingest started"); err != nil {
		log.Printf("worker processIngest event error: %v", err)
	}

	mediaType, batchFiles, err := detectBatchMedia(batchPath)
	if err != nil {
		appendBatchError(batchPath, err)
		return err
	}
	if !matchesItemKind(itemKind, mediaType) {
		err := fmt.Errorf("batch media type %s does not match item_kind %s", mediaType, itemKind)
		appendBatchError(batchPath, err)
		return err
	}

	// Copy into original/ with deterministic naming
	type pageEntry struct {
		PageNumber     int    `json:"page_number"`
		Filename       string `json:"filename"`
		SourceFilename string `json:"source_filename"`
		MimeType       string `json:"mime_type"`
		Bytes          int64  `json:"bytes"`
	}

	originalCount := 0
	var manifest ingestManifestV1
	switch mediaType {
	case mediaImage:
		pages := make([]pageEntry, 0, len(batchFiles))
		dstPagesDir := filepath.Join(objectRoot, "original", "pages")
		for i, src := range batchFiles {
			pageNum := i + 1
			ext := strings.ToLower(filepath.Ext(src))
			dstName := fmt.Sprintf("page_%04d%s", pageNum, ext)
			dstPath := filepath.Join(dstPagesDir, dstName)
			n, err := copyFileAtomic(src, dstPath)
			if err != nil {
				err = fmt.Errorf("copy page %d: %w", pageNum, err)
				appendBatchError(batchPath, err)
				return err
			}
			pages = append(pages, pageEntry{
				PageNumber:     pageNum,
				Filename:       dstName,
				SourceFilename: filepath.Base(src),
				MimeType:       mimeFromExt(ext),
				Bytes:          n,
			})
		}
		originalCount = len(pages)
		manifest = buildIngestManifestV1(objectID, batchPath, pages, len(pages))
	case mediaAudio:
		entries, err := copyMediaFiles(batchFiles, filepath.Join(objectRoot, "original", "audio"), "audio")
		if err != nil {
			appendBatchError(batchPath, err)
			return err
		}
		originalCount = len(entries)
		manifest = buildIngestManifestV1Files(objectID, batchPath, "audio", "audio_%04d", entries, len(entries))
	case mediaVideo:
		entries, err := copyMediaFiles(batchFiles, filepath.Join(objectRoot, "original", "video"), "video")
		if err != nil {
			appendBatchError(batchPath, err)
			return err
		}
		originalCount = len(entries)
		manifest = buildIngestManifestV1Files(objectID, batchPath, "video", "video_%04d", entries, len(entries))
	case mediaDocument:
		entries, err := copyMediaFiles(batchFiles, filepath.Join(objectRoot, "original", "document"), "document")
		if err != nil {
			appendBatchError(batchPath, err)
			return err
		}
		originalCount = len(entries)
		manifest = buildIngestManifestV1Files(objectID, batchPath, "document", "document_%04d", entries, len(entries))
	default:
		return fmt.Errorf("unsupported media type %s", mediaType)
	}

	ingestPath := filepath.Join(objectRoot, "meta", "ingest.json")
	if err := writeJSONAtomic(ingestPath, manifest); err != nil {
		err = fmt.Errorf("write ingest.json: %w", err)
		appendBatchError(batchPath, err)
		return err
	}

	// Write checksums/sha256.txt (relative paths from object root)
	shaPath := filepath.Join(objectRoot, "checksums", "sha256.txt")
	if err := writeSHA256Sums(objectRoot, shaPath, []string{
		filepath.Join(objectRoot, "original"),
		filepath.Join(objectRoot, "meta", "ingest.json"),
	}); err != nil {
		err = fmt.Errorf("write sha256.txt: %w", err)
		appendBatchError(batchPath, err)
		return err
	}

	// Update DB: ingested + page_count
	if err := w.DB.MarkObjectIngested(ctx, objectID, originalCount); err != nil {
		appendBatchError(batchPath, err)
		return err
	}

	// Mark batch imported (rename, do not delete)
	if err := markBatchImported(batchPath, objectID); err != nil {
		// Non-fatal: ingest already succeeded
		if eventErr := w.DB.AddJobEvent(ctx, jobID, objectID, "warn", fmt.Sprintf("batch rename failed: %v", err)); eventErr != nil {
			log.Printf("worker processIngest AddJobEvent failed: %v", eventErr)
		}
	}

	if err := w.DB.AddJobEvent(ctx, jobID, objectID, "info", "ingest completed"); err != nil {
		log.Printf("worker processIngest AddJobEvent failed: %v", err)
	}
	if err := writeObjectEvent(objectRoot, objectID, jobID, "ingest", "ingest_completed", "info", "ingest completed"); err != nil {
		log.Printf("worker processIngest event error: %v", err)
	}
	return nil
}

func ensureObjectDirs(objectRoot string) error {
	dirs := []string{
		filepath.Join(objectRoot, "meta"),
		filepath.Join(objectRoot, "original", "pages"),
		filepath.Join(objectRoot, "original", "audio"),
		filepath.Join(objectRoot, "original", "video"),
		filepath.Join(objectRoot, "original", "document"),
		filepath.Join(objectRoot, "derivatives"),
		filepath.Join(objectRoot, "ocr"),
		filepath.Join(objectRoot, "checksums"),
		filepath.Join(objectRoot, "events"),
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}

func copyFileAtomic(src, dst string) (int64, error) {
	// Write to temp then rename so partial files don't appear
	tmp := dst + ".tmp"

	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()

	n, err := io.Copy(out, in)
	if err != nil {
		return 0, err
	}
	if err := out.Sync(); err != nil {
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return 0, err
	}
	return n, nil
}

func writeJSONAtomic(path string, v any) error {
	tmp := path + ".tmp"
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeSHA256Sums(objectRoot, outFile string, targets []string) error {
	// Collect files under target dirs + individual files, then write sha256 lines with RELATIVE paths.
	var paths []string

	for _, t := range targets {
		fi, err := os.Stat(t)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			err := filepath.WalkDir(t, func(p string, de os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if de.IsDir() {
					return nil
				}
				paths = append(paths, p)
				return nil
			})
			if err != nil {
				return err
			}
		} else {
			paths = append(paths, t)
		}
	}

	sort.Strings(paths)

	var sb strings.Builder
	for _, p := range paths {
		sum, err := sha256File(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(objectRoot, p)
		if err != nil {
			return err
		}
		sb.WriteString(sum)
		sb.WriteString("  ")
		sb.WriteString(filepath.ToSlash(rel))
		sb.WriteString("\n")
	}

	tmp := outFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, outFile)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func mimeFromExt(ext string) string {
	switch ext {
	case ".tif", ".tiff":
		return "image/tiff"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".aiff":
		return "audio/aiff"
	case ".mov":
		return "video/quicktime"
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".webm":
		return "video/webm"
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return "application/octet-stream"
	}
}

func markBatchImported(batchPath, objectID string) error {
	parent := filepath.Dir(batchPath)
	base := filepath.Base(batchPath)
	newName := base + "__IMPORTED__" + objectID
	return os.Rename(batchPath, filepath.Join(parent, newName))
}
