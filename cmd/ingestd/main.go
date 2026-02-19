package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ilova-bazis/osimi-archive/internal/config"
	"github.com/ilova-bazis/osimi-archive/internal/db"
	"github.com/ilova-bazis/osimi-archive/internal/ingest"
)

func main() {

	cfg := config.Load()

	d, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.EnsureSchema(ctx); err != nil {
		log.Fatal(err)
	}

	watcher := &ingest.Watcher{
		DB:          d,
		IngestDrop:  cfg.IngestDrop,
		DoneMarker:  cfg.DoneMarker,
		ArchiveRoot: cfg.ArchiveRoot,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(cfg.PollInterval)
		defer t.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := watcher.ScanAndEnqueue(ctx); err != nil {
					log.Printf("scanning error: %v", err)
				}
			}
		}
	}()

	log.Println("db schema ensured; ingest daemon ready")

	worker := &ingest.Worker{DB: d}
	for i := 0; i < cfg.MaxWorkers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				got, err := worker.RunOnce(ctx)
				if err != nil {
					log.Printf("worker-%d: %v", id, err)
					time.Sleep(1 * time.Second)
					continue
				}
				if !got {
					time.Sleep(500 * time.Millisecond)
				}
			}
		}(i + 1)
	}

	select {
	case sig := <-sigCh:
		log.Printf("shutdown signal received: %v", sig)
		cancel()
	case <-ctx.Done():
	}

	shutdownTimeout := 30 * time.Second
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("ingest daemon stopped")
	case <-shutdownCtx.Done():
		log.Printf("shutdown timeout after %s; forcing exit", shutdownTimeout)
	}
}
