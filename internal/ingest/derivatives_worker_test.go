package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ilova-bazis/osimi-archive/internal/db"
)

type testImageProcessor struct{}

func (p *testImageProcessor) Normalize(_ context.Context, _ string, dst string) error {
	return os.WriteFile(dst, []byte("normalized"), 0o644)
}

func (p *testImageProcessor) Resize(_ context.Context, _ string, dst string, _ int) error {
	return os.WriteFile(dst, []byte("resized"), 0o644)
}

func (p *testImageProcessor) Name() string { return "test-image" }

type recordingImageProcessor struct {
	resizedSources []string
	resizeTargets  []string
}

func (p *recordingImageProcessor) Normalize(_ context.Context, _ string, dst string) error {
	return os.WriteFile(dst, []byte("normalized"), 0o644)
}

func (p *recordingImageProcessor) Resize(_ context.Context, src, dst string, _ int) error {
	p.resizedSources = append(p.resizedSources, src)
	p.resizeTargets = append(p.resizeTargets, dst)
	return os.WriteFile(dst, []byte("resized"), 0o644)
}

func (p *recordingImageProcessor) Name() string { return "recording-image" }

type testAudioProcessor struct{}

func (p *testAudioProcessor) EncodeAccess(_ context.Context, _, dst string) error {
	return os.WriteFile(dst, []byte("audio"), 0o644)
}

func (p *testAudioProcessor) Name() string { return "test-audio" }

type testVideoProcessor struct{}

func (p *testVideoProcessor) EncodeAccess(_ context.Context, _, dst string) error {
	return os.WriteFile(dst, []byte("video"), 0o644)
}

func (p *testVideoProcessor) Name() string { return "test-video" }

type testDocumentProcessor struct{}

func (p *testDocumentProcessor) ToPDF(_ context.Context, _, dst string) error {
	return os.WriteFile(dst, []byte("pdf"), 0o644)
}

func (p *testDocumentProcessor) Name() string { return "test-document" }

type testSearchablePDFProcessor struct {
	called int
	langs  []string
}

func (p *testSearchablePDFProcessor) AddTextLayer(_ context.Context, src, dst, lang string) error {
	p.called++
	p.langs = append(p.langs, lang)
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, append(data, []byte("-ocr")...), 0o644)
}

func (p *testSearchablePDFProcessor) Name() string { return "test-searchable-pdf" }

func openDerivativesTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return d
}

