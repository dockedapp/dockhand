package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/dockedapp/dockhand/internal/api/handlers"
	"github.com/dockedapp/dockhand/internal/api/middleware"
	"github.com/dockedapp/dockhand/internal/config"
	"github.com/dockedapp/dockhand/internal/docker"
	"github.com/dockedapp/dockhand/internal/operations"
)

// Server is the HTTP server for the runner API.
type Server struct {
	httpServer *http.Server
	cfg        *config.Config
}

// maxBodyBytes is the default maximum request body size (1 MiB).
// The POST /containers/create endpoint gets a larger limit since it accepts
// full Docker container configs.
const (
	maxBodyBytes       int64 = 1 << 20 // 1 MiB
	maxCreateBodyBytes int64 = 4 << 20 // 4 MiB
)

// New constructs the HTTP server, wires all routes, and returns it ready to start.
func New(cfg *config.Config, adc *docker.AtomicClient, runner *operations.Runner, configPath string, histDB io.Closer) *Server {
	mux := http.NewServeMux()

	auth := middleware.Auth(cfg.Server.APIKey)
	bodyLimit := limitBody(maxBodyBytes)
	largeBodyLimit := limitBody(maxCreateBodyBytes)

	// Health — unauthenticated so Docked can probe before a key is set
	dockerOK := func() bool {
		dc := adc.Get()
		if dc == nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return dc.Ping(ctx) == nil
	}
	mux.HandleFunc("GET /health", handlers.Health(Version, cfg.Runner.Name, dockerOK))

	// Container routes — always registered. Each handler checks if Docker
	// is available at request time and returns 503 if not. This allows
	// Docker to become available after startup without a restart.
	ch := handlers.NewContainerHandlers(adc, cfg.Docker.ComposeBinary)
	// Listing / inspection
	mux.Handle("GET /containers", auth(http.HandlerFunc(ch.List)))
	mux.Handle("GET /containers/{id}", auth(http.HandlerFunc(ch.Get)))
	mux.Handle("GET /containers/{id}/json", auth(http.HandlerFunc(ch.FullInspect)))
	// Lifecycle management (used by the docked upgrade pipeline)
	mux.Handle("POST /containers/{id}/stop", auth(http.HandlerFunc(ch.Stop)))
	mux.Handle("POST /containers/{id}/start", auth(http.HandlerFunc(ch.Start)))
	mux.Handle("DELETE /containers/{id}", auth(http.HandlerFunc(ch.Remove)))
	mux.Handle("POST /containers/create", auth(largeBodyLimit(http.HandlerFunc(ch.Create))))
	// SSE streams (existing)
	mux.Handle("POST /containers/{id}/upgrade", auth(http.HandlerFunc(ch.Upgrade)))
	mux.Handle("GET /containers/{id}/logs", auth(http.HandlerFunc(ch.Logs)))
	// Image management
	mux.Handle("GET /images", auth(http.HandlerFunc(ch.ListImages)))
	mux.Handle("POST /images/pull", auth(bodyLimit(http.HandlerFunc(ch.PullImage))))
	mux.Handle("DELETE /images/{id}", auth(http.HandlerFunc(ch.RemoveImage)))

	// System routes — update, uninstall, restart, reload, logs (always registered, authenticated)
	mux.Handle("POST /update", auth(bodyLimit(http.HandlerFunc(handlers.Update))))
	mux.Handle("POST /uninstall", auth(bodyLimit(handlers.Uninstall(histDB))))
	mux.Handle("POST /restart", auth(bodyLimit(http.HandlerFunc(handlers.Restart))))
	mux.Handle("POST /reload", auth(bodyLimit(handlers.Reload(configPath, runner))))
	mux.Handle("GET /logs", auth(http.HandlerFunc(handlers.Logs)))

	// Operation routes — always registered so they work even when config.yaml
	// has no operations yet (e.g. fresh install) and after POST /reload adds some.
	// NOTE: static paths (/operations/history) must come before parameterised
	// paths (/operations/{name}/...) to avoid ambiguity.
	oh := handlers.NewOperationHandlers(runner)
	mux.Handle("GET /operations", auth(http.HandlerFunc(oh.List)))
	mux.Handle("GET /operations/history", auth(http.HandlerFunc(oh.AllHistory)))
	mux.Handle("POST /operations/{name}/run", auth(bodyLimit(http.HandlerFunc(oh.Run))))
	mux.Handle("DELETE /operations/{name}/run", auth(http.HandlerFunc(oh.Cancel)))
	mux.Handle("GET /operations/{name}/history", auth(http.HandlerFunc(oh.History)))

	// App routes — the new first-class concept above operations.
	// NOTE: static paths (/apps/history) must come before parameterised paths.
	ah := handlers.NewAppHandlers(runner)
	mux.Handle("GET /apps", auth(http.HandlerFunc(ah.List)))
	mux.Handle("GET /apps/history", auth(http.HandlerFunc(ah.AllAppHistory)))
	mux.Handle("POST /apps/{appName}/operations/{opName}/run", auth(bodyLimit(http.HandlerFunc(ah.RunApp))))
	mux.Handle("DELETE /apps/{appName}/operations/{opName}/run", auth(http.HandlerFunc(ah.CancelApp)))
	mux.Handle("GET /apps/{appName}/operations/{opName}/history", auth(http.HandlerFunc(ah.AppHistory)))

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      loggingMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // disabled — SSE streams are long-lived
		IdleTimeout:  60 * time.Second,
	}

	return &Server{httpServer: httpServer, cfg: cfg}
}

// Start begins listening. Blocks until the server stops.
func (s *Server) Start() error {
	log.Printf("dockhand listening on :%d  (runner: %s)", s.cfg.Server.Port, s.cfg.Runner.Name)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Version is set at build time via -ldflags.
var Version = "dev"

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start))
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter so http.ResponseController
// can discover Flusher/Hijacker on the original writer.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// limitBody returns middleware that caps the request body to maxBytes.
func limitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
