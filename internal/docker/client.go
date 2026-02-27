package docker

import (
	"context"
	"fmt"

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
