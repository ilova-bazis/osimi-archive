package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/ilova-bazis/osimi-archive/internal/config"
	"github.com/ilova-bazis/osimi-archive/internal/db"
	"github.com/ilova-bazis/osimi-archive/internal/vps"
)

type leaseState struct {
	mu          sync.RWMutex
	leaseToken  string
	catalogJSON json.RawMessage
}

var errDropNotFound = errors.New("drop not found")

const availableFilesSnapshotAction = "available_files_snapshot"

func (s *leaseState) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leaseToken
}

func (s *leaseState) CatalogJSON() json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.catalogJSON
}

func (s *leaseState) UpdateFromLease(lease *vps.Lease) {
	if lease == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(lease.LeaseToken) != "" {
		s.leaseToken = lease.LeaseToken
	}
	if len(lease.CatalogJSON) > 0 {
		s.catalogJSON = lease.CatalogJSON
	}
}

type downloadItem struct {
	FileName string
	Path     string
}

type downloadRequestLeaseState struct {
	mu         sync.RWMutex
	leaseToken string
}

func (s *downloadRequestLeaseState) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leaseToken
}

func (s *downloadRequestLeaseState) Update(req *vps.DownloadRequest) {
	if req == nil {
		return
	}
	token := strings.TrimSpace(req.LeaseToken)
	if token == "" {
		return
	}
	s.mu.Lock()
	s.leaseToken = token
	s.mu.Unlock()
}

func main() {
	cfg := config.Load()
	if strings.TrimSpace(cfg.VPSBaseURL) == "" || strings.TrimSpace(cfg.WorkerAuthToken) == "" {
		log.Fatal("VPS_BASE_URL and WORKER_AUTH_TOKEN must be set")
	}

	d, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer d.Close()

	client := vps.NewClientVerbose(cfg.VPSBaseURL, cfg.WorkerAuthToken, cfg.WorkerID, cfg.Verbose)

	if cfg.Verbose {
		log.Println("[VERBOSE] pulld started with verbose logging enabled")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	log.Println("vps puller ready")

	go notifierLoop(ctx, d, client, cfg)
	go backendObjectTasksLoop(ctx, d, client, cfg)
	go downloadRequestsLoop(ctx, d, client, cfg)

	pollTicker := time.NewTicker(cfg.LeasePollInterval)
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-sigCh:
			log.Printf("shutdown signal received: %v", sig)
			cancel()
			return
		case <-pollTicker.C:
			if cfg.Verbose {
				log.Println("[VERBOSE] polling for lease...")
			}
			lease, err := client.LeaseNext(ctx)
			if err != nil {
				log.Printf("lease request failed: %v", err)
				continue
			}
			if lease == nil {
				if cfg.Verbose {
					log.Println("[VERBOSE] no lease available")
				}
				continue
			}
			if cfg.Verbose {
				log.Printf("[VERBOSE] lease acquired: ingestion_id=%s batch_label=%s tenant_id=%s lease_expires_at=%s file_count=%d",
					lease.IngestionID, lease.BatchLabel, lease.TenantID, lease.LeaseExpiresAt, len(lease.DownloadURLs))
			}
			if err := handleLease(ctx, d, cfg, client, lease); err != nil {
				log.Printf("lease handling failed: %v", err)
			}
		}
	}
}

