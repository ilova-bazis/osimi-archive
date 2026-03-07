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

	"github.com/google/uuid"

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

func validateCatalogFile(batchPath, objectID string) (string, error) {
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

	itemKind := strings.TrimSpace(catalog.ItemKind)
	classificationType := strings.TrimSpace(catalog.Classification.Type)

	validItemKinds := map[string]bool{
		"photo":            true,
		"audio":            true,
		"video":            true,
		"scanned_document": true,
		"document":         true,
		"other":            true,
	}

	if itemKind != "" {
		if !validItemKinds[itemKind] {
			return "", fmt.Errorf("catalog.json item_kind '%s' is not supported", itemKind)
		}
	} else if classificationType == "" {
		return "", fmt.Errorf("catalog.json missing item_kind or classification.type")
	} else if itemKind == "document" || itemKind == "scanned_document" {
		if classificationType == "" {
			return "", fmt.Errorf("catalog.json classification.type is required for item_kind '%s'", itemKind)
		}
	}

	if catalog.Dates == nil {
		return "", fmt.Errorf("catalog.json missing dates block")
	}

	resolvedKind := resolveIngestItemKind(catalog)
	if resolvedKind == "" {
		return "", fmt.Errorf("catalog.json item_kind is not supported")
	}
	return resolvedKind, nil
}

func copyCatalogIfMissing(batchPath, objectRoot string) error {
	src := filepath.Join(batchPath, "catalog.json")
	dst := filepath.Join(objectRoot, "meta", "catalog.json")
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	formatted, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dst, formatted, 0644)
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

func postBatchIngestionFailed(ctx context.Context, d *db.DB, batchPath string, cause error) {
	if d == nil {
		return
	}
	ingestionID, _, err := readBatchLeaseInfo(batchPath)
	if err != nil {
		log.Printf("worker ingestion failed missing lease info: %v", err)
		return
	}
	message, err := readBatchErrorFile(batchPath)
	if err != nil || message == "" {
		if cause != nil {
			message = cause.Error()
		}
	}
	payload := map[string]any{
		"step":  "ingest",
		"error": message,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		log.Printf("worker ingestion failed marshal error: %v", err)
		return
	}
	ev := db.VPSEvent{
		EventID:     uuid.NewString(),
		IngestionID: ingestionID,
		EventType:   "INGESTION_FAILED",
		PayloadJSON: string(payloadJSON),
	}
	if err := d.EnqueueVPSEvent(ctx, ev); err != nil {
		log.Printf("worker ingestion failed enqueue error: %v", err)
		return
	}
	if err := d.SetIngestionLeaseState(ctx, ingestionID, "completed"); err != nil {
		log.Printf("worker ingestion failed set state error: %v", err)
	}
}

func postIngestionCompletion(ctx context.Context, d *db.DB, ingestionID, objectID string, ingestManifest any) error {
	if d == nil || ingestionID == "" {
		return nil
	}

	completedPayload := map[string]any{
		"step":        "ingest",
		"object_id":   objectID,
		"ingest_json": ingestManifest,
	}
	completedJSON, err := json.Marshal(completedPayload)
	if err != nil {
		return err
	}
	completedEv := db.VPSEvent{
		EventID:     uuid.NewString(),
		IngestionID: ingestionID,
		ObjectID:    sql.NullString{String: objectID, Valid: objectID != ""},
		EventType:   "INGESTION_COMPLETED",
		PayloadJSON: string(completedJSON),
	}
	if err := d.EnqueueVPSEvent(ctx, completedEv); err != nil {
		return err
	}

	createdPayload := map[string]any{
		"object_id": objectID,
	}
	createdJSON, err := json.Marshal(createdPayload)
	if err != nil {
		return err
	}
	createdEv := db.VPSEvent{
		EventID:     uuid.NewString(),
		IngestionID: ingestionID,
		ObjectID:    sql.NullString{String: objectID, Valid: objectID != ""},
		EventType:   "OBJECT_CREATED",
		PayloadJSON: string(createdJSON),
	}
	if err := d.EnqueueVPSEvent(ctx, createdEv); err != nil {
		return err
	}

	if err := d.EnqueueBackendObjectTask(ctx, objectID, "available_files_snapshot", "ingest_completed"); err != nil {
		return err
	}

	if err := d.SetIngestionLeaseState(ctx, ingestionID, "completed"); err != nil {
		return err
	}

	return nil
}

func readBatchLeaseInfo(batchPath string) (string, string, error) {
	ingestionPath := filepath.Join(batchPath, "INGESTION_ID")
	leasePath := filepath.Join(batchPath, "LEASE_TOKEN")
	ingestionBytes, err := os.ReadFile(ingestionPath)
	if err != nil {
		return "", "", err
	}
	leaseBytes, err := os.ReadFile(leasePath)
	if err != nil {
		return "", "", err
	}
	ingestionID := strings.TrimSpace(string(ingestionBytes))
	leaseToken := strings.TrimSpace(string(leaseBytes))
	if ingestionID == "" || leaseToken == "" {
		return "", "", fmt.Errorf("ingestion id or lease token is empty")
	}
	return ingestionID, leaseToken, nil
}

