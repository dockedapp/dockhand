package docker

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/docker/docker/client"
)

// Client wraps the Docker SDK client with the socket path used at construction.
type Client struct {
	c      *client.Client
	socket string
}

// New creates a Docker client connected to the given Unix socket path.
func New(socketPath string) (*Client, error) {
	c, err := client.NewClientWithOpts(
		client.WithHost("unix://"+socketPath),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Client{c: c, socket: socketPath}, nil
}

// Ping checks connectivity to the Docker daemon.
func (dc *Client) Ping(ctx context.Context) error {
	_, err := dc.c.Ping(ctx)
	return err
}

// Close releases resources held by the client.
func (dc *Client) Close() error {
	return dc.c.Close()
}

// AtomicClient is a thread-safe container for a Docker client pointer.
// It allows the Docker client to be swapped at runtime when Docker
// becomes available after initially being unavailable at startup.
type AtomicClient struct {
	p      atomic.Pointer[Client]
	socket string
}

// NewAtomicClient creates an AtomicClient. If dc is non-nil it is stored
// immediately; otherwise the client starts out empty (Docker unavailable).
func NewAtomicClient(dc *Client, socket string) *AtomicClient {
	ac := &AtomicClient{socket: socket}
	if dc != nil {
		ac.p.Store(dc)
	}
	return ac
}

// Get returns the current Docker client, or nil if Docker is unavailable.
func (ac *AtomicClient) Get() *Client {
	return ac.p.Load()
}

// Set stores a new Docker client (or nil to mark Docker as unavailable).
func (ac *AtomicClient) Set(dc *Client) {
	ac.p.Store(dc)
}

// Socket returns the configured Docker socket path.
func (ac *AtomicClient) Socket() string {
	return ac.socket
}
