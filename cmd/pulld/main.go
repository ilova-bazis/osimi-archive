package main

import (
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
	"strconv"
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
	mu         sync.RWMutex
	leaseToken string
}

var errDropNotFound = errors.New("drop not found")

const availableFilesSnapshotAction = "available_files_snapshot"
const objectResyncAction = "object_resync"

var archiveSleepWithContext = sleepWithContext
var archiveJitterDuration = jitterDuration
var newArchiveHeartbeatTicker = time.NewTicker

type archiveRequestDB interface {
	GetObjectRoot(ctx context.Context, objectID string) (string, error)
}

type archiveRequestClient interface {
	LeaseArchiveRequest(ctx context.Context, actionType string) (*vps.ArchiveRequest, error)
	HeartbeatArchiveRequest(ctx context.Context, requestID, leaseToken string) (*vps.ArchiveRequest, error)
	ReleaseArchiveRequest(ctx context.Context, requestID, leaseToken string) error
	CompleteArchiveRequest(ctx context.Context, requestID, leaseToken string) error
	FailArchiveRequest(ctx context.Context, requestID, leaseToken string, payload vps.ArchiveRequestFailPayload) error
	PutAvailableFiles(ctx context.Context, objectID string, files []vps.AvailableFile) error
}

var newDownloadHeartbeatTicker = time.NewTicker

type downloadRequestDB interface {
	GetObjectRoot(ctx context.Context, objectID string) (string, error)
}

type downloadRequestClient interface {
	HeartbeatDownloadRequest(ctx context.Context, requestID, leaseToken string) (*vps.DownloadRequest, error)
	ReleaseDownloadRequest(ctx context.Context, requestID, leaseToken string) error
	PresignDownloadArtifact(ctx context.Context, requestID, leaseToken string, req vps.DownloadPresignRequest) (*vps.DownloadArtifactPresignResponse, error)
	UploadDownloadRequestArtifact(ctx context.Context, uploadPath, contentType string, sizeBytes int64, headers vps.DownloadArtifactPresignHeaders, body io.Reader) error
	CompleteDownloadRequest(ctx context.Context, requestID, leaseToken, uploadToken string) error
	FailDownloadRequest(ctx context.Context, requestID, leaseToken string, payload vps.DownloadFailPayload) error
}

func (s *leaseState) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leaseToken
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
}

type downloadItem struct {
	FileName string
	Path     string
}

type downloadRequestLeaseState struct {
	mu         sync.RWMutex
	leaseToken string
}

type archiveRequestLeaseState struct {
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

func (s *archiveRequestLeaseState) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leaseToken
}

func (s *archiveRequestLeaseState) Update(req *vps.ArchiveRequest) {
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
	go archiveRequestsLoop(ctx, d, client, cfg)

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
				log.Printf("[VERBOSE] lease acquired: ingestion_id=%s batch_label=%s tenant_id=%s lease_expires_at=%s item_count=%d file_count=%d",
					lease.IngestionID, lease.BatchLabel, lease.TenantID, lease.LeaseExpiresAt, len(lease.Items), countLeaseFiles(lease.Items))
			}
			if err := handleLease(ctx, d, cfg, client, lease); err != nil {
				log.Printf("lease handling failed: %v", err)
			}
		}
	}
}

