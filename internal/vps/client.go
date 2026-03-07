package vps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL   string
	authToken string
	workerID  string
	client    *http.Client
	verbose   bool
}

type LeaseResponse struct {
	Lease *Lease `json:"lease"`
}

type Lease struct {
	LeaseID        string          `json:"lease_id"`
	LeaseToken     string          `json:"lease_token"`
	LeaseExpiresAt string          `json:"lease_expires_at"`
	IngestionID    string          `json:"ingestion_id"`
	BatchLabel     string          `json:"batch_label"`
	TenantID       string          `json:"tenant_id"`
	DownloadURLs   []DownloadURL   `json:"download_urls"`
	CatalogJSON    json.RawMessage `json:"catalog_json"`
}

type DownloadURL struct {
	FileID      string `json:"file_id"`
	StorageKey  string `json:"storage_key"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	DownloadURL string `json:"download_url"`
}

type Event struct {
	EventID   string         `json:"event_id"`
	EventType string         `json:"event_type"`
	ObjectID  string         `json:"object_id,omitempty"`
	Timestamp string         `json:"timestamp"`
	Payload   map[string]any `json:"payload"`
}

type AvailableFile struct {
	ArchiveFileKey string         `json:"archive_file_key"`
	ArtifactKind   string         `json:"artifact_kind"`
	Variant        *string        `json:"variant,omitempty"`
	DisplayName    string         `json:"display_name"`
	ContentType    *string        `json:"content_type,omitempty"`
	SizeBytes      *int64         `json:"size_bytes,omitempty"`
	ChecksumSHA256 string         `json:"checksum_sha256,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	IsAvailable    bool           `json:"is_available"`
}

type DownloadRequestLeaseResponse struct {
	Request *DownloadRequest `json:"request"`
}

type DownloadRequest struct {
	RequestID       string                 `json:"request_id,omitempty"`
	ID              string                 `json:"id,omitempty"`
	LeaseID         string                 `json:"lease_id,omitempty"`
	ObjectID        string                 `json:"object_id"`
	TenantID        string                 `json:"tenant_id,omitempty"`
	AvailableFileID string                 `json:"available_file_id,omitempty"`
	LeaseToken      string                 `json:"lease_token"`
	LeaseExpiresAt  string                 `json:"lease_expires_at"`
	ArchiveFileKey  string                 `json:"archive_file_key,omitempty"`
	ArtifactKind    string                 `json:"artifact_kind,omitempty"`
	Variant         *string                `json:"variant,omitempty"`
	AvailableFile   *DownloadAvailableFile `json:"available_file,omitempty"`
}

type DownloadAvailableFile struct {
	ID             string  `json:"id,omitempty"`
	ArchiveFileKey string  `json:"archive_file_key,omitempty"`
	ArtifactKind   string  `json:"artifact_kind,omitempty"`
	Variant        *string `json:"variant,omitempty"`
	DisplayName    string  `json:"display_name,omitempty"`
	ContentType    *string `json:"content_type,omitempty"`
}

type DownloadPresignRequest struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Extension   string `json:"extension"`
}

type DownloadArtifactPresignResponse struct {
	UploadURL   string                         `json:"upload_url,omitempty"`
	UploadPath  string                         `json:"upload_path,omitempty"`
	UploadToken string                         `json:"upload_token,omitempty"`
	Headers     DownloadArtifactPresignHeaders `json:"headers"`
}

type DownloadArtifactPresignHeaders struct {
	ContentType   string        `json:"content-type"`
	ContentLength Int64Flexible `json:"content-length"`
}

type Int64Flexible int64

func (v *Int64Flexible) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*v = 0
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		s, err := strconv.Unquote(trimmed)
		if err != nil {
			return err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		*v = Int64Flexible(n)
		return nil
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return err
	}
	*v = Int64Flexible(n)
	return nil
}

type DownloadFailPayload struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (r *DownloadRequest) EffectiveRequestID() string {
	if strings.TrimSpace(r.RequestID) != "" {
		return r.RequestID
	}
	return r.ID
}

func (r *DownloadRequest) EffectiveArchiveFileKey() string {
	if strings.TrimSpace(r.ArchiveFileKey) != "" {
		return r.ArchiveFileKey
	}
	if r.AvailableFile != nil {
		return r.AvailableFile.ArchiveFileKey
	}
	return ""
}

func (r *DownloadRequest) EffectiveArtifactKind() string {
	if strings.TrimSpace(r.ArtifactKind) != "" {
		return r.ArtifactKind
	}
	if r.AvailableFile != nil {
		return r.AvailableFile.ArtifactKind
	}
	return ""
}

func (r *DownloadRequest) EffectiveVariant() string {
	if r.Variant != nil && strings.TrimSpace(*r.Variant) != "" {
		return strings.TrimSpace(*r.Variant)
	}
	if r.AvailableFile != nil {
		if r.AvailableFile.Variant != nil {
			return strings.TrimSpace(*r.AvailableFile.Variant)
		}
	}
	return ""
}

func (r *DownloadArtifactPresignResponse) UploadTargetPath() string {
	if strings.TrimSpace(r.UploadURL) != "" {
		return r.UploadURL
	}
	if strings.TrimSpace(r.UploadPath) != "" {
		return r.UploadPath
	}
	if strings.TrimSpace(r.UploadToken) != "" {
		return "/api/object-download-requests/uploads/" + url.PathEscape(r.UploadToken)
	}
	return ""
}

