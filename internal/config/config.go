package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	ArchiveRoot  string
	IngestDrop   string
	DBPath       string
	PollInterval time.Duration
	MaxWorkers   int
	DoneMarker   string
	Verbose      bool

	VPSBaseURL                       string
	WorkerAuthToken                  string
	WorkerID                         string
	LeasePollInterval                time.Duration
	LeaseHeartbeatInterval           time.Duration
	DownloadRequestPollInterval      time.Duration
	DownloadRequestHeartbeatInterval time.Duration

	VPSNotifierPollInterval        time.Duration
	VPSNotifierBatchSize           int
	PublishOriginalsAvailableFiles bool
}

func Load() Config {

	cfg := Config{
		ArchiveRoot:  getenv("ARCHIVE_ROOT", "data/osimi-archive"),
		IngestDrop:   getenv("INGEST_DROP", "data/osimi-archive/ingest_drop"),
		DBPath:       getenv("DB_PATH", "data/osimi-archive/db/osimi.sqlite"),
		PollInterval: getenvDuration("POLL_INTERVAL", 3*time.Second),
		MaxWorkers:   getenvInt("MAX_WORKERS", 4),
		DoneMarker:   getenv("DONE_MARKER", "DONE"),
		Verbose:      getenvBool("VERBOSE", false),

		VPSBaseURL:                       getenv("VPS_BASE_URL", ""),
		WorkerAuthToken:                  getenv("WORKER_AUTH_TOKEN", ""),
		WorkerID:                         getenv("WORKER_ID", ""),
		LeasePollInterval:                getenvDuration("LEASE_POLL_INTERVAL", 5*time.Second),
		LeaseHeartbeatInterval:           getenvDuration("LEASE_HEARTBEAT_INTERVAL", 90*time.Second),
		DownloadRequestPollInterval:      getenvDuration("DOWNLOAD_REQUEST_POLL_INTERVAL", 5*time.Second),
		DownloadRequestHeartbeatInterval: getenvDuration("DOWNLOAD_REQUEST_HEARTBEAT_INTERVAL", 90*time.Second),

		VPSNotifierPollInterval:        getenvDuration("VPS_NOTIFIER_POLL_INTERVAL", 3*time.Second),
		VPSNotifierBatchSize:           getenvInt("VPS_NOTIFIER_BATCH_SIZE", 50),
		PublishOriginalsAvailableFiles: getenvBool("PUBLISH_ORIGINALS_AVAILABLE_FILES", false),
	}
	return cfg
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {

		return v
	}

	return def
}

func getenvInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	val, err := strconv.Atoi(v)
	if err != nil {
		return def
	}

	return val
}

func getenvDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	val, err := time.ParseDuration(v)
	if err != nil {
		return def
	}

	return val

}

func getenvBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	val, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return val
}