func handleLease(ctx context.Context, d *db.DB, cfg config.Config, client *vps.Client, lease *vps.Lease) error {
	state := &leaseState{leaseToken: lease.LeaseToken, catalogJSON: lease.CatalogJSON}

	if err := d.UpsertIngestionLease(ctx, lease.IngestionID, lease.LeaseID, lease.LeaseToken, lease.LeaseExpiresAt); err != nil {
		log.Printf("failed to upsert lease: %v", err)
	}

	if cfg.Verbose {
		log.Printf("[VERBOSE] handling lease for ingestion_id=%s", lease.IngestionID)
		if len(lease.CatalogJSON) > 0 {
			var prettyJSON bytes.Buffer
			if err := json.Indent(&prettyJSON, lease.CatalogJSON, "", "  "); err == nil {
				log.Printf("[VERBOSE] catalog_json:\n%s", prettyJSON.String())
			} else {
				log.Printf("[VERBOSE] catalog_json (raw): %s", string(lease.CatalogJSON))
			}
		} else {
			log.Println("[VERBOSE] catalog_json is empty")
		}
	}

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go heartbeatLoop(heartbeatCtx, d, cfg, client, lease.IngestionID, state)
	released := false
	defer func() {
		if released {
			return
		}
		if lease.IngestionID == "" || state.Token() == "" {
			return
		}
		if err := client.Release(ctx, lease.IngestionID, state.Token()); err != nil {
			log.Printf("release lease failed: %v", err)
		}
	}()

	if err := postStepEvent(ctx, d, lease.IngestionID, "PIPELINE_STEP_STARTED", "download"); err != nil {
		log.Printf("post start event failed: %v", err)
	}

	tmpDir, finalDir, err := buildDropPaths(cfg.IngestDrop, lease)
	if err != nil {
		postFailure(ctx, d, lease.IngestionID, "drop_path_error", err)
		return err
	}
	if err := handleExistingDrop(ctx, d, lease.IngestionID, finalDir); err == nil {
		return nil
	} else if err != errDropNotFound {
		postFailure(ctx, d, lease.IngestionID, "drop_conflict", err)
		return err
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		postFailure(ctx, d, lease.IngestionID, "drop_path_error", err)
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	downloads, checksumPath, err := downloadBatch(ctx, cfg, client, lease, tmpDir)
	if err != nil {
		postFailure(ctx, d, lease.IngestionID, "download_failed", err)
		return err
	}

	if err := writeCatalog(tmpDir, state.CatalogJSON()); err != nil {
		postFailure(ctx, d, lease.IngestionID, "catalog_write_failed", err)
		return err
	}
	if cfg.Verbose {
		log.Printf("[VERBOSE] catalog.json written to %s", tmpDir)
	}

	if checksumPath != "" {
		expected, err := parseChecksumFile(checksumPath)
		if err != nil {
			postFailure(ctx, d, lease.IngestionID, "checksum_parse_failed", err)
			return err
		}
		if cfg.Verbose {
			log.Printf("[VERBOSE] verifying %d checksums from %s", len(expected), checksumPath)
		}
		if err := verifyChecksums(downloads, expected); err != nil {
			postFailure(ctx, d, lease.IngestionID, "checksum_mismatch", err)
			return err
		}
		if cfg.Verbose {
			log.Println("[VERBOSE] checksums verified successfully")
		}
		if err := os.Remove(checksumPath); err != nil {
			log.Printf("checksum cleanup failed: %v", err)
		}
	}

	if err := os.Rename(tmpDir, finalDir); err != nil {
		postFailure(ctx, d, lease.IngestionID, "drop_finalize_failed", err)
		return err
	}
	if cfg.Verbose {
		log.Printf("[VERBOSE] drop finalized: %s", finalDir)
	}
	cleanup = false
	if err := writeLeaseMetadata(finalDir, lease.IngestionID, state.Token()); err != nil {
		postFailure(ctx, d, lease.IngestionID, "lease_metadata_failed", err)
		return err
	}
	if cfg.Verbose {
		log.Println("[VERBOSE] lease metadata written")
	}
	if err := os.WriteFile(filepath.Join(finalDir, cfg.DoneMarker), []byte("ok\n"), 0644); err != nil {
		postFailure(ctx, d, lease.IngestionID, "done_marker_failed", err)
		return err
	}
	if cfg.Verbose {
		log.Printf("[VERBOSE] done marker written: %s", cfg.DoneMarker)
	}
	if err := postIngestionProcessing(ctx, d, lease.IngestionID); err != nil {
		log.Printf("post processing event failed: %v", err)
	}

	if err := postStepEvent(ctx, d, lease.IngestionID, "PIPELINE_STEP_COMPLETED", "download"); err != nil {
		log.Printf("post complete event failed: %v", err)
	}

	released = true
	if cfg.Verbose {
		log.Printf("[VERBOSE] handoff complete for ingestion_id=%s; lease retained for ingest worker completion", lease.IngestionID)
	}
	return nil
}

func heartbeatLoop(ctx context.Context, d *db.DB, cfg config.Config, client *vps.Client, ingestionID string, state *leaseState) {
	if cfg.Verbose {
		log.Printf("[VERBOSE] heartbeat loop started for ingestion_id=%s interval=%v", ingestionID, cfg.LeaseHeartbeatInterval)
	}
	if cfg.LeaseHeartbeatInterval <= 0 {
		return
	}
	ticker := time.NewTicker(cfg.LeaseHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if cfg.Verbose {
				log.Println("[VERBOSE] heartbeat loop cancelled")
			}
			return
		case <-ticker.C:
			lease, err := client.Heartbeat(ctx, ingestionID, state.Token())
			if err != nil {
				log.Printf("heartbeat failed: %v", err)
				continue
			}
			if cfg.Verbose {
				log.Printf("[VERBOSE] heartbeat success: lease_expires_at=%s", lease.LeaseExpiresAt)
			}
			state.UpdateFromLease(lease)
			if err := d.UpsertIngestionLease(ctx, ingestionID, lease.LeaseID, lease.LeaseToken, lease.LeaseExpiresAt); err != nil {
				log.Printf("failed to update lease in db: %v", err)
			}
		}
	}
}

