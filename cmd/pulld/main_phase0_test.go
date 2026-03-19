package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ilova-bazis/osimi-archive/internal/config"
	"github.com/ilova-bazis/osimi-archive/internal/db"
	"github.com/ilova-bazis/osimi-archive/internal/vps"
)

type fakeArchiveDB struct {
	objectRoot string
	err        error
}

func (f fakeArchiveDB) GetObjectRoot(ctx context.Context, objectID string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.objectRoot, nil
}

type fakeArchiveClient struct {
	mu sync.Mutex

	leasedRequest *vps.ArchiveRequest
	leaseErr      error
	putErr        error
	completeErr   error
	releaseErr    error
	failErr       error

	leaseCalls       int
	putCalls         int
	completeCalls    int
	releaseCalls     int
	heartbeatCalls   int
	failCalls        int
	lastReleaseToken string
	failPayloads     []vps.ArchiveRequestFailPayload
	heartbeatTokens  []string
	heartbeatReplies []*vps.ArchiveRequest
}

type fakeDownloadDB struct {
	objectRoot string
	err        error
}

func (f fakeDownloadDB) GetObjectRoot(ctx context.Context, objectID string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.objectRoot, nil
}

type fakeDownloadClient struct {
	mu sync.Mutex

	presignResp *vps.DownloadArtifactPresignResponse
	heartbeat   *vps.DownloadRequest

	heartbeatErr error
	releaseErr   error
	completeErr  error
	failErr      error
	uploadErr    error

	heartbeatCalls int
	releaseCalls   int
	presignCalls   int
	uploadCalls    int
	completeCalls  int
	failCalls      int

	heartbeatTokens []string
}

func (f *fakeDownloadClient) HeartbeatDownloadRequest(ctx context.Context, requestID, leaseToken string) (*vps.DownloadRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeatCalls++
	f.heartbeatTokens = append(f.heartbeatTokens, leaseToken)
	if f.heartbeatErr != nil {
		return nil, f.heartbeatErr
	}
	if f.heartbeat != nil {
		return f.heartbeat, nil
	}
	return &vps.DownloadRequest{LeaseToken: leaseToken}, nil
}

func (f *fakeDownloadClient) ReleaseDownloadRequest(ctx context.Context, requestID, leaseToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	return f.releaseErr
}

func (f *fakeDownloadClient) PresignDownloadArtifact(ctx context.Context, requestID, leaseToken string, req vps.DownloadPresignRequest) (*vps.DownloadArtifactPresignResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presignCalls++
	if f.presignResp != nil {
		return f.presignResp, nil
	}
	return &vps.DownloadArtifactPresignResponse{UploadPath: "/upload", UploadToken: "upload-token"}, nil
}

func (f *fakeDownloadClient) UploadDownloadRequestArtifact(ctx context.Context, uploadPath, contentType string, sizeBytes int64, headers vps.DownloadArtifactPresignHeaders, body io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadCalls++
	return f.uploadErr
}

func (f *fakeDownloadClient) CompleteDownloadRequest(ctx context.Context, requestID, leaseToken, uploadToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeCalls++
	return f.completeErr
}

func (f *fakeDownloadClient) FailDownloadRequest(ctx context.Context, requestID, leaseToken string, payload vps.DownloadFailPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCalls++
	return f.failErr
}

func (f *fakeArchiveClient) LeaseArchiveRequest(ctx context.Context, actionType string) (*vps.ArchiveRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaseCalls++
	if f.leaseErr != nil {
		return nil, f.leaseErr
	}
	return f.leasedRequest, nil
}

func (f *fakeArchiveClient) HeartbeatArchiveRequest(ctx context.Context, requestID, leaseToken string) (*vps.ArchiveRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeatCalls++
	f.heartbeatTokens = append(f.heartbeatTokens, leaseToken)
	if len(f.heartbeatReplies) > 0 {
		reply := f.heartbeatReplies[0]
		f.heartbeatReplies = f.heartbeatReplies[1:]
		if reply != nil {
			return reply, nil
		}
	}
	return &vps.ArchiveRequest{LeaseToken: leaseToken}, nil
}

func (f *fakeArchiveClient) ReleaseArchiveRequest(ctx context.Context, requestID, leaseToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.lastReleaseToken = leaseToken
	return f.releaseErr
}

func (f *fakeArchiveClient) CompleteArchiveRequest(ctx context.Context, requestID, leaseToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeCalls++
	return f.completeErr
}

func (f *fakeArchiveClient) FailArchiveRequest(ctx context.Context, requestID, leaseToken string, payload vps.ArchiveRequestFailPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCalls++
	f.failPayloads = append(f.failPayloads, payload)
	return f.failErr
}

func (f *fakeArchiveClient) PutAvailableFiles(ctx context.Context, objectID string, files []vps.AvailableFile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls++
	return f.putErr
}

func TestHTTPStatusCode(t *testing.T) {
	if !isHTTPStatus(errors.New("vps POST /api/archive-requests/123/complete: status 409: conflict"), 409) {
		t.Fatalf("expected status classifier to match 409")
	}
	if isHTTPStatus(errors.New("no status available"), 409) {
		t.Fatalf("did not expect status classifier to match")
	}
	if code, ok := httpStatusCode(errors.New("status 401: unauthorized")); !ok || code != 401 {
		t.Fatalf("expected status code 401, got code=%d ok=%v", code, ok)
	}
}