func readBatchErrorFile(batchPath string) (string, error) {
	path := filepath.Join(batchPath, "ERROR")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func resolveIngestItemKind(catalog ingestCatalog) string {
	kind := strings.TrimSpace(catalog.ItemKind)
	if kind != "" {
		return kind
	}
	kind = strings.TrimSpace(catalog.Classification.Type)
	switch kind {
	case "image":
		return "photo"
	case "document":
		return "document"
	case "newspaper_article", "magazine_article", "book_chapter", "book", "letter", "speech", "interview", "report", "manuscript":
		return "scanned_document"
	case "other":
		return "other"
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
		"INGESTION_ID": {},
		"LEASE_TOKEN":  {},
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

type ingestRouting struct {
	destSubdir string
	prefix     string
	usePages   bool
}

func resolveIngestRouting(itemKind string, mediaType mediaKind) (ingestRouting, error) {
	switch itemKind {
	case "photo":
		if mediaType != mediaImage {
			return ingestRouting{}, fmt.Errorf("item_kind 'photo' requires image media, got %s", mediaType)
		}
		return ingestRouting{destSubdir: "photos", prefix: "photo_", usePages: false}, nil
	case "scanned_document":
		if mediaType != mediaImage {
			return ingestRouting{}, fmt.Errorf("item_kind 'scanned_document' requires image media, got %s", mediaType)
		}
		return ingestRouting{destSubdir: "pages", prefix: "page_", usePages: true}, nil
	case "document":
		if mediaType != mediaDocument {
			return ingestRouting{}, fmt.Errorf("item_kind 'document' requires document media, got %s", mediaType)
		}
		return ingestRouting{destSubdir: "document", prefix: "document_", usePages: false}, nil
	case "audio":
		if mediaType != mediaAudio {
			return ingestRouting{}, fmt.Errorf("item_kind 'audio' requires audio media, got %s", mediaType)
		}
		return ingestRouting{destSubdir: "audio", prefix: "audio_", usePages: false}, nil
	case "video":
		if mediaType != mediaVideo {
			return ingestRouting{}, fmt.Errorf("item_kind 'video' requires video media, got %s", mediaType)
		}
		return ingestRouting{destSubdir: "video", prefix: "video_", usePages: false}, nil
	case "other":
		return ingestRouting{destSubdir: "other", prefix: "file_", usePages: false}, nil
	default:
		return ingestRouting{}, fmt.Errorf("unsupported item_kind: %s", itemKind)
	}
}

func matchesItemKind(itemKind string, kind mediaKind) bool {
	switch itemKind {
	case "photo":
		return kind == mediaImage
	case "scanned_document":
		return kind == mediaImage
	case "audio":
		return kind == mediaAudio
	case "video":
		return kind == mediaVideo
	case "document":
		return kind == mediaDocument
	case "other":
		return true
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

	itemKind, err := validateCatalogFile(batchPath, objectID)
	if err != nil {
		appendBatchError(batchPath, err)
		postBatchIngestionFailed(ctx, w.DB, batchPath, err)
		return err
	}

	// Ensure folder structure
	if err := ensureObjectDirs(objectRoot); err != nil {
		appendBatchError(batchPath, err)
		return err
	}

	// Validate + copy catalog.json before heavy work
	if err := copyCatalogIfMissing(batchPath, objectRoot); err != nil {
		appendBatchError(batchPath, err)
		postBatchIngestionFailed(ctx, w.DB, batchPath, err)
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
		postBatchIngestionFailed(ctx, w.DB, batchPath, err)
		return err
	}

	// Copy into original/ with deterministic naming using routing
	route, err := resolveIngestRouting(itemKind, mediaType)
	if err != nil {
		appendBatchError(batchPath, err)
		postBatchIngestionFailed(ctx, w.DB, batchPath, err)
		return err
	}

	type pageEntry struct {
		PageNumber     int    `json:"page_number"`
		Filename       string `json:"filename"`
		SourceFilename string `json:"source_filename"`
		MimeType       string `json:"mime_type"`
		Bytes          int64  `json:"bytes"`
	}

	originalCount := 0
	var manifest ingestManifestV1
	dstDir := filepath.Join(objectRoot, "original", route.destSubdir)

	if route.usePages {
		pages := make([]pageEntry, 0, len(batchFiles))
		for i, src := range batchFiles {
			pageNum := i + 1
			ext := strings.ToLower(filepath.Ext(src))
			dstName := fmt.Sprintf("%s%04d%s", route.prefix, pageNum, ext)
			dstPath := filepath.Join(dstDir, dstName)
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
	} else {
		entries, err := copyMediaFiles(batchFiles, dstDir, route.prefix)
		if err != nil {
			appendBatchError(batchPath, err)
			return err
		}
		originalCount = len(entries)
		manifest = buildIngestManifestV1Files(objectID, batchPath, route.destSubdir, route.prefix+"%04d", entries, len(entries))
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

	ingestionID, _, err := readBatchLeaseInfo(batchPath)
	if err != nil {
		appendBatchError(batchPath, err)
		return fmt.Errorf("read batch lease info: %w", err)
	}

	if err := postIngestionCompletion(ctx, w.DB, ingestionID, objectID, manifest); err != nil {
		appendBatchError(batchPath, err)
		return fmt.Errorf("post ingestion completion: %w", err)
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
		filepath.Join(objectRoot, "original", "photos"),
		filepath.Join(objectRoot, "original", "audio"),
		filepath.Join(objectRoot, "original", "video"),
		filepath.Join(objectRoot, "original", "document"),
		filepath.Join(objectRoot, "original", "other"),
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
