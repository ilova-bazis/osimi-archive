# Pipeline Gap Analysis

This document lists the important archive-processing pipelines that already exist, the ones that are still missing, and likely external tools we can use for implementation.

It is meant as a practical roadmap for worker development rather than a strict API contract.

## Current State

The repository already has these pipeline capabilities:

- Ingest: batch-to-object import with item-scoped `catalog.json` and deterministic original-file layout.
- Image derivatives:
  - scanned-document access PDF generation via `img2pdf`
  - photo web derivatives and thumbnails via `vips` or `ffmpeg`
- Audio derivatives:
  - access transcode to AAC/M4A via `ffmpeg`
- Video derivatives:
  - access transcode to H.264/AAC MP4 via `ffmpeg`
- Document derivatives:
  - PDF passthrough or office-to-PDF conversion via `soffice`
- OCR text:
  - page OCR for scanned documents via `tesseract`
  - OCR text persisted into SQLite FTS tables

So the system is already beyond image-only processing, but several important pipelines are still missing or only partially covered.

## High-Priority Missing Pipelines

### 1. Audio Transcription

- Status: missing
- Applies to: `audio`
- Expected outputs:
  - `transcript/transcript_v1.txt`
  - optionally `transcript/transcript_v1.json` with timestamps / segments / confidence
  - marker like `transcript/TRANSCRIPT_DONE`
- Why it matters:
  - the API and catalog processing model already reference `audio_transcript`
  - audio without transcript remains hard to search and review
- Candidate tools:
  - `whisper.cpp`
  - OpenAI Whisper CLI / Python package
  - `faster-whisper`
  - `ffmpeg` for audio normalization / mono / sample-rate prep before inference
- Recommended first pass:
  - use `ffmpeg` to normalize to mono 16 kHz WAV
  - run `whisper.cpp` or `faster-whisper`
  - store plain text plus segment JSON

### 2. Video Transcription

- Status: missing
- Applies to: `video`
- Expected outputs:
  - `transcript/transcript_v1.txt`
  - `transcript/transcript_v1.json`
  - marker like `transcript/TRANSCRIPT_DONE`
- Why it matters:
  - the API already references `video_transcript`
  - video searchability depends on transcript extraction
- Candidate tools:
  - `ffmpeg` to extract audio from video
  - `whisper.cpp`
  - OpenAI Whisper / `faster-whisper`
- Recommended first pass:
  - extract audio with `ffmpeg`
  - reuse the same transcript pipeline used for audio items

### 3. Search / Index Finalization Pipeline

- Status: partially present, but no dedicated pipeline worker
- Applies to: items with OCR or transcripts
- Existing pieces:
  - OCR text is inserted into `page_text` and `page_text_fts`
  - lifecycle states mention `index_running` and `index_done`
- Missing pieces:
  - explicit index job type / worker
  - index completion marker and state transitions
  - unified indexing for OCR + transcripts + metadata
- Candidate tools:
  - current SQLite FTS is enough for a first version
  - future option: Tantivy / Meilisearch / OpenSearch if search scope grows
- Recommended first pass:
  - keep SQLite FTS
  - add an `index` job that consolidates OCR text, transcript text, and key metadata into one searchable representation

## Important Derivative Gaps

### 4. OCR-Enhanced Access PDF

- Status: missing
- Applies to: `scanned_document`
- Expected outputs:
  - `derivatives/access/reading_ocr_v1.pdf`
- Why it matters:
  - a text-searchable PDF is a common access format for scanned documents
  - current docs already mention OCR-enhanced delivery PDFs as a future/non-gating artifact
- Candidate tools:
  - `ocrmypdf`
  - `tesseract` + `img2pdf` + PDF text layer tooling
  - Ghostscript / `qpdf` for normalization if needed
- Recommended first pass:
  - generate base PDF as now
  - run `ocrmypdf` to create searchable access PDF from image-based access PDF

### 5. Contact Sheets / Multi-Image Viewer Derivatives

- Status: missing
- Applies to: `photo`, possibly `scanned_document`
- Expected outputs:
  - contact-sheet JPEG / PNG
  - optional lightweight gallery manifest JSON
- Why it matters:
  - helps UI review for multi-image items
  - useful for archivists before opening full-resolution assets
- Candidate tools:
  - `vips`
  - ImageMagick `montage`
  - `ffmpeg` tile filter for image sequences

### 6. Video Poster / Preview Clip Pipeline

- Status: missing
- Applies to: `video`
- Expected outputs:
  - poster frame thumbnail
  - web preview stills or short clip