func TestProcessArchiveRequest_Table(t *testing.T) {
	makeReq := func() *vps.ArchiveRequest {
		return &vps.ArchiveRequest{
			RequestID:  "req-1",
			LeaseToken: "tok-1",
			TargetID:   "OBJ-20260312-TEST01",
			ActionType: objectResyncAction,
		}
	}

	tests := []struct {
		name              string
		req               *vps.ArchiveRequest
		db                fakeArchiveDB
		client            *fakeArchiveClient
		wantErr           bool
		errContains       string
		wantFailCode      string
		wantFailRetryable bool
		wantReleaseCalls  int
		wantCompleteCalls int
		wantPutCalls      int
	}{
		{
			name:              "success",
			req:               makeReq(),
			db:                fakeArchiveDB{objectRoot: t.TempDir()},
			client:            &fakeArchiveClient{},
			wantErr:           false,
			wantReleaseCalls:  0,
			wantCompleteCalls: 1,
			wantPutCalls:      1,
		},
		{
			name: "missing_request_id",
			req: &vps.ArchiveRequest{
				LeaseToken: "tok-1",
				TargetID:   "OBJ-20260312-TEST01",
				ActionType: objectResyncAction,
			},
			db:               fakeArchiveDB{objectRoot: t.TempDir()},
			client:           &fakeArchiveClient{},
			wantErr:          true,
			errContains:      "archive request missing id",
			wantReleaseCalls: 0,
		},
		{
			name: "missing_target_id",
			req: &vps.ArchiveRequest{
				RequestID:  "req-1",
				LeaseToken: "tok-1",
				ActionType: objectResyncAction,
			},
			db:               fakeArchiveDB{objectRoot: t.TempDir()},
			client:           &fakeArchiveClient{},
			wantErr:          true,
			errContains:      "missing target_id",
			wantReleaseCalls: 0,
		},
		{
			name: "unsupported_action",
			req: &vps.ArchiveRequest{
				RequestID:  "req-1",
				LeaseToken: "tok-1",
				TargetID:   "OBJ-20260312-TEST01",
				ActionType: "artifact_fetch",
			},
			db:                fakeArchiveDB{objectRoot: t.TempDir()},
			client:            &fakeArchiveClient{},
			wantErr:           true,
			errContains:       "archive request req-1 failed",
			wantFailCode:      "UNSUPPORTED_ACTION",
			wantFailRetryable: false,
			wantReleaseCalls:  1,
			wantCompleteCalls: 0,
			wantPutCalls:      0,
		},
		{
			name:              "object_not_found",
			req:               makeReq(),
			db:                fakeArchiveDB{err: errors.New("missing object")},
			client:            &fakeArchiveClient{},
			wantErr:           true,
			errContains:       "archive request req-1 failed",
			wantFailCode:      "OBJECT_NOT_FOUND",
			wantFailRetryable: false,
			wantReleaseCalls:  1,
			wantCompleteCalls: 0,
			wantPutCalls:      0,
		},
		{
			name: "snapshot_build_failed",
			req:  makeReq(),
			db: func() fakeArchiveDB {
				filePath := t.TempDir() + "/not_a_dir"
				if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
					t.Fatalf("write fixture file: %v", err)
				}
				return fakeArchiveDB{objectRoot: filePath}
			}(),
			client:            &fakeArchiveClient{},
			wantErr:           true,
			errContains:       "archive request req-1 failed",
			wantFailCode:      "SNAPSHOT_BUILD_FAILED",
			wantFailRetryable: true,
			wantReleaseCalls:  1,
			wantCompleteCalls: 0,
			wantPutCalls:      0,
		},
		{
			name:              "snapshot_sync_failed",
			req:               makeReq(),
			db:                fakeArchiveDB{objectRoot: t.TempDir()},
			client:            &fakeArchiveClient{putErr: errors.New("upstream unavailable")},
			wantErr:           true,
			errContains:       "archive request req-1 failed",
			wantFailCode:      "SNAPSHOT_SYNC_FAILED",
			wantFailRetryable: true,
			wantReleaseCalls:  1,
			wantCompleteCalls: 0,
			wantPutCalls:      1,
		},
		{
			name:              "complete_conflict_409",
			req:               makeReq(),
			db:                fakeArchiveDB{objectRoot: t.TempDir()},
			client:            &fakeArchiveClient{completeErr: errors.New("vps POST /api/archive-requests/req-1/complete: status 409: conflict")},
			wantErr:           false,
			wantReleaseCalls:  0,
			wantCompleteCalls: 1,
			wantPutCalls:      1,
		},
		{
			name:              "complete_failure_reports_fail",
			req:               makeReq(),
			db:                fakeArchiveDB{objectRoot: t.TempDir()},
			client:            &fakeArchiveClient{completeErr: errors.New("vps POST /api/archive-requests/req-1/complete: status 500: boom")},
			wantErr:           true,
			errContains:       "archive request req-1 failed",
			wantFailCode:      "COMPLETE_FAILED",
			wantFailRetryable: true,
			wantReleaseCalls:  1,
			wantCompleteCalls: 1,
			wantPutCalls:      1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{ArchiveRequestHeartbeatInterval: 0}
			err := processArchiveRequest(context.Background(), tc.db, cfg, tc.client, tc.req)

			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.errContains != "" && (err == nil || !strings.Contains(err.Error(), tc.errContains)) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}

			if tc.client.releaseCalls != tc.wantReleaseCalls {
				t.Fatalf("release calls mismatch: got %d want %d", tc.client.releaseCalls, tc.wantReleaseCalls)
			}
			if tc.client.completeCalls != tc.wantCompleteCalls {
				t.Fatalf("complete calls mismatch: got %d want %d", tc.client.completeCalls, tc.wantCompleteCalls)
			}
			if tc.client.putCalls != tc.wantPutCalls {
				t.Fatalf("put calls mismatch: got %d want %d", tc.client.putCalls, tc.wantPutCalls)
			}

			if tc.wantFailCode == "" {
				if tc.client.failCalls != 0 {
					t.Fatalf("expected no fail calls, got %d", tc.client.failCalls)
				}
				return
			}

			if tc.client.failCalls != 1 {
				t.Fatalf("expected one fail call, got %d", tc.client.failCalls)
			}
			if len(tc.client.failPayloads) != 1 {
				t.Fatalf("expected one fail payload, got %d", len(tc.client.failPayloads))
			}
			payload := tc.client.failPayloads[0]
			if payload.Code != tc.wantFailCode {
				t.Fatalf("fail code mismatch: got %q want %q", payload.Code, tc.wantFailCode)
			}
			if payload.Retryable != tc.wantFailRetryable {
				t.Fatalf("fail retryable mismatch: got %v want %v", payload.Retryable, tc.wantFailRetryable)
			}
		})
	}
}