func handleLease(ctx context.Context, d *db.DB, cfg config.Config, client *vps.Client, lease *vps.Lease) error {
	state := &leaseState{leaseToken: lease.LeaseToken}

	if err := d.UpsertIngestionLease(ctx, lease.IngestionID, lease.LeaseID, lease.LeaseToken, lease.LeaseExpiresAt); err != nil {
		log.Printf("failed to upsert lease: %v", err)
	}

	if cfg.Verbose {
		log.Printf("[VERBOSE] handling lease for ingestion_id=%s", lease.IngestionID)
		for _, item := range lease.Items {
			log.Printf("[VERBOSE] lease item: ingestion_item_id=%s item_index=%d file_count=%d",
				item.IngestionItemID, item.ItemIndex, len(item.Files))
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
	if err := d.SeedIngestionRun(ctx, lease.IngestionID, lease.LeaseID, leaseItemIDs(lease.Items)); err != nil {
		postFailure(ctx, d, lease.IngestionID, "ingestion_run_seed_failed", err)
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	if err := downloadBatch(ctx, cfg, client, lease, tmpDir, state.Token(), cfg.DoneMarker); err != nil {
		postFailure(ctx, d, lease.IngestionID, "download_failed", err)
		return err
	}

	if err := os.Rename(tmpDir, finalDir); err != nil {
		postFailure(ctx, d, lease.IngestionID, "drop_finalize_failed", err)
		return err
	}
	if cfg.Verbose {
		log.Printf("[VERBOSE] drop finalized: %s", finalDir)
	}
	cleanup = false

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

func countLeaseFiles(items []vps.LeaseItem) int {
	total := 0
	for _, item := range items {
		total += len(item.Files)
	}
	return total
}

func leaseItemIDs(items []vps.LeaseItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.IngestionItemID)
	}
	return ids
}

func buildItemDirName(item vps.LeaseItem) string {
	label := fmt.Sprintf("item_%03d", item.ItemIndex)
	if strings.TrimSpace(item.IngestionItemID) != "" {
		label += "__ITEM-" + sanitizeName(item.IngestionItemID)
	}
	return label
}

func itemBatchReady(dir string, doneMarker string) bool {
	if _, err := os.Stat(filepath.Join(dir, doneMarker)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "ENQUEUED")); err == nil {
		return true
	}
	return strings.Contains(filepath.Base(dir), "__IMPORTED__")
}

func itemBatchFailed(dir string) (string, bool) {
	content, err := readErrorFile(filepath.Join(dir, "ERROR"))
	if err != nil {
		return "", false
	}
	return content, true
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

	entries, err := os.ReadDir(finalDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := os.RemoveAll(finalDir); err != nil {
			return fmt.Errorf("cleanup incomplete drop: %w", err)
		}
		return errDropNotFound
	}

	hasReadyBatch := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(finalDir, entry.Name())
		if content, ok := itemBatchFailed(path); ok {
			if postErr := postIngestionFailed(ctx, d, ingestionID, content); postErr != nil {
				return postErr
			}
			return nil
		}
		if itemBatchReady(path, "DONE") {
			hasReadyBatch = true
		}
	}
	if !hasReadyBatch {
		if err := os.RemoveAll(finalDir); err != nil {
			return fmt.Errorf("cleanup incomplete drop: %w", err)
		}
		return errDropNotFound
	}

	if err := postIngestionProcessing(ctx, d, ingestionID); err != nil {
		return err
	}
	return nil
}