func installFakeImg2PDF(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "img2pdf")
	contents := "#!/bin/sh\nout=\"\"\nargs_file=\"$IMG2PDF_ARGS_FILE\"\nargs=\"\"\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"-o\" ]; then\n    shift\n    out=\"$1\"\n  else\n    args=\"$args$1\n\"\n  fi\n  shift\ndone\nif [ -n \"$args_file\" ]; then\n  printf '%s' \"$args\" > \"$args_file\"\nfi\nprintf 'pdf' > \"$out\"\n"
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fake img2pdf: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func seedDerivativeObject(t *testing.T, d *db.DB, objectRoot string) string {
	t.Helper()
	ctx := context.Background()
	objectID := "OBJ-20260318-ABC123"
	if err := d.InsertObject(ctx, objectID, objectRoot, 2026, 3); err != nil {
		t.Fatalf("insert object: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.ExecContext(ctx, `
		INSERT INTO jobs (job_id, object_id, job_type, state, queued_at)
		VALUES (?, ?, 'derivatives', 'running', ?)
	`, "JOB-1", objectID, now); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	return objectID
}

func writeScannedDocumentFixture(t *testing.T, objectRoot string, language string) {
	t.Helper()
	for _, dir := range []string{
		filepath.Join(objectRoot, "meta"),
		filepath.Join(objectRoot, "original", "pages"),
		filepath.Join(objectRoot, "derivatives", "access"),
		filepath.Join(objectRoot, "events"),
		filepath.Join(objectRoot, "ocr"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	catalog := `{
	  "schema_version": "1.0",
	  "updated_at": "2026-03-18T00:00:00Z",
	  "item_kind": "scanned_document",
	  "access": {},
	  "title": {"primary": "Doc"},
	  "classification": {"type": "book", "language": "` + language + `"},
	  "dates": {}
	}`
	if err := os.WriteFile(filepath.Join(objectRoot, "meta", "catalog.json"), []byte(catalog), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	if err := os.WriteFile(filepath.Join(objectRoot, "original", "pages", "page_0001.tif"), []byte("page"), 0o644); err != nil {
		t.Fatalf("write page: %v", err)
	}
}

func writeScannedDocumentPage(t *testing.T, objectRoot, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(objectRoot, "original", "pages", name), []byte(name), 0o644); err != nil {
		t.Fatalf("write page %s: %v", name, err)
	}
}

func objectProcessingState(t *testing.T, d *db.DB, objectID string) string {
	t.Helper()
	var state string
	if err := d.QueryRowContext(context.Background(), `SELECT processing_state FROM objects WHERE object_id = ?`, objectID).Scan(&state); err != nil {
		t.Fatalf("query processing state: %v", err)
	}
	return state
}

func orchestratorWakeupExists(t *testing.T, d *db.DB) bool {
	t.Helper()
	var count int
	if err := d.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM orchestrator_wakeup WHERE id = 1`).Scan(&count); err != nil {
		t.Fatalf("query orchestrator wakeup: %v", err)
	}
	return count == 1
}

func TestProcessDerivativesScannedDocumentWaitsForOCRBeforeSearchablePDF(t *testing.T) {
	installFakeImg2PDF(t)
	d := openDerivativesTestDB(t)
	objectRoot := t.TempDir()
	writeScannedDocumentFixture(t, objectRoot, "eng")
	objectID := seedDerivativeObject(t, d, objectRoot)
	searchable := &testSearchablePDFProcessor{}
	w := &DerivativesWorker{
		DB:                d,
		ImageProcessor:    &testImageProcessor{},
		AudioProcessor:    &testAudioProcessor{},
		VideoProcessor:    &testVideoProcessor{},
		DocumentProcessor: &testDocumentProcessor{},
		SearchablePDF:     searchable,
	}

	if err := w.processDerivatives(context.Background(), objectID, "JOB-1"); err != nil {
		t.Fatalf("process derivatives: %v", err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "derivatives", "access", "reading_v1.pdf")); err != nil {
		t.Fatalf("expected reading_v1.pdf: %v", err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "derivatives", "images", "thumb", "thumb.jpg")); err != nil {
		t.Fatalf("expected thumb.jpg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "derivatives", "access", "reading_ocr_v1.pdf")); !os.IsNotExist(err) {
		t.Fatalf("expected reading_ocr_v1.pdf to be absent, got err=%v", err)
	}
	if searchable.called != 0 {
		t.Fatalf("expected searchable pdf processor not to run before OCR")
	}
	if got := objectProcessingState(t, d, objectID); got != "ingested" {
		t.Fatalf("expected processing state ingested, got %s", got)
	}
}

func TestProcessDerivativesScannedDocumentBuildsSearchablePDFWithOCRLanguage(t *testing.T) {
	installFakeImg2PDF(t)
	d := openDerivativesTestDB(t)
	objectRoot := t.TempDir()
	writeScannedDocumentFixture(t, objectRoot, "tj+ru")
	if err := os.WriteFile(filepath.Join(objectRoot, "ocr", "OCR_DONE"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write OCR_DONE: %v", err)
	}
	objectID := seedDerivativeObject(t, d, objectRoot)
	searchable := &testSearchablePDFProcessor{}
	w := &DerivativesWorker{
		DB:                d,
		ImageProcessor:    &testImageProcessor{},
		AudioProcessor:    &testAudioProcessor{},
		VideoProcessor:    &testVideoProcessor{},
		DocumentProcessor: &testDocumentProcessor{},
		SearchablePDF:     searchable,
	}

	if err := w.processDerivatives(context.Background(), objectID, "JOB-1"); err != nil {
		t.Fatalf("process derivatives: %v", err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "derivatives", "access", "reading_ocr_v1.pdf")); err != nil {
		t.Fatalf("expected reading_ocr_v1.pdf: %v", err)
	}
	if searchable.called != 1 {
		t.Fatalf("expected searchable pdf processor called once, got %d", searchable.called)
	}
	if len(searchable.langs) != 1 || searchable.langs[0] != "tgk+rus" {
		t.Fatalf("expected searchable pdf language tgk+rus, got %#v", searchable.langs)
	}
	if got := objectProcessingState(t, d, objectID); got != "ocr_done" {
		t.Fatalf("expected processing state ocr_done, got %s", got)
	}
}

func TestProcessDerivativesScannedDocumentNormalizesPagesBeforeBuildingPDF(t *testing.T) {
	installFakeImg2PDF(t)
	d := openDerivativesTestDB(t)
	objectRoot := t.TempDir()
	writeScannedDocumentFixture(t, objectRoot, "eng")
	writeScannedDocumentPage(t, objectRoot, "page_0002.tif")
	objectID := seedDerivativeObject(t, d, objectRoot)
	argsFile := filepath.Join(t.TempDir(), "img2pdf-args.txt")
	t.Setenv("IMG2PDF_ARGS_FILE", argsFile)
	imageProcessor := &recordingImageProcessor{}
	w := &DerivativesWorker{
		DB:                d,
		ImageProcessor:    imageProcessor,
		AudioProcessor:    &testAudioProcessor{},
		VideoProcessor:    &testVideoProcessor{},
		DocumentProcessor: &testDocumentProcessor{},
		SearchablePDF:     &testSearchablePDFProcessor{},
	}

	if err := w.processDerivatives(context.Background(), objectID, "JOB-1"); err != nil {
		t.Fatalf("process derivatives: %v", err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "derivatives", "access", "reading_v1.pdf")); err != nil {
		t.Fatalf("expected reading_v1.pdf: %v", err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "derivatives", "images", "thumb", "thumb.jpg")); err != nil {
		t.Fatalf("expected thumb.jpg: %v", err)
	}
	if len(imageProcessor.resizedSources) != 3 {
		t.Fatalf("expected three resize calls (thumb + 2 pages), got %d", len(imageProcessor.resizedSources))
	}
	argsData, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read img2pdf args: %v", err)
	}
	args := strings.Fields(string(argsData))
	if len(args) != 2 {
		t.Fatalf("expected two img2pdf inputs, got %#v", args)
	}
	for _, arg := range args {
		if strings.HasSuffix(arg, "page_0001.tif") || strings.HasSuffix(arg, "page_0002.tif") {
			t.Fatalf("expected img2pdf to receive normalized files, got original path %q", arg)
		}
		if filepath.Ext(arg) != ".jpg" {
			t.Fatalf("expected normalized jpg input, got %q", arg)
		}
	}
	if got := objectProcessingState(t, d, objectID); got != "ingested" {
		t.Fatalf("expected processing state ingested, got %s", got)
	}
}

func TestProcessDerivativesScannedDocumentUsesOnlyFirstPageForThumbnail(t *testing.T) {
	installFakeImg2PDF(t)
	d := openDerivativesTestDB(t)
	objectRoot := t.TempDir()
	writeScannedDocumentFixture(t, objectRoot, "eng")
	writeScannedDocumentPage(t, objectRoot, "page_0002.tif")
	objectID := seedDerivativeObject(t, d, objectRoot)
	imageProcessor := &recordingImageProcessor{}
	w := &DerivativesWorker{
		DB:                d,
		ImageProcessor:    imageProcessor,
		AudioProcessor:    &testAudioProcessor{},
		VideoProcessor:    &testVideoProcessor{},
		DocumentProcessor: &testDocumentProcessor{},
		SearchablePDF:     &testSearchablePDFProcessor{},
	}

	if err := w.processDerivatives(context.Background(), objectID, "JOB-1"); err != nil {
		t.Fatalf("process derivatives: %v", err)
	}
	if len(imageProcessor.resizedSources) != 3 {
		t.Fatalf("expected three resize calls (thumb + 2 pages), got %d", len(imageProcessor.resizedSources))
	}
	want := filepath.Join(objectRoot, "original", "pages", "page_0001.tif")
	if imageProcessor.resizedSources[0] != want {
		t.Fatalf("expected thumbnail source %q, got %q", want, imageProcessor.resizedSources[0])
	}
	if imageProcessor.resizedSources[1] != want {
		t.Fatalf("expected first normalized page %q, got %q", want, imageProcessor.resizedSources[1])
	}
	second := filepath.Join(objectRoot, "original", "pages", "page_0002.tif")
	if imageProcessor.resizedSources[2] != second {
		t.Fatalf("expected second normalized page %q, got %q", second, imageProcessor.resizedSources[2])
	}
}

func TestProcessOCRSignalsOrchestratorWakeup(t *testing.T) {
	d := openDerivativesTestDB(t)
	objectRoot := t.TempDir()
	writeScannedDocumentFixture(t, objectRoot, "eng")
	objectID := seedDerivativeObject(t, d, objectRoot)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO jobs (job_id, object_id, job_type, state, queued_at, payload_json)
		VALUES (?, ?, 'ocr', 'running', ?, ?)
	`, "JOB-OCR-1", objectID, now, `{"language":"eng"}`); err != nil {
		t.Fatalf("insert ocr job: %v", err)
	}
	tesseractDir := t.TempDir()
	tesseractScript := filepath.Join(tesseractDir, "tesseract")
	if err := os.WriteFile(tesseractScript, []byte("#!/bin/sh\nprintf 'ocr text'\n"), 0o755); err != nil {
		t.Fatalf("write fake tesseract: %v", err)
	}
	t.Setenv("PATH", tesseractDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	w := &OCRWorker{DB: d, DefaultLang: "eng", OCRVersion: "v1"}

	if err := w.processOCR(context.Background(), objectID, "JOB-OCR-1", "eng", ocrPayload{Language: "eng"}); err != nil {
		t.Fatalf("process OCR: %v", err)
	}
	if !orchestratorWakeupExists(t, d) {
		t.Fatalf("expected orchestrator wakeup to be signaled")
	}
}