func TestArchiveRequestsLoopUsesInjectedSleepAndJitter(t *testing.T) {
	origSleep := archiveSleepWithContext
	origJitter := archiveJitterDuration
	t.Cleanup(func() {
		archiveSleepWithContext = origSleep
		archiveJitterDuration = origJitter
	})

	client := &fakeArchiveClient{leasedRequest: nil}
	d := fakeArchiveDB{}
	cfg := config.Config{ArchiveRequestPollInterval: time.Second}

	sleepCalls := 0
	jitterCalls := 0

	ctx, cancel := context.WithCancel(context.Background())
	archiveJitterDuration = func(min, max time.Duration) time.Duration {
		jitterCalls++
		return 0
	}
	archiveSleepWithContext = func(ctx context.Context, d time.Duration) bool {
		sleepCalls++
		cancel()
		return false
	}

	archiveRequestsLoop(ctx, d, client, cfg)

	if client.leaseCalls != 1 {
		t.Fatalf("expected one lease attempt, got %d", client.leaseCalls)
	}
	if jitterCalls != 1 {
		t.Fatalf("expected one jitter call, got %d", jitterCalls)
	}
	if sleepCalls != 1 {
		t.Fatalf("expected one sleep call, got %d", sleepCalls)
	}
}

func TestArchiveRequestsLoopLeaseErrorUsesFallbackPollSleep(t *testing.T) {
	origSleep := archiveSleepWithContext
	origJitter := archiveJitterDuration
	t.Cleanup(func() {
		archiveSleepWithContext = origSleep
		archiveJitterDuration = origJitter
	})

	client := &fakeArchiveClient{leaseErr: errors.New("boom")}
	d := fakeArchiveDB{}
	cfg := config.Config{ArchiveRequestPollInterval: 3 * time.Second}

	var slept []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	archiveJitterDuration = func(min, max time.Duration) time.Duration {
		t.Fatalf("did not expect jitter function on lease error path")
		return 0
	}
	archiveSleepWithContext = func(ctx context.Context, d time.Duration) bool {
		slept = append(slept, d)
		cancel()
		return false
	}

	archiveRequestsLoop(ctx, d, client, cfg)

	if client.leaseCalls != 1 {
		t.Fatalf("expected one lease attempt, got %d", client.leaseCalls)
	}
	if len(slept) != 1 {
		t.Fatalf("expected one sleep call, got %d", len(slept))
	}
	if slept[0] != 3*time.Second {
		t.Fatalf("expected fallback poll sleep of 3s, got %v", slept[0])
	}
}

func TestArchiveRequestsLoopStopsImmediatelyWhenContextCanceled(t *testing.T) {
	origSleep := archiveSleepWithContext
	origJitter := archiveJitterDuration
	t.Cleanup(func() {
		archiveSleepWithContext = origSleep
		archiveJitterDuration = origJitter
	})

	client := &fakeArchiveClient{}
	d := fakeArchiveDB{}
	cfg := config.Config{ArchiveRequestPollInterval: time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		archiveRequestsLoop(ctx, d, client, cfg)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("archiveRequestsLoop did not stop quickly on canceled context")
	}

	if client.leaseCalls != 0 {
		t.Fatalf("expected no lease calls on canceled context, got %d", client.leaseCalls)
	}
}