func buildDropPaths(ingestDrop string, lease *vps.Lease) (string, string, error) {
	base := strings.TrimSpace(lease.BatchLabel)
	if base == "" {
		base = "ingestion"
	}
	base = sanitizeName(base)
	if base == "" {
		base = "ingestion"
	}
	if strings.TrimSpace(lease.IngestionID) != "" {
		base = fmt.Sprintf("%s__ING-%s", base, lease.IngestionID)
	}
	finalDir := filepath.Join(ingestDrop, base)
	tmpRoot := filepath.Join(ingestDrop, ".tmp")
	tmpDir := filepath.Join(tmpRoot, base)
	return tmpDir, finalDir, nil
}

func handleExistingDrop(ctx context.Context, d *db.DB, ingestionID, finalDir string) error {
	info, err := os.Stat(finalDir)
	if err != nil {
		if os.IsNotExist(err) {
			return errDropNotFound
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("drop path exists but is not a directory: %s", finalDir)
	}

	donePath := filepath.Join(finalDir, "DONE")
	if _, err := os.Stat(donePath); err != nil {
		if os.IsNotExist(err) {
			if err := os.RemoveAll(finalDir); err != nil {
				return fmt.Errorf("cleanup incomplete drop: %w", err)
			}
			return errDropNotFound
		}
		return err
	}

	errorPath := filepath.Join(finalDir, "ERROR")
	if _, err := os.Stat(errorPath); err == nil {
		content, readErr := readErrorFile(errorPath)
		if readErr != nil {
			content = fmt.Sprintf("failed to read ERROR file: %v", readErr)
		}
		if postErr := postIngestionFailed(ctx, d, ingestionID, content); postErr != nil {
			return postErr
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := postIngestionProcessing(ctx, d, ingestionID); err != nil {
		return err
	}
	return nil
}

func downloadBatch(ctx context.Context, cfg config.Config, client *vps.Client, lease *vps.Lease, dstDir string) ([]downloadItem, string, error) {
	var items []downloadItem
	checksumPath := ""
	usedNames := map[string]int{}

	if cfg.Verbose {
		log.Printf("[VERBOSE] downloading %d files to %s", len(lease.DownloadURLs), dstDir)
	}

	for _, url := range lease.DownloadURLs {
		name := filepath.Base(url.StorageKey)
		if name == "." || name == string(filepath.Separator) || name == "" {
			name = url.FileID
		}
		if count := usedNames[name]; count > 0 {
			name = fmt.Sprintf("%s_%d", name, count+1)
		}
		usedNames[name]++

		if cfg.Verbose {
			log.Printf("[VERBOSE] download: file_id=%s storage_key=%s content_type=%s size_bytes=%d",
				url.FileID, url.StorageKey, url.ContentType, url.SizeBytes)
		}

		if isChecksumFile(name, url.ContentType) {
			path := filepath.Join(dstDir, name)
			if err := downloadToFile(ctx, client, url.DownloadURL, path); err != nil {
				return nil, "", err
			}
			checksumPath = path
			if cfg.Verbose {
				log.Printf("[VERBOSE] checksum file detected: %s", name)
			}
			continue
		}
		path := filepath.Join(dstDir, name)
		if err := downloadToFile(ctx, client, url.DownloadURL, path); err != nil {
			return nil, "", err
		}
		items = append(items, downloadItem{FileName: name, Path: path})
		if cfg.Verbose {
			log.Printf("[VERBOSE] downloaded: %s -> %s", name, path)
		}
	}
	if len(items) == 0 {
		return nil, checksumPath, fmt.Errorf("lease contains no files to ingest")
	}
	if cfg.Verbose {
		log.Printf("[VERBOSE] download complete: %d files, checksum_path=%s", len(items), checksumPath)
	}
	return items, checksumPath, nil
}

func downloadToFile(ctx context.Context, client *vps.Client, downloadURL, dst string) error {
	resp, err := client.Download(ctx, downloadURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download %s: status %d: %s", downloadURL, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmpFile.Close()
	}()
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpFile.Name(), dst)
}

func writeCatalog(dstDir string, catalog json.RawMessage) error {
	if len(catalog) == 0 {
		return fmt.Errorf("catalog_json is empty")
	}
	var payload any
	if err := json.Unmarshal(catalog, &payload); err != nil {
		return fmt.Errorf("catalog_json is invalid: %w", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dstDir, "catalog.json"), data, 0644)
}

func writeLeaseMetadata(dstDir, ingestionID, leaseToken string) error {
	if strings.TrimSpace(ingestionID) == "" {
		return fmt.Errorf("missing ingestion id")
	}
	if strings.TrimSpace(leaseToken) == "" {
		return fmt.Errorf("missing lease token")
	}
	if err := os.WriteFile(filepath.Join(dstDir, "INGESTION_ID"), []byte(ingestionID+"\n"), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dstDir, "LEASE_TOKEN"), []byte(leaseToken+"\n"), 0600); err != nil {
		return err
	}
	return nil
}

func parseChecksumFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	checksums := make(map[string]string)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid checksum line: %s", line)
		}
		hash := fields[0]
		name := fields[len(fields)-1]
		name = strings.TrimPrefix(name, "*")
		checksums[filepath.Base(name)] = hash
	}
	return checksums, nil
}

func readErrorFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func verifyChecksums(items []downloadItem, expected map[string]string) error {
	for _, item := range items {
		expectedHash, ok := expected[item.FileName]
		if !ok {
			return fmt.Errorf("missing checksum for %s", item.FileName)
		}
		actual, err := sha256File(item.Path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, expectedHash) {
			return fmt.Errorf("checksum mismatch for %s", item.FileName)
		}
	}
	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func isChecksumFile(name, contentType string) bool {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, "sha256.txt") || strings.HasSuffix(name, "checksums.txt") {
		return true
	}
	return strings.HasPrefix(contentType, "text/") && strings.Contains(name, "checksum")
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	return strings.Trim(name, "_")
}

func postStepEvent(ctx context.Context, d *db.DB, ingestionID, eventType, step string) error {
	if ingestionID == "" {
		return fmt.Errorf("missing ingestion id")
	}
	payload := map[string]any{
		"step": step,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ev := db.VPSEvent{
		EventID:     uuid.NewString(),
		IngestionID: ingestionID,
		EventType:   eventType,
		PayloadJSON: string(payloadJSON),
	}
	return d.EnqueueVPSEvent(ctx, ev)
}

func postIngestionProcessing(ctx context.Context, d *db.DB, ingestionID string) error {
	if ingestionID == "" {
		return fmt.Errorf("missing ingestion id")
	}
	payload := map[string]any{
		"step": "drop",
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ev := db.VPSEvent{
		EventID:     uuid.NewString(),
		IngestionID: ingestionID,
		EventType:   "INGESTION_PROCESSING",
		PayloadJSON: string(payloadJSON),
	}
	return d.EnqueueVPSEvent(ctx, ev)
}

func postIngestionFailed(ctx context.Context, d *db.DB, ingestionID, message string) error {
	if ingestionID == "" {
		return fmt.Errorf("missing ingestion id")
	}
	payload := map[string]any{
		"step":  "drop",
		"error": message,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ev := db.VPSEvent{
		EventID:     uuid.NewString(),
		IngestionID: ingestionID,
		EventType:   "INGESTION_FAILED",
		PayloadJSON: string(payloadJSON),
	}
	return d.EnqueueVPSEvent(ctx, ev)
}

func postFailure(ctx context.Context, d *db.DB, ingestionID, step string, err error) {
	if err == nil || ingestionID == "" {
		return
	}
	message := err.Error()
	payload := map[string]any{
		"step":  step,
		"error": message,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		log.Printf("marshal failure payload failed: %v", err)
		return
	}
	ev := db.VPSEvent{
		EventID:     uuid.NewString(),
		IngestionID: ingestionID,
		EventType:   "PIPELINE_STEP_FAILED",
		PayloadJSON: string(payloadJSON),
	}
	if err := d.EnqueueVPSEvent(ctx, ev); err != nil {
		log.Printf("enqueue failure event failed: %v", err)
	}
}

func notifierLoop(ctx context.Context, d *db.DB, client *vps.Client, cfg config.Config) {
	pollTicker := time.NewTicker(cfg.VPSNotifierPollInterval)
	defer pollTicker.Stop()

	log.Println("vps notifier loop started")

	for {
		select {
		case <-ctx.Done():
			log.Println("vps notifier loop stopped")
			return
		case <-pollTicker.C:
			events, err := d.FetchPendingVPSEvents(ctx, cfg.VPSNotifierBatchSize)
			if err != nil {
				log.Printf("fetch pending vps events failed: %v", err)
				continue
			}
			if len(events) == 0 {
				continue
			}
			if cfg.Verbose {
				log.Printf("[VERBOSE] notifier processing %d events", len(events))
			}
			for _, ev := range events {
				if err := deliverEvent(ctx, d, client, ev); err != nil {
					log.Printf("deliver vps event failed: %v", err)
				}
			}
		}
	}
}

func backendObjectTasksLoop(ctx context.Context, d *db.DB, client *vps.Client, cfg config.Config) {
	pollTicker := time.NewTicker(cfg.VPSNotifierPollInterval)
	defer pollTicker.Stop()

	log.Println("backend object tasks loop started")

	for {
		select {
		case <-ctx.Done():
			log.Println("backend object tasks loop stopped")
			return
		case <-pollTicker.C:
			for {
				task, err := d.ClaimNextBackendObjectTask(ctx)
				if err != nil {
					log.Printf("claim backend object task failed: %v", err)
					break
				}
				if task == nil {
					break
				}
				if err := deliverBackendObjectTask(ctx, d, client, cfg, *task); err != nil {
					log.Printf("deliver backend object task failed: task_id=%d object_id=%s action_type=%s err=%v", task.TaskID, task.ObjectID, task.ActionType, err)
				}
			}
		}
	}
}

func downloadRequestsLoop(ctx context.Context, d *db.DB, client *vps.Client, cfg config.Config) {
	pollTicker := time.NewTicker(cfg.DownloadRequestPollInterval)
	defer pollTicker.Stop()

	log.Println("download requests loop started")

	for {
		select {
		case <-ctx.Done():
			log.Println("download requests loop stopped")
			return
		case <-pollTicker.C:
			req, err := client.LeaseNextDownloadRequest(ctx)
			if err != nil {
				log.Printf("lease download request failed: %v", err)
				continue
			}
			if req == nil {
				continue
			}
			if err := processDownloadRequest(ctx, d, cfg, client, req); err != nil {
				log.Printf("process download request failed: %v", err)
			}
		}
	}
}

func processDownloadRequest(ctx context.Context, d *db.DB, cfg config.Config, client *vps.Client, req *vps.DownloadRequest) error {
	if req == nil {
		return nil
	}
	requestID := strings.TrimSpace(req.EffectiveRequestID())
	if requestID == "" {
		return fmt.Errorf("download request missing id")
	}
	if strings.TrimSpace(req.ObjectID) == "" {
		return fmt.Errorf("download request %s missing object_id", requestID)
	}
	state := &downloadRequestLeaseState{leaseToken: req.LeaseToken}
	completed := false
	defer func() {
		if completed {
			return
		}
		token := state.Token()
		if token == "" {
			return
		}
		if err := client.ReleaseDownloadRequest(ctx, requestID, token); err != nil {
			log.Printf("release download request failed: request_id=%s err=%v", requestID, err)
		}
	}()

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go downloadRequestHeartbeatLoop(heartbeatCtx, cfg, client, requestID, state)

	objectRoot, err := d.GetObjectRoot(ctx, req.ObjectID)
	if err != nil {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "OBJECT_NOT_FOUND",
			Message:   err.Error(),
			Retryable: false,
		})
	}

	filePath, err := resolveDownloadRequestArtifact(req, objectRoot)
	if err != nil {
		retryable := !errors.Is(err, os.ErrNotExist)
		code := "RESOLUTION_FAILED"
		if errors.Is(err, os.ErrNotExist) {
			code = "FILE_NOT_FOUND"
		}
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      code,
			Message:   err.Error(),
			Retryable: retryable,
		})
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "FILE_STAT_FAILED",
			Message:   err.Error(),
			Retryable: false,
		})
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), ".")
	if strings.TrimSpace(ext) == "" {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "FILE_EXTENSION_MISSING",
			Message:   fmt.Sprintf("cannot infer extension for %s", filePath),
			Retryable: false,
		})
	}
	presignReq := vps.DownloadPresignRequest{
		ContentType: contentTypeFromFilename(filePath),
		SizeBytes:   fileInfo.Size(),
		Extension:   ext,
	}
	presign, err := client.PresignDownloadArtifact(ctx, requestID, state.Token(), presignReq)
	if err != nil {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "PRESIGN_FAILED",
			Message:   err.Error(),
			Retryable: true,
		})
	}
	uploadPath := presign.UploadTargetPath()
	if strings.TrimSpace(uploadPath) == "" {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "UPLOAD_TARGET_MISSING",
			Message:   "presign response missing upload target",
			Retryable: true,
		})
	}
	if presign.Headers.ContentLength > 0 && int64(presign.Headers.ContentLength) != fileInfo.Size() {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "PRESIGN_LENGTH_MISMATCH",
			Message:   fmt.Sprintf("presign content-length %d does not match file size %d", presign.Headers.ContentLength, fileInfo.Size()),
			Retryable: false,
		})
	}

	file, err := os.Open(filePath)
	if err != nil {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "FILE_OPEN_FAILED",
			Message:   err.Error(),
			Retryable: false,
		})
	}
	defer file.Close()

	if err := client.UploadDownloadRequestArtifact(ctx, uploadPath, presignReq.ContentType, fileInfo.Size(), presign.Headers, file); err != nil {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "UPLOAD_FAILED",
			Message:   err.Error(),
			Retryable: true,
		})
	}

	if strings.TrimSpace(presign.UploadToken) == "" {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "UPLOAD_TOKEN_MISSING",
			Message:   "presign response missing upload_token",
			Retryable: true,
		})
	}

	if err := client.CompleteDownloadRequest(ctx, requestID, state.Token(), presign.UploadToken); err != nil {
		return failDownloadRequest(ctx, client, requestID, state.Token(), vps.DownloadFailPayload{
			Code:      "COMPLETE_FAILED",
			Message:   err.Error(),
			Retryable: true,
		})
	}

	completed = true
	if cfg.Verbose {
		log.Printf("[VERBOSE] download request completed: request_id=%s object_id=%s file=%s", requestID, req.ObjectID, filePath)
	}
	return nil
}

