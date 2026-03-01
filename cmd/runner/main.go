package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dockedapp/dockhand/internal/api"
	"github.com/dockedapp/dockhand/internal/config"
	"github.com/dockedapp/dockhand/internal/docker"
	"github.com/dockedapp/dockhand/internal/enrollment"
	"github.com/dockedapp/dockhand/internal/operations"
)

func main() {
	configPath := flag.String("config", "/etc/dockhand/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	// Attempt enrollment with Docked server (no-ops if already registered
	// or no enrollment token is configured).
	enrollment.Run(cfg, *configPath)

	// Docker client (optional — skipped if Docker is disabled or unavailable)
	var dc *docker.Client
	if cfg.Docker.Enabled {
		dc, err = docker.New(cfg.Docker.Socket)
		if err != nil {
			log.Printf("warning: docker unavailable: %v (container features disabled)", err)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if pingErr := dc.Ping(ctx); pingErr != nil {
				log.Printf("warning: docker ping failed: %v (container features disabled)", pingErr)
				dc = nil
			}
			cancel()
		}
	}

	// Operation history DB
	dataDir := config.DataDir()
	histDB, err := operations.OpenDB(dataDir)
	if err != nil {
		log.Fatalf("history db: %v", err)
	}
	defer histDB.Close()

	runner := operations.NewRunner(cfg.Operations, cfg.Apps, histDB, *configPath)

	// HTTP server
	srv := api.New(cfg, dc, runner, *configPath)

	// Run server in background; block on OS signal
	serverErr := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("server error: %v", err)
	case sig := <-quit:
		log.Printf("received %s — shutting down", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}

	if dc != nil {
		dc.Close()
	}
	log.Println("shutdown complete")
}