func TestArchiveRequestHeartbeatLoopRotatesToken(t *testing.T) {
	client := &fakeArchiveClient{
		heartbeatReplies: []*vps.ArchiveRequest{
			{LeaseToken: "tok-2"},
			{LeaseToken: "tok-3"},
		},
	}
	state := &archiveRequestLeaseState{leaseToken: "tok-1"}
	cfg := config.Config{ArchiveRequestHeartbeatInterval: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		archiveRequestHeartbeatLoop(ctx, cfg, client, "req-1", state)
		close(done)
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		client.mu.Lock()
		calls := client.heartbeatCalls
		tokens := append([]string(nil), client.heartbeatTokens...)
		client.mu.Unlock()
		if calls >= 2 {
			if len(tokens) < 2 {
				t.Fatalf("expected at least 2 heartbeat tokens, got %d", len(tokens))
			}
			if tokens[0] != "tok-1" {
				t.Fatalf("expected first heartbeat with tok-1, got %q", tokens[0])
			}
			if tokens[1] != "tok-2" {
				t.Fatalf("expected second heartbeat with rotated tok-2, got %q", tokens[1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat loop did not reach 2 calls in time")
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("heartbeat loop did not exit after cancellation")
	}

	if got := state.Token(); got != "tok-3" {
		t.Fatalf("expected state token to rotate to tok-3, got %q", got)
	}
}

func TestFailDownloadRequestConflict409ReturnsNil(t *testing.T) {
	client := &fakeDownloadClient{failErr: errors.New("vps POST /api/object-download-requests/req-1/fail: status 409: conflict")}
	err := failDownloadRequest(context.Background(), client, "req-1", "tok-1", vps.DownloadFailPayload{Code: "X", Message: "failed"})
	if err != nil {
		t.Fatalf("expected nil error on 409 conflict, got %v", err)
	}
	if client.failCalls != 1 {
		t.Fatalf("expected fail call once, got %d", client.failCalls)
	}
}

func TestProcessDownloadRequestCompleteConflictStopsWithoutFailOrRelease(t *testing.T) {
	root := t.TempDir()
	originalDir := filepath.Join(root, "original")
	if err := os.MkdirAll(originalDir, 0o755); err != nil {
		t.Fatalf("mkdir original: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originalDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	variant := "original_v1"
	req := &vps.DownloadRequest{
		RequestID:    "req-1",
		ObjectID:     "OBJ-20260312-TEST01",
		LeaseToken:   "tok-1",
		ArtifactKind: "original",
		Variant:      &variant,
	}

	d := fakeDownloadDB{objectRoot: root}
	client := &fakeDownloadClient{
		presignResp: &vps.DownloadArtifactPresignResponse{UploadPath: "/upload", UploadToken: "u-token"},
		completeErr: errors.New("vps POST /api/object-download-requests/req-1/complete: status 409: conflict"),
	}
	cfg := config.Config{DownloadRequestHeartbeatInterval: 0}

	err := processDownloadRequest(context.Background(), d, cfg, client, req)
	if err != nil {
		t.Fatalf("expected nil error on complete 409, got %v", err)
	}
	if client.failCalls != 0 {
		t.Fatalf("expected no fail call, got %d", client.failCalls)
	}
	if client.releaseCalls != 0 {
		t.Fatalf("expected no release call after complete conflict, got %d", client.releaseCalls)
	}
	if client.completeCalls != 1 {
		t.Fatalf("expected one complete call, got %d", client.completeCalls)
	}
}

func TestDownloadRequestHeartbeatLoopConflictDoesNotBreak(t *testing.T) {
	client := &fakeDownloadClient{heartbeatErr: errors.New("vps POST /api/object-download-requests/req-1/lease/heartbeat: status 409: conflict")}
	state := &downloadRequestLeaseState{leaseToken: "tok-1"}
	cfg := config.Config{DownloadRequestHeartbeatInterval: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		downloadRequestHeartbeatLoop(ctx, cfg, client, "req-1", state)
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("download heartbeat loop did not exit after cancellation")
	}

	if client.heartbeatCalls == 0 {
		t.Fatalf("expected at least one heartbeat call")
	}
	if got := state.Token(); got != "tok-1" {
		t.Fatalf("expected token to remain unchanged, got %q", got)
	}
}

func TestDownloadBatchPerFileChecksumValidation(t *testing.T) {
	content := []byte("hello world")
	validHash := sha256Hex(content)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file-1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(content)
	}))
	defer server.Close()

	client := vps.NewClient(server.URL, "worker-token", "worker-id")
	cfg := config.Config{}

	tests := []struct {
		name        string
		checksum    string
		wantErr     bool
		errContains string
	}{
		{name: "missing_checksum", checksum: "", wantErr: true, errContains: "missing checksum_sha256"},
		{name: "invalid_checksum_format", checksum: "xyz", wantErr: true, errContains: "must be 64 hex chars"},
		{name: "checksum_mismatch", checksum: strings.Repeat("a", 64), wantErr: true, errContains: "checksum mismatch"},
		{name: "checksum_valid", checksum: validHash, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lease := &vps.Lease{
				IngestionID: "ing-1",
				Items: []vps.LeaseItem{{
					IngestionItemID: "item-1",
					ItemIndex:       1,
					CatalogJSON:     json.RawMessage(`{"schema_version":"1.0","updated_at":"2026-01-01T00:00:00Z","access":{},"title":{"primary":"Test"},"classification":{"type":"image","language":"en"},"dates":{}}`),
					Files: []vps.LeaseItemFile{{
						FileID:         "file-1",
						Filename:       "file-1.bin",
						SortOrder:      1,
						StorageKey:     "original/file-1.bin",
						ContentType:    "application/octet-stream",
						SizeBytes:      int64(len(content)),
						ChecksumSHA256: tc.checksum,
						DownloadURL:    "/file-1",
					}},
				}},
			}

			dstDir := t.TempDir()
			err := downloadBatch(context.Background(), cfg, client, lease, dstDir, "lease-token", "DONE")
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.errContains != "" && (err == nil || !strings.Contains(err.Error(), tc.errContains)) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
			if tc.wantErr {
				return
			}
			itemDir := filepath.Join(dstDir, buildItemDirName(lease.Items[0]))
			if _, err := os.Stat(filepath.Join(itemDir, "file_0001-original.bin")); err != nil {
				t.Fatalf("expected downloaded file, got error: %v", err)
			}
			if _, err := os.Stat(filepath.Join(itemDir, "DONE")); err != nil {
				t.Fatalf("expected done marker, got error: %v", err)
			}
		})
	}
}

func TestChecksumManifestPathParseAndVerify(t *testing.T) {
	root := t.TempDir()
	dataPath := filepath.Join(root, "payload.txt")
	data := []byte("payload-data")
	if err := os.WriteFile(dataPath, data, 0o644); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	hash := sha256Hex(data)
	manifestPath := filepath.Join(root, "checksums.sha256.txt")
	if err := os.WriteFile(manifestPath, []byte(fmt.Sprintf("%s *payload.txt\n", hash)), 0o644); err != nil {
		t.Fatalf("write checksum manifest: %v", err)
	}

	expected, err := parseChecksumFile(manifestPath)
	if err != nil {
		t.Fatalf("parse checksum manifest failed: %v", err)
	}
	items := []downloadItem{{FileName: "payload.txt", Path: dataPath}}
	if err := verifyChecksums(items, expected); err != nil {
		t.Fatalf("verifyChecksums failed: %v", err)
	}
}

func TestChecksumManifestMalformedLineFails(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "checksums.sha256.txt")
	if err := os.WriteFile(manifestPath, []byte("badline\n"), 0o644); err != nil {
		t.Fatalf("write checksum manifest: %v", err)
	}

	_, err := parseChecksumFile(manifestPath)
	if err == nil {
		t.Fatalf("expected parse error for malformed checksum line")
	}
	if !strings.Contains(err.Error(), "invalid checksum line") {
		t.Fatalf("unexpected parse error: %v", err)
	}
}

func TestChecksumManifestMissingEntryFailsVerification(t *testing.T) {
	root := t.TempDir()
	dataPath := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(dataPath, []byte("payload-data"), 0o644); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	expected := map[string]string{"other.txt": strings.Repeat("a", 64)}
	items := []downloadItem{{FileName: "payload.txt", Path: dataPath}}
	err := verifyChecksums(items, expected)
	if err == nil {
		t.Fatalf("expected verifyChecksums error for missing manifest entry")
	}
	if !strings.Contains(err.Error(), "missing checksum for payload.txt") {
		t.Fatalf("unexpected verify error: %v", err)
	}
}

func TestChecksumManifestMismatchFailsVerification(t *testing.T) {
	root := t.TempDir()
	dataPath := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(dataPath, []byte("payload-data"), 0o644); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	expected := map[string]string{"payload.txt": strings.Repeat("a", 64)}
	items := []downloadItem{{FileName: "payload.txt", Path: dataPath}}
	err := verifyChecksums(items, expected)
	if err == nil {
		t.Fatalf("expected verifyChecksums mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch for payload.txt") {
		t.Fatalf("unexpected verify error: %v", err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestHandleExistingDropWithoutDoneRemovesDirectory(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	finalDir := filepath.Join(t.TempDir(), "drop")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatalf("mkdir final dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, "partial.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write partial file: %v", err)
	}

	err := handleExistingDrop(ctx, testDB, "ing-1", finalDir)
	if !errors.Is(err, errDropNotFound) {
		t.Fatalf("expected errDropNotFound, got %v", err)
	}
	if _, statErr := os.Stat(finalDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected final dir to be removed, stat err=%v", statErr)
	}
}

func TestHandleExistingDropDonePostsProcessingEvent(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	finalDir := filepath.Join(t.TempDir(), "drop")
	itemDir := filepath.Join(finalDir, "item_001__ITEM-item-1")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatalf("mkdir final dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "DONE"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write done marker: %v", err)
	}

	err := handleExistingDrop(ctx, testDB, "ing-2", finalDir)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	events := fetchPendingEvents(t, testDB)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	if events[0].EventType != "INGESTION_PROCESSING" {
		t.Fatalf("expected INGESTION_PROCESSING, got %s", events[0].EventType)
	}
	if !strings.Contains(events[0].PayloadJSON, "\"step\":\"drop\"") {
		t.Fatalf("unexpected payload: %s", events[0].PayloadJSON)
	}
}

func TestHandleExistingDropDoneWithErrorPostsFailedEvent(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	finalDir := filepath.Join(t.TempDir(), "drop")
	itemDir := filepath.Join(finalDir, "item_001__ITEM-item-1")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatalf("mkdir final dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "DONE"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write done marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "ERROR"), []byte("worker failed"), 0o644); err != nil {
		t.Fatalf("write error marker: %v", err)
	}

	err := handleExistingDrop(ctx, testDB, "ing-3", finalDir)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	events := fetchPendingEvents(t, testDB)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	if events[0].EventType != "INGESTION_FAILED" {
		t.Fatalf("expected INGESTION_FAILED, got %s", events[0].EventType)
	}
	if !strings.Contains(events[0].PayloadJSON, "worker failed") {
		t.Fatalf("unexpected payload: %s", events[0].PayloadJSON)
	}
}

func TestPostStepEventAndPostFailureEnqueueExpectedEvents(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)

	if err := postStepEvent(ctx, testDB, "ing-4", "PIPELINE_STEP_STARTED", "download"); err != nil {
		t.Fatalf("postStepEvent failed: %v", err)
	}
	postFailure(ctx, testDB, "ing-4", "download_failed", errors.New("boom"))

	events := fetchPendingEvents(t, testDB)
	if len(events) != 2 {
		t.Fatalf("expected two events, got %d", len(events))
	}

	if events[0].EventType != "PIPELINE_STEP_STARTED" {
		t.Fatalf("expected first event PIPELINE_STEP_STARTED, got %s", events[0].EventType)
	}
	assertPayloadHasStep(t, events[0].PayloadJSON, "download")

	if events[1].EventType != "PIPELINE_STEP_FAILED" {
		t.Fatalf("expected second event PIPELINE_STEP_FAILED, got %s", events[1].EventType)
	}
	assertPayloadHasStep(t, events[1].PayloadJSON, "download_failed")
	if !strings.Contains(events[1].PayloadJSON, "boom") {
		t.Fatalf("expected failure payload to include error message, got %s", events[1].PayloadJSON)
	}
}

func TestPostFailureNoopForNilError(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)

	postFailure(ctx, testDB, "ing-5", "download_failed", nil)

	events := fetchPendingEvents(t, testDB)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %d", len(events))
	}
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	testDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = testDB.Close() })
	if err := testDB.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return testDB
}

