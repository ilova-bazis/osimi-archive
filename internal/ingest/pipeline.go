package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type PipelineManifest struct {
	ObjectID    string             `json:"object_id"`
	UpdatedAt   string             `json:"updated_at"`
	Derivatives *PipelineArtifacts `json:"derivatives,omitempty"`
	OCR         *PipelineArtifacts `json:"ocr,omitempty"`
}

type PipelineArtifacts struct {
	CompletedAt string           `json:"completed_at"`
	Version     string           `json:"version,omitempty"`
	Artifacts   []ArtifactRecord `json:"artifacts"`
}

type ArtifactRecord struct {
	Type      string `json:"type"`
	Path      string `json:"path"`
	MimeType  string `json:"mime_type,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

func WritePipelineManifest(objectRoot, pipeline string, artifacts []ArtifactRecord, version string) error {
	manifestPath := filepath.Join(objectRoot, "meta", fmt.Sprintf("pipeline.%s.json", pipeline))

	var existing PipelineManifest
	if data, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pipelineArtifacts := PipelineArtifacts{
		CompletedAt: now,
		Version:     version,
		Artifacts:   artifacts,
	}

	switch pipeline {
	case "derivatives":
		existing.Derivatives = &pipelineArtifacts
	case "ocr":
		existing.OCR = &pipelineArtifacts
	default:
		return fmt.Errorf("unknown pipeline: %s", pipeline)
	}

	existing.ObjectID = extractObjectIDFromPath(objectRoot)
	existing.UpdatedAt = now

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, data, 0644)
}

func extractObjectIDFromPath(objectRoot string) string {
	return filepath.Base(objectRoot)
}

type artifactScanner struct{}

func ScanDerivativesArtifacts(objectRoot string) ([]ArtifactRecord, error) {
	var artifacts []ArtifactRecord

	derivativeDirs := []string{
		filepath.Join(objectRoot, "derivatives", "access"),
		filepath.Join(objectRoot, "derivatives", "delivery"),
		filepath.Join(objectRoot, "derivatives", "images", "web"),
		filepath.Join(objectRoot, "derivatives", "images", "thumb"),
		filepath.Join(objectRoot, "derivatives", "audio"),
		filepath.Join(objectRoot, "derivatives", "video"),
	}

	for _, dir := range derivativeDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			relPath, err := filepath.Rel(objectRoot, path)
			if err != nil {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			artifacts = append(artifacts, ArtifactRecord{
				Type:      "derivative",
				Path:      relPath,
				MimeType:  mimeFromFilename(e.Name()),
				SizeBytes: info.Size(),
			})
		}
	}

	return artifacts, nil
}

func ScanOCRArtifacts(objectRoot string) ([]ArtifactRecord, error) {
	var artifacts []ArtifactRecord

	ocrDir := filepath.Join(objectRoot, "ocr")
	entries, err := os.ReadDir(ocrDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(ocrDir, e.Name())
		relPath, err := filepath.Rel(objectRoot, path)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		artifacts = append(artifacts, ArtifactRecord{
			Type:      "ocr",
			Path:      relPath,
			MimeType:  mimeFromFilename(e.Name()),
			SizeBytes: info.Size(),
		})
	}

	return artifacts, nil
}

func mimeFromFilename(name string) string {
	ext := filepath.Ext(name)
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