func downloadRequestHeartbeatLoop(ctx context.Context, cfg config.Config, client *vps.Client, requestID string, state *downloadRequestLeaseState) {
	if cfg.DownloadRequestHeartbeatInterval <= 0 {
		return
	}
	ticker := time.NewTicker(cfg.DownloadRequestHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := client.HeartbeatDownloadRequest(ctx, requestID, state.Token())
			if err != nil {
				log.Printf("download request heartbeat failed: request_id=%s err=%v", requestID, err)
				continue
			}
			state.Update(req)
		}
	}
}

func failDownloadRequest(ctx context.Context, client *vps.Client, requestID, leaseToken string, payload vps.DownloadFailPayload) error {
	if err := client.FailDownloadRequest(ctx, requestID, leaseToken, payload); err != nil {
		return fmt.Errorf("report download request failure failed: %w", err)
	}
	return fmt.Errorf("download request %s failed: %s", requestID, payload.Message)
}

func resolveDownloadRequestArtifact(req *vps.DownloadRequest, objectRoot string) (string, error) {
	requestID := req.EffectiveRequestID()
	archiveFileKey := strings.TrimSpace(req.EffectiveArchiveFileKey())
	artifactKind := strings.TrimSpace(req.EffectiveArtifactKind())
	variant := strings.TrimSpace(req.EffectiveVariant())

	if archiveFileKey != "" {
		path, kind, parsedVariant, err := resolveArtifactPathFromKey(archiveFileKey, objectRoot)
		if err == nil {
			if artifactKind == "" {
				artifactKind = kind
			}
			if parsedVariant != "" {
				variant = parsedVariant
			}
			if _, err := os.Stat(path); err != nil {
				return "", err
			}
			return path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("request %s resolve archive_file_key: %w", requestID, err)
		}
	}

	if artifactKind == "" {
		return "", fmt.Errorf("request %s missing artifact_kind", requestID)
	}
	path, err := resolveArtifactPathByKind(objectRoot, artifactKind, variant)
	if err != nil {
		return "", fmt.Errorf("request %s resolve artifact by kind: %w", requestID, err)
	}
	return path, nil
}