func fetchPendingEvents(t *testing.T, testDB *db.DB) []db.VPSEvent {
	t.Helper()
	events, err := testDB.FetchPendingVPSEvents(context.Background(), 100)
	if err != nil {
		t.Fatalf("fetch pending events: %v", err)
	}
	return events
}

func assertPayloadHasStep(t *testing.T, payloadJSON, wantStep string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	step, _ := payload["step"].(string)
	if step != wantStep {
		t.Fatalf("unexpected step in payload: got %q want %q payload=%s", step, wantStep, payloadJSON)
	}
}

func TestBuildAvailableFilesSnapshotIncludesAndSortsArtifacts(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "derivatives", "access"))
	mustMkdirAll(t, filepath.Join(root, "derivatives", "images", "thumb"))
	mustMkdirAll(t, filepath.Join(root, "derivatives", "images", "web"))
	mustMkdirAll(t, filepath.Join(root, "ocr"))
	mustMkdirAll(t, filepath.Join(root, "original"))

	mustWriteFile(t, filepath.Join(root, "derivatives", "access", "reading_v1.pdf"), []byte("pdf"))
	mustWriteFile(t, filepath.Join(root, "derivatives", "access", "reading_ocr_v1.pdf"), []byte("pdf-ocr"))
	mustWriteFile(t, filepath.Join(root, "derivatives", "images", "thumb", "thumb.jpg"), []byte("thumb"))
	mustWriteFile(t, filepath.Join(root, "derivatives", "images", "web", "web.png"), []byte("web"))
	mustWriteFile(t, filepath.Join(root, "ocr", "ocr.txt"), []byte("ocr"))
	mustWriteFile(t, filepath.Join(root, "original", "master.tif"), []byte("orig"))

	files, err := buildAvailableFilesSnapshot("OBJ-20260312-TEST01", root, true)
	if err != nil {
		t.Fatalf("buildAvailableFilesSnapshot failed: %v", err)
	}
	if len(files) != 6 {
		t.Fatalf("expected 6 files with originals enabled, got %d", len(files))
	}

	archiveKeys := make([]string, 0, len(files))
	kinds := make(map[string]bool)
	variants := make(map[string]bool)
	for _, f := range files {
		archiveKeys = append(archiveKeys, f.ArchiveFileKey)
		kinds[f.ArtifactKind] = true
		if f.Variant == nil || strings.TrimSpace(*f.Variant) == "" {
			t.Fatalf("expected variant for kind=%s", f.ArtifactKind)
		}
		variants[*f.Variant] = true
		if f.ContentType == nil || strings.TrimSpace(*f.ContentType) == "" {
			t.Fatalf("expected content type for kind=%s", f.ArtifactKind)
		}
		if f.SizeBytes == nil || *f.SizeBytes < 0 {
			t.Fatalf("expected size bytes for kind=%s", f.ArtifactKind)
		}
		pathValue, ok := f.Metadata["path"].(string)
		if !ok || strings.TrimSpace(pathValue) == "" {
			t.Fatalf("expected metadata.path for kind=%s", f.ArtifactKind)
		}
	}

	if !sort.StringsAreSorted(archiveKeys) {
		t.Fatalf("expected archive file keys to be sorted, got %v", archiveKeys)
	}

	for _, kind := range []string{"pdf", "thumbnail", "web_version", "ocr_text", "original"} {
		if !kinds[kind] {
			t.Fatalf("expected artifact kind %q in snapshot", kind)
		}
	}
	for _, variant := range []string{"access_v1", "access_ocr_v1"} {
		if !variants[variant] {
			t.Fatalf("expected pdf variant %q in snapshot", variant)
		}
	}
}

