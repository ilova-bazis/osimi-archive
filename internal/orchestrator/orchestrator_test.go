package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOCRLanguageNormalizesAliases(t *testing.T) {
	c := catalogManifest{}
	c.Processing.OCRText = &processingToggle{Language: "tj+ru"}

	got, err := resolveOCRLanguage(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tgk+rus" {
		t.Fatalf("expected tgk+rus, got %q", got)
	}
}

func TestDerivativesCompleteScannedDocumentBeforeOCRReady(t *testing.T) {
	objectRoot := t.TempDir()
	accessDir := filepath.Join(objectRoot, "derivatives", "access")
	thumbDir := filepath.Join(objectRoot, "derivatives", "images", "thumb")
	if err := os.MkdirAll(accessDir, 0o755); err != nil {
		t.Fatalf("mkdir access dir: %v", err)
	}
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatalf("mkdir thumb dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(accessDir, "reading_v1.pdf"), []byte("pdf"), 0o644); err != nil {
		t.Fatalf("write reading_v1.pdf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(thumbDir, "thumb.jpg"), []byte("thumb"), 0o644); err != nil {
		t.Fatalf("write thumb.jpg: %v", err)
	}

	if !derivativesComplete("scanned_document", objectRoot, true) {
		t.Fatalf("expected derivatives to be considered complete until OCR is ready")
	}
}

func TestDerivativesCompleteScannedDocumentRequiresThumbnail(t *testing.T) {
	objectRoot := t.TempDir()
	accessDir := filepath.Join(objectRoot, "derivatives", "access")
	if err := os.MkdirAll(accessDir, 0o755); err != nil {
		t.Fatalf("mkdir access dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(accessDir, "reading_v1.pdf"), []byte("pdf"), 0o644); err != nil {
		t.Fatalf("write reading_v1.pdf: %v", err)
	}

	if derivativesComplete("scanned_document", objectRoot, false) {
		t.Fatalf("expected derivatives incomplete without thumb.jpg")
	}
}

func TestDerivativesCompleteScannedDocumentRequiresSearchablePDFOnceOCRDone(t *testing.T) {
	objectRoot := t.TempDir()
	accessDir := filepath.Join(objectRoot, "derivatives", "access")
	ocrDir := filepath.Join(objectRoot, "ocr")
	thumbDir := filepath.Join(objectRoot, "derivatives", "images", "thumb")
	if err := os.MkdirAll(accessDir, 0o755); err != nil {
		t.Fatalf("mkdir access dir: %v", err)
	}
	if err := os.MkdirAll(ocrDir, 0o755); err != nil {
		t.Fatalf("mkdir ocr dir: %v", err)
	}
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatalf("mkdir thumb dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(accessDir, "reading_v1.pdf"), []byte("pdf"), 0o644); err != nil {
		t.Fatalf("write reading_v1.pdf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(thumbDir, "thumb.jpg"), []byte("thumb"), 0o644); err != nil {
		t.Fatalf("write thumb.jpg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ocrDir, "OCR_DONE"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write OCR_DONE: %v", err)
	}

	if derivativesComplete("scanned_document", objectRoot, true) {
		t.Fatalf("expected derivatives incomplete without reading_ocr_v1.pdf after OCR completion")
	}
	if err := os.WriteFile(filepath.Join(accessDir, "reading_ocr_v1.pdf"), []byte("pdf-ocr"), 0o644); err != nil {
		t.Fatalf("write reading_ocr_v1.pdf: %v", err)
	}
	if !derivativesComplete("scanned_document", objectRoot, true) {
		t.Fatalf("expected derivatives complete once both PDFs exist")
	}
}