func NewClient(baseURL, authToken, workerID string) *Client {
	clean := strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL:   clean,
		authToken: authToken,
		workerID:  workerID,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		verbose: false,
	}
}

func NewClientVerbose(baseURL, authToken, workerID string, verbose bool) *Client {
	clean := strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL:   clean,
		authToken: authToken,
		workerID:  workerID,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		verbose: verbose,
	}
}

func (c *Client) LeaseNext(ctx context.Context) (*Lease, error) {
	var resp LeaseResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/ingestions/lease", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Lease, nil
}

func (c *Client) LeaseIngestion(ctx context.Context, ingestionID string) (*Lease, error) {
	var resp LeaseResponse
	path := fmt.Sprintf("/api/ingestions/%s/lease", ingestionID)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Lease, nil
}

func (c *Client) Heartbeat(ctx context.Context, ingestionID, leaseToken string) (*Lease, error) {
	body := map[string]string{"lease_token": leaseToken}
	var resp LeaseResponse
	path := fmt.Sprintf("/api/ingestions/%s/lease/heartbeat", ingestionID)
	if err := c.doJSON(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return resp.Lease, nil
}

func (c *Client) Release(ctx context.Context, ingestionID, leaseToken string) error {
	body := map[string]string{"lease_token": leaseToken}
	path := fmt.Sprintf("/api/ingestions/%s/lease/release", ingestionID)
	return c.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) PostEvents(ctx context.Context, ingestionID, leaseToken string, events []Event) error {
	body := map[string]any{
		"lease_token": leaseToken,
		"events":      events,
	}
	path := fmt.Sprintf("/api/ingestions/%s/events", ingestionID)
	return c.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) PutAvailableFiles(ctx context.Context, objectID string, files []AvailableFile) error {
	body := map[string]any{
		"files": files,
	}
	if c.verbose {
		if payload, err := json.MarshalIndent(body, "", "  "); err != nil {
			log.Printf("[VPS] put available-files payload marshal failed: object_id=%s err=%v", objectID, err)
		} else {
			log.Printf("[VPS] put available-files payload: object_id=%s files=%d\n%s", objectID, len(files), string(payload))
		}
	}
	path := fmt.Sprintf("/api/internal/objects/%s/available-files", url.PathEscape(objectID))
	return c.doJSON(ctx, http.MethodPut, path, body, nil)
}

func (c *Client) LeaseNextDownloadRequest(ctx context.Context) (*DownloadRequest, error) {
	var resp DownloadRequestLeaseResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/object-download-requests/lease", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Request, nil
}

func (c *Client) HeartbeatDownloadRequest(ctx context.Context, requestID, leaseToken string) (*DownloadRequest, error) {
	body := map[string]string{"lease_token": leaseToken}
	var resp DownloadRequestLeaseResponse
	path := fmt.Sprintf("/api/object-download-requests/%s/lease/heartbeat", url.PathEscape(requestID))
	if err := c.doJSON(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return resp.Request, nil
}

func (c *Client) ReleaseDownloadRequest(ctx context.Context, requestID, leaseToken string) error {
	body := map[string]string{"lease_token": leaseToken}
	path := fmt.Sprintf("/api/object-download-requests/%s/lease/release", url.PathEscape(requestID))
	return c.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) PresignDownloadArtifact(ctx context.Context, requestID, leaseToken string, req DownloadPresignRequest) (*DownloadArtifactPresignResponse, error) {
	body := map[string]any{
		"lease_token":  leaseToken,
		"content_type": req.ContentType,
		"size_bytes":   req.SizeBytes,
		"extension":    req.Extension,
	}
	var resp DownloadArtifactPresignResponse
	path := fmt.Sprintf("/api/object-download-requests/%s/artifacts/presign", url.PathEscape(requestID))
	if err := c.doJSON(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) CompleteDownloadRequest(ctx context.Context, requestID, leaseToken, uploadToken string) error {
	body := map[string]any{
		"lease_token":  leaseToken,
		"upload_token": uploadToken,
	}
	path := fmt.Sprintf("/api/object-download-requests/%s/complete", url.PathEscape(requestID))
	return c.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) FailDownloadRequest(ctx context.Context, requestID, leaseToken string, payload DownloadFailPayload) error {
	body := map[string]any{
		"lease_token": leaseToken,
		"failure":     payload,
	}
	path := fmt.Sprintf("/api/object-download-requests/%s/fail", url.PathEscape(requestID))
	return c.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) UploadDownloadRequestArtifact(ctx context.Context, uploadPath, contentType string, sizeBytes int64, headers DownloadArtifactPresignHeaders, body io.Reader) error {
	target := strings.TrimSpace(uploadPath)
	if target == "" {
		return fmt.Errorf("missing upload target")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = c.baseURL + target
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(headers.ContentType) != "" {
		req.Header.Set("Content-Type", headers.ContentType)
	} else if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = sizeBytes
	req.Header.Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload PUT %s: status %d: %s", uploadPath, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return nil
}

func (c *Client) Download(ctx context.Context, downloadURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+downloadURL, nil)
	if err != nil {
		return nil, err
	}
	return c.client.Do(req)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewBuffer(payload)
		if c.verbose {
			log.Printf("[VPS] request: %s %s body_size=%d", method, path, len(payload))
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("x-worker-auth-token", c.authToken)
	if c.workerID != "" {
		req.Header.Set("x-worker-id", c.workerID)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if c.verbose {
		log.Printf("[VPS] response: %s %s status=%d", method, path, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vps %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	return decoder.Decode(out)
}
