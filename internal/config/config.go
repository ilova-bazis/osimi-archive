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
}

func Load() Config {

	cfg := Config{
		ArchiveRoot:  getenv("ARCHIVE_ROOT", "data/osimi-archive"),
		IngestDrop:   getenv("INGEST_DROP", "data/osimi-archive/ingest_drop"),
		DBPath:       getenv("DB_PATH", "data/osimi-archive/db/osimi.sqlite"),
		PollInterval: getenvDuration("POLL_INTERVAL", 3*time.Second),
		MaxWorkers:   getenvInt("MAX_WORKERS", 4),
		DoneMarker:   getenv("DONE_MARKER", "DONE"),
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