func resolveArtifactPathFromKey(archiveFileKey, objectRoot string) (string, string, string, error) {
	parts := strings.SplitN(archiveFileKey, ":", 4)
	if len(parts) == 3 {
		artifactKind := strings.TrimSpace(parts[1])
		variant := strings.TrimSpace(parts[2])
		path, err := resolveArtifactPathByKind(objectRoot, artifactKind, variant)
		if err != nil {
			return "", "", "", err
		}
		return path, artifactKind, variant, nil
	}
	if len(parts) != 4 {
		return "", "", "", fmt.Errorf("invalid archive_file_key format")
	}
	artifactKind := strings.TrimSpace(parts[1])
	variant := strings.TrimSpace(parts[2])
	relPath := strings.TrimSpace(parts[3])
	if relPath == "" {
		return "", "", "", fmt.Errorf("invalid archive_file_key missing relative path")
	}
	absPath := filepath.Join(objectRoot, filepath.FromSlash(relPath))
	if _, err := os.Stat(absPath); err != nil {
		return "", "", "", err
	}
	return absPath, artifactKind, variant, nil
}

func resolveArtifactPathByKind(objectRoot, artifactKind, variant string) (string, error) {
	type target struct {
		dir  string
		exts map[string]struct{}
	}
	var targets []target
	expectedVariant := ""
	switch artifactKind {
	case "pdf":
		targets = []target{{dir: filepath.Join(objectRoot, "derivatives", "access"), exts: map[string]struct{}{".pdf": {}}}}
		expectedVariant = "access_v1"
	case "thumbnail":
		targets = []target{{dir: filepath.Join(objectRoot, "derivatives", "images", "thumb"), exts: map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}}}}
		expectedVariant = "thumb_v1"
	case "web_version":
		targets = []target{{dir: filepath.Join(objectRoot, "derivatives", "images", "web"), exts: map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}}}}
		expectedVariant = "web_v1"
	case "ocr_text":
		targets = []target{{dir: filepath.Join(objectRoot, "ocr"), exts: map[string]struct{}{".txt": {}}}}
		expectedVariant = "ocr_text_v1"
	case "original":
		targets = []target{{dir: filepath.Join(objectRoot, "original"), exts: map[string]struct{}{}}}
		expectedVariant = "original_v1"
	default:
		return "", fmt.Errorf("unsupported artifact_kind: %s", artifactKind)
	}
	if strings.TrimSpace(variant) != "" && expectedVariant != "" && strings.TrimSpace(variant) != expectedVariant {
		return "", os.ErrNotExist
	}

	for _, target := range targets {
		candidate, err := findFirstMatchingFile(target.dir, target.exts)
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return "", err
	}

	return "", os.ErrNotExist
}

