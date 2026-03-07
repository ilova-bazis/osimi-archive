package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type leaseResponse struct {
	Lease *struct {
		CatalogJSON json.RawMessage `json:"catalog_json"`
	} `json:"lease"`
}

func main() {
	baseURL := strings.TrimRight(os.Getenv("VPS_BASE_URL"), "/")
	authToken := os.Getenv("WORKER_AUTH_TOKEN")
	if baseURL == "" || authToken == "" {
		panic("VPS_BASE_URL and WORKER_AUTH_TOKEN must be set")
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/ingestions/lease", bytes.NewBufferString("{}"))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-worker-auth-token", authToken)
	if workerID := os.Getenv("WORKER_ID"); workerID != "" {
		req.Header.Set("x-worker-id", workerID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Printf("status=%d body=%s\n", resp.StatusCode, string(body))
		return
	}

	var leaseResp leaseResponse
	if err := json.Unmarshal(body, &leaseResp); err != nil {
		panic(err)
	}
	if leaseResp.Lease == nil {
		fmt.Println("lease: null")
		return
	}

	if len(leaseResp.Lease.CatalogJSON) == 0 {
		fmt.Println("catalog_json is empty or null")
		return
	}
	fmt.Println(string(leaseResp.Lease.CatalogJSON))
}