- Why it matters:
  - current video derivative path creates an access MP4, but UI browsing also needs representative preview artifacts
- Candidate tools:
  - `ffmpeg` for poster frames, spritesheets, short preview clips
- Recommended first pass:
  - extract midpoint frame as thumbnail
  - optional 10-20 second MP4 preview clip

### 7. Audio Waveform / Preview Metadata

- Status: missing
- Applies to: `audio`
- Expected outputs:
  - waveform JSON or PNG
  - duration / loudness / stream metadata summary
- Why it matters:
  - improves player UX and review workflow
- Candidate tools:
  - `ffmpeg`
  - `ffprobe`
  - `audiowaveform`

## Metadata / Enrichment Pipelines Worth Adding

### 8. EXIF / Technical Metadata Extraction

- Status: mostly missing as a formal pipeline
- Applies to: image, audio, video, document
- Expected outputs:
  - normalized technical metadata stored under object metadata or pipeline manifest
- Why it matters:
  - useful for preservation and diagnostics
  - helps expose duration, dimensions, codecs, DPI, camera info, etc.
- Candidate tools:
  - `exiftool`
  - `ffprobe`
  - `pdfinfo`
  - `identify` / `vipsheader`

### 9. Language Detection for OCR / Transcripts

- Status: missing
- Applies to: OCR text and transcript text
- Expected outputs:
  - detected language code
  - optional confidence score
- Why it matters:
  - can validate or fill missing catalog metadata
  - can improve downstream indexing and UI filtering
- Candidate tools:
  - `lingua`
  - fastText language ID
  - CLD3 bindings

### 10. Entity / Keyword Extraction

- Status: missing
- Applies to: OCR text and transcripts
- Expected outputs:
  - extracted keywords / names / places for search facets
- Why it matters:
  - can improve discovery without manual tagging everything
- Candidate tools:
  - spaCy
  - Presidio / Hugging Face NER models
  - simple TF-IDF / YAKE for lightweight keyword extraction

## Preservation / Quality Pipelines

### 11. Fixity Reverification Pipeline

- Status: initial ingest checksums exist, but no recurring reverification pipeline
- Applies to: all objects
- Expected outputs:
  - fixity audit logs
  - failure events for checksum drift
- Why it matters:
  - preservation systems usually need scheduled checksum revalidation
- Candidate tools:
  - built-in SHA-256 hashing in Go is sufficient

### 12. File Format Validation / Characterization

- Status: missing
- Applies to: all originals
- Expected outputs:
  - format validation results
  - characterization metadata
- Why it matters:
  - helps preservation review and bad-file detection
- Candidate tools:
  - Siegfried / `sf`
  - DROID
  - JHOVE for format-specific validation

## Suggested Implementation Order

If we want the highest value next, I would implement in this order:

1. audio transcription
2. video transcription
3. index finalization worker
4. OCR-enhanced searchable PDF
5. video poster/preview generation
6. technical metadata extraction
7. fixity reverification
8. richer enrichment pipelines

## Tool Recommendations By Pipeline

| Pipeline | Best first tool | Good alternatives |
| --- | --- | --- |
| image resize / thumbnails | `vips` | `ffmpeg`, ImageMagick |
| scanned-document access PDF | `img2pdf` | Ghostscript, `qpdf` |
| OCR text | `tesseract` | PaddleOCR, EasyOCR |
| OCR searchable PDF | `ocrmypdf` | `tesseract` + PDF tooling |
| audio transcription | `whisper.cpp` | `faster-whisper`, Whisper |
| video transcription | `ffmpeg` + `whisper.cpp` | `faster-whisper` |
| audio access derivatives | `ffmpeg` | - |
| video access derivatives | `ffmpeg` | - |
| office to PDF | `soffice` | LibreOffice UNO, unoconv |
| technical metadata extraction | `exiftool` + `ffprobe` | `pdfinfo`, `vipsheader` |
| format validation | Siegfried | DROID, JHOVE |
| waveform generation | `audiowaveform` | `ffmpeg` + custom processing |

## Notes For Implementation

- Keep pipeline outputs deterministic and rebuildable from originals.
- Prefer writing machine outputs into dedicated directories like `ocr/`, `transcript/`, and `derivatives/` rather than mixing them into `catalog.json`.
- Continue using marker files plus pipeline manifests so orchestration can remain filesystem-first.
- Reuse the current object-centric worker pattern: planner decides, workers execute, backend sync happens after artifacts are produced.
