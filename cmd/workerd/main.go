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

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	imageProcessor, err := ingest.NewImageProcessor()
	if err != nil {
		log.Fatal(err)
	}
	if imageProcessor.Name() == "ffmpeg" {
		log.Printf("image processor: ffmpeg (fallback)")
	} else {
		log.Printf("image processor: %s", imageProcessor.Name())
	}

	audioProcessor, err := ingest.NewAudioProcessor()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("audio processor: %s", audioProcessor.Name())

	videoProcessor, err := ingest.NewVideoProcessor()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("video processor: %s", videoProcessor.Name())

	documentProcessor, err := ingest.NewDocumentProcessor()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("document processor: %s", documentProcessor.Name())

	derivativesWorker := &ingest.DerivativesWorker{
		DB:                d,
		ImageProcessor:    imageProcessor,
		AudioProcessor:    audioProcessor,
		VideoProcessor:    videoProcessor,
		DocumentProcessor: documentProcessor,
	}
	ocrWorker := &ingest.OCRWorker{DB: d}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			got, err := derivativesWorker.RunOnce(ctx)
			if err != nil {
				log.Printf("derivatives worker: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}
			if got {
				continue
			}

			got, err = ocrWorker.RunOnce(ctx)
			if err != nil {
				log.Printf("ocr worker: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}
			if !got {
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()

	log.Println("worker daemon ready")
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
		log.Println("worker daemon stopped")
	case <-shutdownCtx.Done():
		log.Printf("shutdown timeout after %s; forcing exit", shutdownTimeout)
	}
}