func TestBuildAvailableFilesSnapshotExcludesOriginalWhenDisabled(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "original"))
	mustMkdirAll(t, filepath.Join(root, "derivatives", "access"))
	mustWriteFile(t, filepath.Join(root, "original", "master.tif"), []byte("orig"))
	mustWriteFile(t, filepath.Join(root, "derivatives", "access", "reading_v1.pdf"), []byte("pdf"))

	files, err := buildAvailableFilesSnapshot("OBJ-20260312-TEST01", root, false)
	if err != nil {
		t.Fatalf("buildAvailableFilesSnapshot failed: %v", err)
	}
	for _, f := range files {
		if f.ArtifactKind == "original" {
			t.Fatalf("did not expect original artifact when includeOriginals=false")
		}
	}
}

func TestResolveArtifactPathByKindSupportsBothPDFVariants(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "derivatives", "access"))
	mustWriteFile(t, filepath.Join(root, "derivatives", "access", "reading_v1.pdf"), []byte("pdf"))
	mustWriteFile(t, filepath.Join(root, "derivatives", "access", "reading_ocr_v1.pdf"), []byte("pdf-ocr"))

	path, err := resolveArtifactPathByKind(root, "pdf", "access_v1")
	if err != nil {
		t.Fatalf("resolve access_v1: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("derivatives", "access", "reading_v1.pdf")) {
		t.Fatalf("unexpected access_v1 path: %s", path)
	}

	path, err = resolveArtifactPathByKind(root, "pdf", "access_ocr_v1")
	if err != nil {
		t.Fatalf("resolve access_ocr_v1: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("derivatives", "access", "reading_ocr_v1.pdf")) {
		t.Fatalf("unexpected access_ocr_v1 path: %s", path)
	}
}

func TestDeliverBackendObjectTaskMarksSentOnSuccess(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	objectID := "OBJ-20260312-TEST01"
	objectRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(objectRoot, "derivatives", "access"))
	mustWriteFile(t, filepath.Join(objectRoot, "derivatives", "access", "doc.pdf"), []byte("pdf"))

	insertObjectFixture(t, testDB, objectID, objectRoot)
	taskID := insertBackendTaskFixture(t, testDB, objectID, availableFilesSnapshotAction)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("expected PUT request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/api/internal/objects/") || !strings.HasSuffix(r.URL.Path, "/available-files") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := vps.NewClient(server.URL, "token", "worker")
	cfg := config.Config{PublishOriginalsAvailableFiles: false}

	err := deliverBackendObjectTask(ctx, testDB, client, cfg, db.BackendObjectTask{
		TaskID:     taskID,
		ObjectID:   objectID,
		ActionType: availableFilesSnapshotAction,
	})
	if err != nil {
		t.Fatalf("deliverBackendObjectTask failed: %v", err)
	}

	state, attempts, lastErr := getBackendTaskState(t, testDB, taskID)
	if state != "sent" {
		t.Fatalf("expected task state sent, got %s", state)
	}
	if attempts != 0 {
		t.Fatalf("expected attempts to remain 0, got %d", attempts)
	}
	if strings.TrimSpace(lastErr) != "" {
		t.Fatalf("expected empty last_error, got %q", lastErr)
	}
}

func TestDeliverBackendObjectTaskMarksFailedOnUnsupportedAction(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	objectID := "OBJ-20260312-TEST02"
	insertObjectFixture(t, testDB, objectID, t.TempDir())
	taskID := insertBackendTaskFixture(t, testDB, objectID, availableFilesSnapshotAction)

	err := deliverBackendObjectTask(ctx, testDB, nil, config.Config{}, db.BackendObjectTask{
		TaskID:     taskID,
		ObjectID:   objectID,
		ActionType: "unknown_action",
	})
	if err != nil {
		t.Fatalf("expected nil error with failed-state recording, got %v", err)
	}

	state, attempts, lastErr := getBackendTaskState(t, testDB, taskID)
	if state != "failed" {
		t.Fatalf("expected task state failed, got %s", state)
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 after failure, got %d", attempts)
	}
	if !strings.Contains(lastErr, "unsupported action_type") {
		t.Fatalf("expected unsupported action in last_error, got %q", lastErr)
	}
}

