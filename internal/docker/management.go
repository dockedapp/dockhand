package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

// ImageInfo is the representation sent to Docked for image management.
type ImageInfo struct {
	ID          string   `json:"id"`
	RepoTags    []string `json:"repoTags"`
	RepoDigests []string `json:"repoDigests"`
	Size        int64    `json:"size"`
	Created     int64    `json:"created"`
}

// createBody mirrors the Docker Engine API container-create request body.
// The top-level fields correspond to container.Config; HostConfig and
// NetworkingConfig are nested objects in the same payload.
type createBody struct {
	container.Config
	HostConfig       *container.HostConfig      `json:"HostConfig,omitempty"`
	NetworkingConfig *network.NetworkingConfig  `json:"NetworkingConfig,omitempty"`
}

// CreateContainerResult is returned after a successful container create.
type CreateContainerResult struct {
	ID       string   `json:"id"`
	Warnings []string `json:"warnings,omitempty"`
}

// FullInspect returns the raw Docker ContainerJSON for a container.
// The returned bytes are the same JSON structure that the Docker Engine API
// returns from GET /containers/{id}/json, so the docked server can parse it
// with the same logic it uses for Portainer-proxied calls.
func (dc *Client) FullInspect(ctx context.Context, id string) (json.RawMessage, error) {
	inspect, err := dc.c.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", id, err)
	}
	raw, err := json.Marshal(inspect)
	if err != nil {
		return nil, fmt.Errorf("marshal inspect: %w", err)
	}
	return raw, nil
}

// StopContainer stops a running container (30-second timeout).
func (dc *Client) StopContainer(ctx context.Context, id string) error {
	timeout := 30
	return dc.c.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

// StartContainer starts a stopped container.
func (dc *Client) StartContainer(ctx context.Context, id string) error {
	return dc.c.ContainerStart(ctx, id, container.StartOptions{})
}

// RemoveContainer removes a container. Set force=true to remove a running container.
func (dc *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	return dc.c.ContainerRemove(ctx, id, container.RemoveOptions{Force: force})
}

// CreateContainer creates a new container from a Docker Engine API-compatible
// JSON payload (same format as POST /containers/create). The container name
// is passed separately (equivalent to the Docker API ?name= query parameter).
func (dc *Client) CreateContainer(ctx context.Context, name string, body json.RawMessage) (*CreateContainerResult, error) {
	var req createBody
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse container config: %w", err)
	}

	hostCfg := req.HostConfig
	if hostCfg == nil {
		hostCfg = &container.HostConfig{}
	}
	netCfg := req.NetworkingConfig
	if netCfg == nil {
		netCfg = &network.NetworkingConfig{}
	}

	resp, err := dc.c.ContainerCreate(ctx, &req.Config, hostCfg, netCfg, nil, name)
	if err != nil {
		return nil, fmt.Errorf("create container %q: %w", name, err)
	}
	return &CreateContainerResult{ID: resp.ID, Warnings: resp.Warnings}, nil
}

// ListImages returns all local images.
func (dc *Client) ListImages(ctx context.Context) ([]ImageInfo, error) {
	imgs, err := dc.c.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]ImageInfo, 0, len(imgs))
	for _, img := range imgs {
		out = append(out, ImageInfo{
			ID:          img.ID,
			RepoTags:    img.RepoTags,
			RepoDigests: img.RepoDigests,
			Size:        img.Size,
			Created:     img.Created,
		})
	}
	return out, nil
}

// PullImage pulls an image by reference (e.g. "nginx:latest") and discards
// the progress stream. Blocks until the pull is complete or the context is
// cancelled. Returns an error if the pull fails.
func (dc *Client) PullImage(ctx context.Context, ref string) error {
	rc, err := dc.c.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer rc.Close()
	// Drain and discard — progress is tracked by docked server separately.
	_, err = io.Copy(io.Discard, rc)
	return err
}

// RemoveImage removes a local image. Set force=true to remove even if
// the image is used by stopped containers.
func (dc *Client) RemoveImage(ctx context.Context, id string, force bool) error {
	_, err := dc.c.ImageRemove(ctx, id, image.RemoveOptions{Force: force, PruneChildren: true})
	return err
}
