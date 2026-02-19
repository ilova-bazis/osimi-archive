package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ilova-bazis/osimi-archive/internal/config"
	"github.com/ilova-bazis/osimi-archive/internal/db"
	"github.com/ilova-bazis/osimi-archive/internal/orchestrator"
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

	planner := &orchestrator.Orchestrator{
		DB:          d,
		ArchiveRoot: cfg.ArchiveRoot,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- planner.Run(ctx)
	}()

	log.Println("orchestrator ready")
	select {
	case sig := <-sigCh:
		log.Printf("shutdown signal received: %v", sig)
		cancel()
		shutdownTimeout := 30 * time.Second
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		select {
		case err := <-errCh:
			if err != nil {
				log.Fatal(err)
			}
		case <-shutdownCtx.Done():
			log.Printf("shutdown timeout after %s; forcing exit", shutdownTimeout)
		}
	case err := <-errCh:
		if err != nil {
			log.Fatal(err)
		}
	}
}