func TestDeliverBackendObjectTaskMarksFailedWhenSyncFails(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	objectID := "OBJ-20260312-TEST03"
	objectRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(objectRoot, "derivatives", "access"))
	mustWriteFile(t, filepath.Join(objectRoot, "derivatives", "access", "doc.pdf"), []byte("pdf"))

	insertObjectFixture(t, testDB, objectID, objectRoot)
	taskID := insertBackendTaskFixture(t, testDB, objectID, availableFilesSnapshotAction)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	client := vps.NewClient(server.URL, "token", "worker")

	err := deliverBackendObjectTask(ctx, testDB, client, config.Config{}, db.BackendObjectTask{
		TaskID:     taskID,
		ObjectID:   objectID,
		ActionType: availableFilesSnapshotAction,
	})
	if err != nil {
		t.Fatalf("expected nil error with failed-state recording, got %v", err)
	}

	state, attempts, lastErr := getBackendTaskState(t, testDB, taskID)
	if state != "failed" {
		t.Fatalf("expected task state failed, got %s", state)
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 after sync failure, got %d", attempts)
	}
	if !strings.Contains(lastErr, "status 500") {
		t.Fatalf("expected backend error in last_error, got %q", lastErr)
	}
}

func insertObjectFixture(t *testing.T, testDB *db.DB, objectID, objectRoot string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := testDB.ExecContext(context.Background(), `
		INSERT INTO objects (
			object_id, object_root, year, month,
			processing_state, curation_state,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, objectID, objectRoot, 2026, 3, "index_done", "needs_review", now, now)
	if err != nil {
		t.Fatalf("insert object fixture failed: %v", err)
	}
}

func insertBackendTaskFixture(t *testing.T, testDB *db.DB, objectID, actionType string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO backend_object_tasks (
			object_id, action_type, reason, state,
			attempts, next_attempt_at, created_at, updated_at
		) VALUES (?, ?, '', 'processing', 0, ?, ?, ?)
	`, objectID, actionType, now, now, now)
	if err != nil {
		t.Fatalf("insert backend task fixture failed: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id failed: %v", err)
	}
	return id
}

func getBackendTaskState(t *testing.T, testDB *db.DB, taskID int64) (state string, attempts int, lastErr string) {
	t.Helper()
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT state, attempts, COALESCE(last_error, '')
		FROM backend_object_tasks
		WHERE task_id = ?
	`, taskID).Scan(&state, &attempts, &lastErr); err != nil {
		t.Fatalf("query backend task state failed: %v", err)
	}
	return state, attempts, lastErr
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s failed: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s failed: %v", path, err)
	}
}

func TestDeliverEventSuccessMarksSent(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	ingestionID := "ing-evt-1"
	insertIngestionLeaseFixture(t, testDB, ingestionID, "lease-1", "tok-1", time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339))
	eventID := "evt-1"
	insertVPSEventFixture(t, testDB, eventID, ingestionID, `{"step":"download"}`, "PIPELINE_STEP_STARTED")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ingestions/"+ingestionID+"/events" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body failed: %v", err)
		}
		if body["lease_token"] != "tok-1" {
			t.Fatalf("expected lease token tok-1, got %v", body["lease_token"])
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := vps.NewClient(server.URL, "token", "worker")
	ev := db.VPSEvent{EventID: eventID, IngestionID: ingestionID, EventType: "PIPELINE_STEP_STARTED", PayloadJSON: `{"step":"download"}`}
	err := deliverEvent(ctx, testDB, client, ev)
	if err != nil {
		t.Fatalf("deliverEvent failed: %v", err)
	}

	state, attempts, lastErr, sentAt := getVPSEventState(t, testDB, eventID)
	if state != "sent" {
		t.Fatalf("expected event state sent, got %s", state)
	}
	if attempts != 0 {
		t.Fatalf("expected attempts=0, got %d", attempts)
	}
	if strings.TrimSpace(lastErr) != "" {
		t.Fatalf("expected empty last_error, got %q", lastErr)
	}
	if strings.TrimSpace(sentAt) == "" {
		t.Fatalf("expected sent_at to be set")
	}
}

func TestDeliverEventExpiredLeaseReacquiresAndUsesNewToken(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	ingestionID := "ing-evt-2"
	insertIngestionLeaseFixture(t, testDB, ingestionID, "lease-old", "tok-old", time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	eventID := "evt-2"
	insertVPSEventFixture(t, testDB, eventID, ingestionID, `{"step":"download"}`, "PIPELINE_STEP_STARTED")

	seenLeaseAcquire := false
	seenEvents := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ingestions/" + ingestionID + "/lease":
			seenLeaseAcquire = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"lease":{"lease_id":"lease-new","lease_token":"tok-new","lease_expires_at":"2999-01-01T00:00:00Z","ingestion_id":"` + ingestionID + `","batch_label":"b","tenant_id":"t","items":[]}}`))
		case "/api/ingestions/" + ingestionID + "/events":
			seenEvents = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode events body failed: %v", err)
			}
			if body["lease_token"] != "tok-new" {
				t.Fatalf("expected rotated lease token tok-new, got %v", body["lease_token"])
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := vps.NewClient(server.URL, "token", "worker")
	ev := db.VPSEvent{EventID: eventID, IngestionID: ingestionID, EventType: "PIPELINE_STEP_STARTED", PayloadJSON: `{"step":"download"}`}
	err := deliverEvent(ctx, testDB, client, ev)
	if err != nil {
		t.Fatalf("deliverEvent failed: %v", err)
	}
	if !seenLeaseAcquire {
		t.Fatalf("expected lease reacquire call")
	}
	if !seenEvents {
		t.Fatalf("expected events post call")
	}

	leaseID, leaseToken, _, _, qErr := testDB.GetIngestionLease(ctx, ingestionID)
	if qErr != nil {
		t.Fatalf("GetIngestionLease failed: %v", qErr)
	}
	if leaseID != "lease-new" || leaseToken != "tok-new" {
		t.Fatalf("expected updated lease in db, got lease_id=%s lease_token=%s", leaseID, leaseToken)
	}
}

