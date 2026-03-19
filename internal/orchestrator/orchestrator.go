package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ilova-bazis/osimi-archive/internal/db"
	"github.com/ilova-bazis/osimi-archive/internal/ocrlang"
)

const (
	defaultMinInterval = 10 * time.Second
	defaultMaxInterval = 15 * time.Minute
	wakeCheckInterval  = 5 * time.Second
)

type Orchestrator struct {
	DB          *db.DB
	ArchiveRoot string
	MinInterval time.Duration
	MaxInterval time.Duration
	lastSignal  time.Time
}

func (o *Orchestrator) Run(ctx context.Context) error {
	minInterval := o.MinInterval
	maxInterval := o.MaxInterval
	if minInterval <= 0 {
		minInterval = defaultMinInterval
	}
	if maxInterval <= 0 {
		maxInterval = defaultMaxInterval
	}

	backoff := minInterval
	nextRun := time.Now()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		wakeup, err := o.checkWakeup(ctx)
		if err != nil {
			log.Printf("orchestrator wakeup check failed: %v", err)
		}
		if wakeup {
			backoff = minInterval
			nextRun = time.Now()
		}

		if time.Now().After(nextRun) {
			log.Println("Checking for work")
			workFound, err := o.RunOnce(ctx)
			fmt.Printf("work found %v, error: %v\n", workFound, err)
			if err != nil {
				log.Printf("orchestrator run failed: %v", err)
			}
			if workFound {
				backoff = minInterval
			} else {
				backoff *= 2
				if backoff > maxInterval {
					backoff = maxInterval
				}
			}
			nextRun = time.Now().Add(backoff)
		}

		sleepFor := time.Until(nextRun)
		log.Println("sleeping for ", sleepFor)
		log.Println("next run is ", nextRun)
		if sleepFor <= 0 {
			continue
		}
		if sleepFor > wakeCheckInterval {
			sleepFor = wakeCheckInterval
		}
		select {
		case <-time.After(sleepFor):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (o *Orchestrator) RunOnce(ctx context.Context) (bool, error) {
	fmt.Println("running once")
	objectsRoot := filepath.Join(o.ArchiveRoot, "objects")
	var enqueued bool

	err := filepath.WalkDir(objectsRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			fmt.Println("initial walk error", err)
			return nil
		}
		if !entry.IsDir() {
			fmt.Println("is directory", err)
			return nil
		}
		if entry.Name() == "objects" {
			fmt.Println("equals objects", err)
			return nil
		}
		if strings.HasPrefix(entry.Name(), "OBJ-") {
			fmt.Println("has prefix OBJ-")
			objectID := entry.Name()
			objectRoot := path
			fmt.Println("attempting to run plan object")
			queued, err := o.planObject(ctx, objectID, objectRoot)
			if err != nil {
				log.Printf("orchestrator plan failed for %s: %v", objectID, err)
			} else if queued {
				enqueued = true
			}
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return enqueued, err
	}
	return enqueued, nil
}

func (o *Orchestrator) planObject(ctx context.Context, objectID, objectRoot string) (bool, error) {
	catalogPath := filepath.Join(objectRoot, "meta", "catalog.json")
	catalog, err := readCatalog(catalogPath)
	if err != nil {
		fmt.Println("catalog read error", err)
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	itemKind := resolveItemKind(catalog)
	enqueued := false
	ocrEnabled, ocrLang, err := ocrIntent(catalog, itemKind)
	if err != nil {
		return enqueued, err
	}

	fmt.Println("item kind is", itemKind)
	if shouldRunDerivatives(catalog, itemKind) {
		complete := derivativesComplete(itemKind, objectRoot, ocrEnabled)
		if !complete {
			active, err := o.DB.HasActiveJob(ctx, objectID, "derivatives")
			if err != nil {
				return enqueued, err
			}
			if !active {
				jobID := buildJobID(objectID, "derivatives")
				if err := o.DB.InsertJob(ctx, jobID, objectID, "derivatives", nil); err != nil {
					return enqueued, err
				}
				enqueued = true
			}
		}
	}

	fmt.Println("ocr enabled", ocrEnabled)
	if ocrEnabled {
		complete := ocrComplete(objectRoot)
		fmt.Println("is complete", complete)
		if !complete {
			fmt.Println("checking has active job")
			active, err := o.DB.HasActiveJob(ctx, objectID, "ocr")
			if err != nil {
				return enqueued, err
			}
			if !active {
				payload, err := json.Marshal(ocrPayload{Language: ocrLang})
				if err != nil {
					return enqueued, err
				}
				payloadStr := string(payload)
				jobID := buildJobID(objectID, "ocr")
				if err := o.DB.InsertJob(ctx, jobID, objectID, "ocr", &payloadStr); err != nil {
					return enqueued, err
				}
				enqueued = true
			}
		}
	}

	return enqueued, nil
}

func (o *Orchestrator) checkWakeup(ctx context.Context) (bool, error) {
	lastSignal, ok, err := o.DB.GetOrchestratorWakeup(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if ok && lastSignal.After(o.lastSignal) {
		o.lastSignal = lastSignal
		return true, nil
	}
	return false, nil
}

type catalogManifest struct {
	ItemKind       string           `json:"item_kind,omitempty"`
	Processing     processingIntent `json:"processing,omitempty"`
	Classification struct {
		Type     string `json:"type,omitempty"`
		Language string `json:"language,omitempty"`
	} `json:"classification,omitempty"`
}

type processingIntent struct {
	OCRText *processingToggle `json:"ocr_text,omitempty"`
}

type processingToggle struct {
	Enabled  *bool  `json:"enabled,omitempty"`
	Language string `json:"language,omitempty"`
}

type ocrPayload struct {
	Language string `json:"language,omitempty"`
}

func readCatalog(path string) (catalogManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return catalogManifest{}, err
	}
	var c catalogManifest
	if err := json.Unmarshal(b, &c); err != nil {
		return catalogManifest{}, err
	}
	return c, nil
}

func resolveItemKind(c catalogManifest) string {
	if strings.TrimSpace(c.ItemKind) != "" {
		return strings.TrimSpace(c.ItemKind)
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

func shouldRunDerivatives(c catalogManifest, itemKind string) bool {
	switch itemKind {
	case "scanned_document", "photo", "audio", "video", "document":
		return true
	default:
		return false
	}
}

func derivativesComplete(itemKind, objectRoot string, ocrEnabled bool) bool {
	switch itemKind {
	case "scanned_document":
		accessPDF := filepath.Join(objectRoot, "derivatives", "access", "reading_v1.pdf")
		thumb := filepath.Join(objectRoot, "derivatives", "images", "thumb", "thumb.jpg")
		ocrPDF := filepath.Join(objectRoot, "derivatives", "access", "reading_ocr_v1.pdf")
		if !fileExists(accessPDF) {
			return false
		}
		if !fileExists(thumb) {
			return false
		}
		if !ocrEnabled {
			return true
		}
		if !ocrComplete(objectRoot) {
			return true
		}
		return fileExists(ocrPDF)
	case "photo":
		return hasFiles(filepath.Join(objectRoot, "derivatives", "images", "web")) || hasFiles(filepath.Join(objectRoot, "derivatives", "images", "thumb"))
	case "audio":
		return hasFiles(filepath.Join(objectRoot, "derivatives", "audio"))
	case "video":
		return hasFiles(filepath.Join(objectRoot, "derivatives", "video"))
	case "document":
		return hasFiles(filepath.Join(objectRoot, "derivatives", "access"))
	default:
		return true
	}
}

func ocrIntent(c catalogManifest, itemKind string) (bool, string, error) {
	if c.Processing.OCRText != nil && c.Processing.OCRText.Enabled != nil {
		enabled := *c.Processing.OCRText.Enabled
		lang, err := resolveOCRLanguage(c)
		return enabled, lang, err
	}

	switch itemKind {
	case "scanned_document":
		lang, err := resolveOCRLanguage(c)
		return true, lang, err
	case "photo", "audio":
		return false, "", nil
	default:
		return false, "", nil
	}
}

func resolveOCRLanguage(c catalogManifest) (string, error) {
	preferred := ""
	fallback := strings.TrimSpace(c.Classification.Language)
	if c.Processing.OCRText != nil {
		preferred = strings.TrimSpace(c.Processing.OCRText.Language)
	}
	return ocrlang.Resolve(preferred, fallback)
}

func ocrComplete(objectRoot string) bool {
	marker := filepath.Join(objectRoot, "ocr", "OCR_DONE")
	return fileExists(marker)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true
		}
	}
	return false
}

func buildJobID(objectID, jobType string) string {
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	return fmt.Sprintf("JOB-%s-%s-%s", objectID, jobType, timestamp)
}