func findFirstMatchingFile(dir string, allowedExts map[string]struct{}) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			nested, nestedErr := findFirstMatchingFile(path, allowedExts)
			if nestedErr == nil {
				candidates = append(candidates, nested)
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if len(allowedExts) > 0 {
			if _, ok := allowedExts[ext]; !ok {
				continue
			}
		}
		if len(allowedExts) == 0 && ext == "" {
			continue
		}
		candidates = append(candidates, path)
	}
	if len(candidates) == 0 {
		return "", os.ErrNotExist
	}
	sort.Strings(candidates)
	return candidates[0], nil
}

func deliverBackendObjectTask(ctx context.Context, d *db.DB, client *vps.Client, cfg config.Config, task db.BackendObjectTask) error {
	switch task.ActionType {
	case availableFilesSnapshotAction:
		objectRoot, err := d.GetObjectRoot(ctx, task.ObjectID)
		if err != nil {
			return markBackendObjectTaskFailed(ctx, d, task, err.Error())
		}
		files, err := buildAvailableFilesSnapshot(task.ObjectID, objectRoot, cfg.PublishOriginalsAvailableFiles)
		if err != nil {
			return markBackendObjectTaskFailed(ctx, d, task, err.Error())
		}
		if err := client.PutAvailableFiles(ctx, task.ObjectID, files); err != nil {
			return markBackendObjectTaskFailed(ctx, d, task, err.Error())
		}
		return d.MarkBackendObjectTaskSent(ctx, task.TaskID)
	default:
		return markBackendObjectTaskFailed(ctx, d, task, fmt.Sprintf("unsupported action_type: %s", task.ActionType))
	}
}

func markBackendObjectTaskFailed(ctx context.Context, d *db.DB, task db.BackendObjectTask, errMsg string) error {
	nextAttempt := computeNextAttempt(task.Attempts)
	return d.MarkBackendObjectTaskFailed(ctx, task.TaskID, errMsg, nextAttempt)
}