func TestDeliverEventMalformedPayloadMarksFailed(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	ingestionID := "ing-evt-3"
	insertIngestionLeaseFixture(t, testDB, ingestionID, "lease-3", "tok-3", time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339))
	eventID := "evt-3"
	insertVPSEventFixture(t, testDB, eventID, ingestionID, `{"step":`, "PIPELINE_STEP_STARTED")

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := vps.NewClient(server.URL, "token", "worker")

	ev := db.VPSEvent{EventID: eventID, IngestionID: ingestionID, EventType: "PIPELINE_STEP_STARTED", PayloadJSON: `{"step":`}
	err := deliverEvent(ctx, testDB, client, ev)
	if err != nil {
		t.Fatalf("expected nil return after failed-state recording, got %v", err)
	}

	state, attempts, lastErr, _ := getVPSEventState(t, testDB, eventID)
	if state != "failed" {
		t.Fatalf("expected failed state, got %s", state)
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", attempts)
	}
	if !strings.Contains(lastErr, "unmarshal") && !strings.Contains(lastErr, "invalid character") && !strings.Contains(lastErr, "unexpected end of JSON input") {
		t.Fatalf("unexpected last_error: %q", lastErr)
	}
}

func TestDeliverEventPostFailureMarksFailed(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	ingestionID := "ing-evt-4"
	insertIngestionLeaseFixture(t, testDB, ingestionID, "lease-4", "tok-4", time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339))
	eventID := "evt-4"
	insertVPSEventFixture(t, testDB, eventID, ingestionID, `{"step":"download"}`, "PIPELINE_STEP_STARTED")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()
	client := vps.NewClient(server.URL, "token", "worker")

	ev := db.VPSEvent{EventID: eventID, IngestionID: ingestionID, EventType: "PIPELINE_STEP_STARTED", PayloadJSON: `{"step":"download"}`}
	err := deliverEvent(ctx, testDB, client, ev)
	if err != nil {
		t.Fatalf("expected nil return after failed-state recording, got %v", err)
	}

	state, attempts, lastErr, _ := getVPSEventState(t, testDB, eventID)
	if state != "failed" {
		t.Fatalf("expected failed state, got %s", state)
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", attempts)
	}
	if !strings.Contains(lastErr, "status 500") {
		t.Fatalf("expected status 500 in last_error, got %q", lastErr)
	}
}

func TestDeliverEventPassesObjectIDWhenProvided(t *testing.T) {
	ctx := context.Background()
	testDB := openTestDB(t)
	ingestionID := "ing-evt-5"
	insertIngestionLeaseFixture(t, testDB, ingestionID, "lease-5", "tok-5", time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339))
	eventID := "evt-5"
	insertVPSEventFixture(t, testDB, eventID, ingestionID, `{"step":"download"}`, "OBJECT_CREATED")

	objectID := "OBJ-20260312-TEST99"
	receivedObjectID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ingestions/"+ingestionID+"/events" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body struct {
			Events []struct {
				ObjectID string `json:"object_id"`
			} `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body failed: %v", err)
		}
		if len(body.Events) != 1 {
			t.Fatalf("expected one event, got %d", len(body.Events))
		}
		receivedObjectID = body.Events[0].ObjectID
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := vps.NewClient(server.URL, "token", "worker")
	ev := db.VPSEvent{
		EventID:     eventID,
		IngestionID: ingestionID,
		ObjectID:    sql.NullString{String: objectID, Valid: true},
		EventType:   "OBJECT_CREATED",
		PayloadJSON: `{"step":"download"}`,
	}
	if err := deliverEvent(ctx, testDB, client, ev); err != nil {
		t.Fatalf("deliverEvent failed: %v", err)
	}
	if receivedObjectID != objectID {
		t.Fatalf("expected object_id %q, got %q", objectID, receivedObjectID)
	}
}

func insertIngestionLeaseFixture(t *testing.T, testDB *db.DB, ingestionID, leaseID, leaseToken, leaseExpiresAt string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := testDB.ExecContext(context.Background(), `
		INSERT INTO ingestion_lease_tokens (ingestion_id, lease_id, lease_token, lease_expires_at, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)
	`, ingestionID, leaseID, leaseToken, leaseExpiresAt, now, now)
	if err != nil {
		t.Fatalf("insert ingestion lease fixture failed: %v", err)
	}
}

func insertVPSEventFixture(t *testing.T, testDB *db.DB, eventID, ingestionID, payloadJSON, eventType string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := testDB.ExecContext(context.Background(), `
		INSERT INTO vps_events (event_id, ingestion_id, object_id, event_type, payload_json, created_at, attempts, next_attempt_at, state)
		VALUES (?, ?, NULL, ?, ?, ?, 0, ?, 'pending')
	`, eventID, ingestionID, eventType, payloadJSON, now, now)
	if err != nil {
		t.Fatalf("insert vps event fixture failed: %v", err)
	}
}

func getVPSEventState(t *testing.T, testDB *db.DB, eventID string) (state string, attempts int, lastErr string, sentAt string) {
	t.Helper()
	var lastErrNS sql.NullString
	var sentAtNS sql.NullString
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT state, attempts, last_error, sent_at
		FROM vps_events
		WHERE event_id = ?
	`, eventID).Scan(&state, &attempts, &lastErrNS, &sentAtNS); err != nil {
		t.Fatalf("query vps event state failed: %v", err)
	}
	if lastErrNS.Valid {
		lastErr = lastErrNS.String
	}
	if sentAtNS.Valid {
		sentAt = sentAtNS.String
	}
	return state, attempts, lastErr, sentAt
}
