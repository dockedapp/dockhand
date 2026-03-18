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

const dockerRetryInterval = 30 * time.Second

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
	// or no enrollment token is configured). Runs in the background so it
	// doesn't block startup if the Docked server is temporarily unreachable.
	go enrollment.Run(cfg, *configPath)

	// Docker client (optional — skipped if Docker is disabled or unavailable).
	// Wrapped in AtomicClient so the background retry loop can connect later.
	var dc *docker.Client
	socket := cfg.Docker.Socket
	if cfg.Docker.Enabled {
		dc, err = docker.New(socket)
		if err != nil {
			log.Printf("warning: docker unavailable: %v (will retry in background)", err)
		} else {
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if pingErr := dc.Ping(pingCtx); pingErr != nil {
				log.Printf("warning: docker ping failed: %v (will retry in background)", pingErr)
				dc = nil
			}
			pingCancel()
		}
	}

	adc := docker.NewAtomicClient(dc, socket)

	// If Docker is enabled but not yet connected, retry in the background.
	if cfg.Docker.Enabled && adc.Get() == nil {
		go dockerRetryLoop(ctx, adc)
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
	srv := api.New(cfg, adc, runner, *configPath, histDB)

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
		dc := adc.Get()
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

	if finalDC := adc.Get(); finalDC != nil {
		finalDC.Close()
	}
	log.Println("shutdown complete")
}

// dockerRetryLoop periodically attempts to connect to the Docker daemon.
// Once connected, it stores the client in the AtomicClient and returns.
// If Docker later becomes unavailable, the per-request health check in
// each handler and the heartbeat dockerOK function will detect it. A full
// reconnect (new SDK client) would require another restart, but the common
// case — Docker was starting up when dockhand launched — is handled here.
func dockerRetryLoop(ctx context.Context, adc *docker.AtomicClient) {
	socket := adc.Socket()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(dockerRetryInterval):
		}

		dc, err := docker.New(socket)
		if err != nil {
			log.Printf("docker retry: client init failed: %v", err)
			continue
		}

		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		pingErr := dc.Ping(pingCtx)
		pingCancel()

		if pingErr != nil {
			log.Printf("docker retry: ping failed: %v", pingErr)
			dc.Close()
			continue
		}

		adc.Set(dc)
		log.Println("docker retry: connected — container features now available")
		return
	}
}
