package ingest

import (
	"fmt"
	"time"
)

type ingestManifestV1 struct {
	SchemaVersion string `json:"schema_version"`
	ObjectID      string `json:"object_id"`
	CreatedAt     string `json:"created_at"`

	Ingest      any `json:"ingest"`
	Original    any `json:"original"`
	Derivatives any `json:"derivatives"`
	OCR         any `json:"ocr"`
	Checksums   any `json:"checksums"`
	Tools       any `json:"tools,omitempty"`
}

func buildIngestManifestV1(objectID, batchPath string, pages any, pageCount int) ingestManifestV1 {
	now := time.Now().UTC().Format(time.RFC3339)

	return ingestManifestV1{
		SchemaVersion: "1.0",
		ObjectID:      objectID,
		CreatedAt:     now,
		Ingest: map[string]any{
			"ingest_id": "ING-" + now,
			"source": map[string]any{
				"type":        "drop_folder",
				"path":        batchPath,
				"captured_at": now,
			},
			"operator": map[string]any{
				"name":    nil,
				"contact": nil,
			},
			"notes": nil,
		},
		Original: map[string]any{
			"pages_dir":     "original/pages",
			"page_count":    pageCount,
			"page_naming":   "page_%04d",
			"page_start":    1,
			"format_policy": "preserve",
			"pages":         pages,
		},
		Derivatives: map[string]any{},
		OCR:         map[string]any{},
		Checksums: map[string]any{
			"algorithm": "sha256",
			"files": []any{
				map[string]any{
					"path":   "checksums/sha256.txt",
					"covers": []string{"original", "meta"},
				},
			},
		},
	}
}

func buildIngestManifestV1Files(objectID, batchPath, subdir, naming string, files any, fileCount int) ingestManifestV1 {
	now := time.Now().UTC().Format(time.RFC3339)

	return ingestManifestV1{
		SchemaVersion: "1.0",
		ObjectID:      objectID,
		CreatedAt:     now,
		Ingest: map[string]any{
			"ingest_id": "ING-" + now,
			"source": map[string]any{
				"type":        "drop_folder",
				"path":        batchPath,
				"captured_at": now,
			},
			"operator": map[string]any{
				"name":    nil,
				"contact": nil,
			},
			"notes": nil,
		},
		Original: map[string]any{
			"files_dir":     fmt.Sprintf("original/%s", subdir),
			"file_count":    fileCount,
			"file_naming":   naming,
			"format_policy": "preserve",
			"files":         files,
		},
		Derivatives: map[string]any{},
		OCR:         map[string]any{},
		Checksums: map[string]any{
			"algorithm": "sha256",
			"files": []any{
				map[string]any{
					"path":   "checksums/sha256.txt",
					"covers": []string{"original", "meta"},
				},
			},
		},
	}
}