func buildAvailableFilesSnapshot(objectID, objectRoot string, includeOriginals bool) ([]vps.AvailableFile, error) {
	files := make([]vps.AvailableFile, 0)

	type scanTarget struct {
		relDir       string
		artifactKind string
		variant      string
		displayName  string
		exts         map[string]struct{}
	}

	targets := []scanTarget{
		{
			relDir:       filepath.Join("derivatives", "access"),
			artifactKind: "pdf",
			variant:      "access_v1",
			displayName:  "Access PDF",
			exts:         map[string]struct{}{".pdf": {}},
		},
		{
			relDir:       filepath.Join("derivatives", "images", "thumb"),
			artifactKind: "thumbnail",
			variant:      "thumb_v1",
			displayName:  "Thumbnail",
			exts:         map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}},
		},
		{
			relDir:       filepath.Join("derivatives", "images", "web"),
			artifactKind: "web_version",
			variant:      "web_v1",
			displayName:  "Web Version",
			exts:         map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}},
		},
		{
			relDir:       "ocr",
			artifactKind: "ocr_text",
			variant:      "ocr_text_v1",
			displayName:  "OCR Text",
			exts:         map[string]struct{}{".txt": {}},
		},
	}
	if includeOriginals {
		targets = append(targets, scanTarget{
			relDir:       "original",
			artifactKind: "original",
			variant:      "original_v1",
			displayName:  "Original File",
			exts:         map[string]struct{}{},
		})
	}

	for _, target := range targets {
		dir := filepath.Join(objectRoot, target.relDir)
		path, err := findFirstMatchingFile(dir, target.exts)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		relPath, err := filepath.Rel(objectRoot, path)
		if err != nil {
			return nil, err
		}
		relPath = filepath.ToSlash(relPath)
		variant := target.variant
		archiveFileKey := fmt.Sprintf("%s:%s:%s", objectID, target.artifactKind, variant)
		contentType := contentTypeFromFilename(path)
		sizeBytes := info.Size()
		files = append(files, vps.AvailableFile{
			ArchiveFileKey: archiveFileKey,
			ArtifactKind:   target.artifactKind,
			Variant:        &variant,
			DisplayName:    target.displayName,
			ContentType:    &contentType,
			SizeBytes:      &sizeBytes,
			Metadata: map[string]any{
				"source": "archive-system",
				"path":   relPath,
			},
			IsAvailable: true,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ArchiveFileKey < files[j].ArchiveFileKey
	})

	return files, nil
}

func contentTypeFromFilename(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
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

func deliverEvent(ctx context.Context, d *db.DB, client *vps.Client, ev db.VPSEvent) error {
	leaseID, leaseToken, leaseExpiresAt, _, err := d.GetIngestionLease(ctx, ev.IngestionID)
	if err != nil {
		log.Printf("get lease for ingestion %s failed: %v", ev.IngestionID, err)
		return markEventFailed(ctx, d, ev, err.Error())
	}
	if leaseToken == "" {
		return markEventFailed(ctx, d, ev, "no lease token found")
	}
	if !isLeaseValid(leaseExpiresAt) {
		lease, err := client.LeaseIngestion(ctx, ev.IngestionID)
		if err != nil {
			log.Printf("lease ingestion %s failed: %v", ev.IngestionID, err)
			return markEventFailed(ctx, d, ev, err.Error())
		}
		if lease != nil {
			leaseToken = lease.LeaseToken
			leaseID = lease.LeaseID
			leaseExpiresAt = lease.LeaseExpiresAt
			if err := d.UpsertIngestionLease(ctx, ev.IngestionID, leaseID, leaseToken, leaseExpiresAt); err != nil {
				log.Printf("upsert lease after refresh failed: %v", err)
			}
		}
	}

	event := vps.Event{
		EventID:   ev.EventID,
		EventType: ev.EventType,
		ObjectID:  ev.ObjectID.String,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &payload); err != nil {
		log.Printf("unmarshal payload failed: %v", err)
		return markEventFailed(ctx, d, ev, err.Error())
	}
	event.Payload = payload

	if err := client.PostEvents(ctx, ev.IngestionID, leaseToken, []vps.Event{event}); err != nil {
		log.Printf("post event to vps failed: %v", err)
		return markEventFailed(ctx, d, ev, err.Error())
	}

	if err := d.MarkVPSEventSent(ctx, ev.EventID); err != nil {
		log.Printf("mark event sent failed: %v", err)
	}
	return nil
}

func markEventFailed(ctx context.Context, d *db.DB, ev db.VPSEvent, errMsg string) error {
	nextAttempt := computeNextAttempt(ev.Attempts)
	return d.MarkVPSEventFailed(ctx, ev.EventID, errMsg, nextAttempt)
}

func computeNextAttempt(attempts int) string {
	baseDelay := 10 * time.Second
	maxDelay := 24 * time.Hour

	delay := baseDelay
	for i := 0; i < attempts; i++ {
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
			break
		}
	}

	jitter := time.Duration(rand.Int63n(int64(delay / 4)))
	next := time.Now().UTC().Add(delay + jitter)
	return next.Format(time.RFC3339)
}

func isLeaseValid(leaseExpiresAt string) bool {
	if leaseExpiresAt == "" {
		return false
	}
	expTime, err := time.Parse(time.RFC3339, leaseExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().UTC().Before(expTime)
}