func downloadBatch(ctx context.Context, cfg config.Config, client *vps.Client, lease *vps.Lease, dstDir, leaseToken, doneMarker string) error {
	if len(lease.Items) == 0 {
		return fmt.Errorf("lease contains no items to ingest")
	}
	if cfg.Verbose {
		log.Printf("[VERBOSE] materializing %d items to %s", len(lease.Items), dstDir)
	}

	for _, item := range lease.Items {
		if strings.TrimSpace(item.IngestionItemID) == "" {
			return fmt.Errorf("lease item missing ingestion_item_id")
		}
		if len(item.Files) == 0 {
			return fmt.Errorf("lease item %s contains no files", item.IngestionItemID)
		}

		itemDir := filepath.Join(dstDir, buildItemDirName(item))
		if err := os.MkdirAll(itemDir, 0755); err != nil {
			return err
		}
		if err := writeCatalog(itemDir, item.CatalogJSON); err != nil {
			return err
		}
		if err := writeLeaseMetadata(itemDir, lease.IngestionID, item.IngestionItemID, leaseToken); err != nil {
			return err
		}

		usedNames := map[string]int{}
		for i, file := range item.Files {
			expectedChecksum, err := normalizeSHA256(file.ChecksumSHA256)
			if err != nil {
				return fmt.Errorf("invalid checksum for item=%s file_id=%s storage_key=%s: %w", item.IngestionItemID, file.FileID, file.StorageKey, err)
			}

			ext := strings.ToLower(filepath.Ext(file.Filename))
			if ext == "" {
				ext = strings.ToLower(filepath.Ext(file.StorageKey))
			}
			name := fmt.Sprintf("file_%04d-original%s", i+1, ext)
			if count := usedNames[name]; count > 0 {
				name = fmt.Sprintf("file_%04d-original_%d%s", i+1, count+1, ext)
			}
			usedNames[name]++

			if cfg.Verbose {
				log.Printf("[VERBOSE] download item=%s file_id=%s filename=%s sort_order=%d",
					item.IngestionItemID, file.FileID, file.Filename, file.SortOrder)
			}

			path := filepath.Join(itemDir, name)
			if err := downloadToFile(ctx, client, file.DownloadURL, path); err != nil {
				return err
			}
			actualChecksum, err := sha256File(path)
			if err != nil {
				return err
			}
			if !strings.EqualFold(actualChecksum, expectedChecksum) {
				return fmt.Errorf("checksum mismatch for item=%s file_id=%s storage_key=%s: expected=%s actual=%s", item.IngestionItemID, file.FileID, file.StorageKey, expectedChecksum, actualChecksum)
			}
		}

		if err := os.WriteFile(filepath.Join(itemDir, doneMarker), []byte("ok\n"), 0644); err != nil {
			return err
		}
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

func writeLeaseMetadata(dstDir, ingestionID, ingestionItemID, leaseToken string) error {
	if strings.TrimSpace(ingestionID) == "" {
		return fmt.Errorf("missing ingestion id")
	}
	if strings.TrimSpace(ingestionItemID) == "" {
		return fmt.Errorf("missing ingestion item id")
	}
	if strings.TrimSpace(leaseToken) == "" {
		return fmt.Errorf("missing lease token")
	}
	if err := os.WriteFile(filepath.Join(dstDir, "INGESTION_ID"), []byte(ingestionID+"\n"), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dstDir, "INGESTION_ITEM_ID"), []byte(ingestionItemID+"\n"), 0644); err != nil {
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

func normalizeSHA256(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("missing checksum_sha256")
	}
	if len(value) != 64 {
		return "", fmt.Errorf("checksum_sha256 must be 64 hex chars")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("checksum_sha256 must be hex")
	}
	return strings.ToLower(value), nil
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

func archiveRequestsLoop(ctx context.Context, d archiveRequestDB, client archiveRequestClient, cfg config.Config) {
	log.Println("archive requests loop started")

	errorSleep := cfg.ArchiveRequestPollInterval
	if errorSleep <= 0 {
		errorSleep = 5 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("archive requests loop stopped")
			return
		default:
		}

		req, err := client.LeaseArchiveRequest(ctx, objectResyncAction)
		if err != nil {
			log.Printf("lease archive request failed: %v", err)
			if !archiveSleepWithContext(ctx, errorSleep) {
				log.Println("archive requests loop stopped")
				return
			}
			continue
		}

		if req == nil {
			if !archiveSleepWithContext(ctx, archiveJitterDuration(2*time.Second, 10*time.Second)) {
				log.Println("archive requests loop stopped")
				return
			}
			continue
		}

		if err := processArchiveRequest(ctx, d, cfg, client, req); err != nil {
			log.Printf("process archive request failed: %v", err)
		}
	}
}

func processDownloadRequest(ctx context.Context, d downloadRequestDB, cfg config.Config, client downloadRequestClient, req *vps.DownloadRequest) error {
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
		if err := client.ReleaseDownloadRequest(ctx, requestID, token); err != nil && !isHTTPStatus(err, 409) {
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
		if isHTTPStatus(err, 409) {
			completed = true
			return nil
		}
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

func downloadRequestHeartbeatLoop(ctx context.Context, cfg config.Config, client downloadRequestClient, requestID string, state *downloadRequestLeaseState) {
	if cfg.DownloadRequestHeartbeatInterval <= 0 {
		return
	}
	ticker := newDownloadHeartbeatTicker(cfg.DownloadRequestHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := client.HeartbeatDownloadRequest(ctx, requestID, state.Token())
			if err != nil {
				if !isHTTPStatus(err, 409) {
					log.Printf("download request heartbeat failed: request_id=%s err=%v", requestID, err)
				}
				continue
			}
			state.Update(req)
		}
	}
}

func failDownloadRequest(ctx context.Context, client downloadRequestClient, requestID, leaseToken string, payload vps.DownloadFailPayload) error {
	if err := client.FailDownloadRequest(ctx, requestID, leaseToken, payload); err != nil {
		if isHTTPStatus(err, 409) {
			return nil
		}
		return fmt.Errorf("report download request failure failed: %w", err)
	}
	return fmt.Errorf("download request %s failed: %s", requestID, payload.Message)
}

func processArchiveRequest(ctx context.Context, d archiveRequestDB, cfg config.Config, client archiveRequestClient, req *vps.ArchiveRequest) error {
	if req == nil {
		return nil
	}
	requestID := strings.TrimSpace(req.EffectiveRequestID())
	if requestID == "" {
		return fmt.Errorf("archive request missing id")
	}
	if strings.TrimSpace(req.TargetID) == "" {
		return fmt.Errorf("archive request %s missing target_id", requestID)
	}

	state := &archiveRequestLeaseState{leaseToken: req.LeaseToken}
	completed := false
	defer func() {
		if completed {
			return
		}
		token := state.Token()
		if token == "" {
			return
		}
		if err := client.ReleaseArchiveRequest(ctx, requestID, token); err != nil && !isHTTPStatus(err, 409) {
			log.Printf("release archive request failed: request_id=%s err=%v", requestID, err)
		}
	}()

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go archiveRequestHeartbeatLoop(heartbeatCtx, cfg, client, requestID, state)

	actionType := strings.TrimSpace(req.ActionType)
	switch actionType {
	case objectResyncAction:
		objectRoot, err := d.GetObjectRoot(ctx, req.TargetID)
		if err != nil {
			return failArchiveRequest(ctx, client, requestID, state.Token(), vps.ArchiveRequestFailPayload{
				Code:      "OBJECT_NOT_FOUND",
				Message:   err.Error(),
				Retryable: false,
			})
		}

		files, err := buildAvailableFilesSnapshot(req.TargetID, objectRoot, cfg.PublishOriginalsAvailableFiles)
		if err != nil {
			return failArchiveRequest(ctx, client, requestID, state.Token(), vps.ArchiveRequestFailPayload{
				Code:      "SNAPSHOT_BUILD_FAILED",
				Message:   err.Error(),
				Retryable: true,
			})
		}

		if err := client.PutAvailableFiles(ctx, req.TargetID, files); err != nil {
			return failArchiveRequest(ctx, client, requestID, state.Token(), vps.ArchiveRequestFailPayload{
				Code:      "SNAPSHOT_SYNC_FAILED",
				Message:   err.Error(),
				Retryable: true,
			})
		}
	default:
		return failArchiveRequest(ctx, client, requestID, state.Token(), vps.ArchiveRequestFailPayload{
			Code:      "UNSUPPORTED_ACTION",
			Message:   fmt.Sprintf("unsupported action_type: %s", actionType),
			Retryable: false,
		})
	}

	if err := client.CompleteArchiveRequest(ctx, requestID, state.Token()); err != nil {
		if isHTTPStatus(err, 409) {
			completed = true
			return nil
		}
		return failArchiveRequest(ctx, client, requestID, state.Token(), vps.ArchiveRequestFailPayload{
			Code:      "COMPLETE_FAILED",
			Message:   err.Error(),
			Retryable: true,
		})
	}

	completed = true
	if cfg.Verbose {
		log.Printf("[VERBOSE] archive request completed: request_id=%s target_id=%s action_type=%s", requestID, req.TargetID, actionType)
	}
	return nil
}

func archiveRequestHeartbeatLoop(ctx context.Context, cfg config.Config, client archiveRequestClient, requestID string, state *archiveRequestLeaseState) {
	if cfg.ArchiveRequestHeartbeatInterval <= 0 {
		return
	}
	ticker := newArchiveHeartbeatTicker(cfg.ArchiveRequestHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := client.HeartbeatArchiveRequest(ctx, requestID, state.Token())
			if err != nil {
				if !isHTTPStatus(err, 409) {
					log.Printf("archive request heartbeat failed: request_id=%s err=%v", requestID, err)
				}
				continue
			}
			state.Update(req)
		}
	}
}

func failArchiveRequest(ctx context.Context, client archiveRequestClient, requestID, leaseToken string, payload vps.ArchiveRequestFailPayload) error {
	if err := client.FailArchiveRequest(ctx, requestID, leaseToken, payload); err != nil {
		if isHTTPStatus(err, 409) {
			return nil
		}
		return fmt.Errorf("report archive request failure failed: %w", err)
	}
	return fmt.Errorf("archive request %s failed: %s", requestID, payload.Message)
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
		switch strings.TrimSpace(variant) {
		case "", "access_v1":
			path := filepath.Join(objectRoot, "derivatives", "access", "reading_v1.pdf")
			if _, err := os.Stat(path); err != nil {
				return "", err
			}
			return path, nil
		case "access_ocr_v1":
			path := filepath.Join(objectRoot, "derivatives", "access", "reading_ocr_v1.pdf")
			if _, err := os.Stat(path); err != nil {
				return "", err
			}
			return path, nil
		default:
			return "", os.ErrNotExist
		}
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
		fileName     string
	}

	targets := []scanTarget{
		{
			relDir:       filepath.Join("derivatives", "access"),
			artifactKind: "pdf",
			variant:      "access_ocr_v1",
			displayName:  "Searchable PDF",
			exts:         map[string]struct{}{".pdf": {}},
			fileName:     "reading_ocr_v1.pdf",
		},
		{
			relDir:       filepath.Join("derivatives", "access"),
			artifactKind: "pdf",
			variant:      "access_v1",
			displayName:  "Access PDF",
			exts:         map[string]struct{}{".pdf": {}},
			fileName:     "reading_v1.pdf",
		},
		{
			relDir:       filepath.Join("derivatives", "images", "thumb"),
			artifactKind: "thumbnail",
			variant:      "thumb_v1",
			displayName:  "Thumbnail",
			exts:         map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}},
			fileName:     "",
		},
		{
			relDir:       filepath.Join("derivatives", "images", "web"),
			artifactKind: "web_version",
			variant:      "web_v1",
			displayName:  "Web Version",
			exts:         map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}},
			fileName:     "",
		},
		{
			relDir:       "ocr",
			artifactKind: "ocr_text",
			variant:      "ocr_text_v1",
			displayName:  "OCR Text",
			exts:         map[string]struct{}{".txt": {}},
			fileName:     "",
		},
	}
	if includeOriginals {
		targets = append(targets, scanTarget{
			relDir:       "original",
			artifactKind: "original",
			variant:      "original_v1",
			displayName:  "Original File",
			exts:         map[string]struct{}{},
			fileName:     "",
		})
	}

	for _, target := range targets {
		dir := filepath.Join(objectRoot, target.relDir)
		path, err := findArtifactFile(dir, target.fileName, target.exts)
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

func findArtifactFile(dir, fileName string, allowedExts map[string]struct{}) (string, error) {
	if strings.TrimSpace(fileName) != "" {
		path := filepath.Join(dir, fileName)
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return path, nil
	}
	return findFirstMatchingFile(dir, allowedExts)
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
		EventID:         ev.EventID,
		EventType:       ev.EventType,
		IngestionItemID: ev.IngestionItemID.String,
		ObjectID:        ev.ObjectID.String,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
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

func jitterDuration(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min)+1))
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func isHTTPStatus(err error, status int) bool {
	actual, ok := httpStatusCode(err)
	return ok && actual == status
}

func httpStatusCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	for i := 0; i+10 <= len(msg); i++ {
		if msg[i:i+7] != "status " {
			continue
		}
		code := msg[i+7 : i+10]
		if code[0] < '0' || code[0] > '9' || code[1] < '0' || code[1] > '9' || code[2] < '0' || code[2] > '9' {
			continue
		}
		v, convErr := strconv.Atoi(code)
		if convErr != nil {
			continue
		}
		return v, true
	}
	return 0, false
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
