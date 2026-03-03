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
	"github.com/dockedapp/dockhand/internal/heartbeat"
	"github.com/dockedapp/dockhand/internal/operations"
)

func main() {
	configPath := flag.String("config", "/etc/dockhand/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// Watch config file — reload operations/apps automatically on save
	go config.Watch(ctx, *configPath, 2*time.Second, func() {
		c, err := config.Load(*configPath)
		if err != nil {
			log.Printf("config watcher: reload failed: %v", err)
			return
		}
		runner.Reload(c.Operations)
		runner.ReloadApps(c.Apps)
		log.Printf("config watcher: reloaded (%d operations, %d apps)", len(c.Operations), len(c.Apps))
	})

	// HTTP server
	srv := api.New(cfg, dc, runner, *configPath)

	// Run server in background; block on OS signal
	serverErr := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// Start heartbeat — periodically phone home to Docked server so it
	// knows our current URL (handles IP changes after restarts).
	dockerOKFn := func() bool {
		if dc == nil {
			return false
		}
		pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
		defer pingCancel()
		return dc.Ping(pingCtx) == nil
	}
	heartbeat.Start(ctx, cfg, api.Version, dockerOKFn)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("server error: %v", err)
	case sig := <-quit:
		log.Printf("received %s — shutting down", sig)
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}

	if dc != nil {
		dc.Close()
	}
	log.Println("shutdown complete")
}
