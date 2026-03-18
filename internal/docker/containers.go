package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

// ContainerInfo is the representation sent to Docked.
// It intentionally omits large fields; Docked does registry checks server-side.
type ContainerInfo struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	ImageID string            `json:"imageId"`
	Status  string            `json:"status"`
	State   string            `json:"state"`
	Created int64             `json:"created"`
	Labels  map[string]string `json:"labels,omitempty"`
	Ports   []PortBinding     `json:"ports,omitempty"`

	// NetworkMode from HostConfig (e.g. "container:<id>", "service:<name>", "bridge").
	// Used by Docked to display network dependency warnings before upgrading.
	NetworkMode string `json:"networkMode,omitempty"`

	// Compose info (populated when the container is part of a Compose project)
	ComposeProject    string `json:"composeProject,omitempty"`
	ComposeService    string `json:"composeService,omitempty"`
	ComposeWorkingDir string `json:"composeWorkingDir,omitempty"`
	ComposeConfigFile string `json:"composeConfigFile,omitempty"`

	// RepoDigests from the local image, used by Docked for update detection.
	RepoDigests []string `json:"repoDigests,omitempty"`

	// ImageCreated is the unix timestamp of when the image was built.
	// Populated from the local image metadata.
	ImageCreated int64 `json:"imageCreated,omitempty"`
}

// PortBinding represents a single host→container port mapping.
type PortBinding struct {
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort"`
	ContainerPort string `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

// imageMetadata holds fields extracted from the local image list.
type imageMetadata struct {
	RepoDigests []string
	Created     int64
}

// ListContainers returns all running containers, enriched with RepoDigests and
// image creation time from local images.
func (dc *Client) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	args := filters.NewArgs()
	args.Add("status", "running")

	raw, err := dc.c.ContainerList(ctx, container.ListOptions{Filters: args})
	if err != nil {
		return nil, err
	}

	// Build imageID → metadata map from a single ImageList call.
	metaByID := buildImageMetadataMap(ctx, dc)

	out := make([]ContainerInfo, 0, len(raw))
	for _, c := range raw {
		info := containerToInfo(c)
		if meta, ok := metaByID[c.ImageID]; ok {
			info.RepoDigests = meta.RepoDigests
			info.ImageCreated = meta.Created
		}
		out = append(out, info)
	}
	return out, nil
}

// buildImageMetadataMap fetches the local image list and returns a map of
// imageID → imageMetadata (RepoDigests + Created). Errors are silently ignored (best-effort).
func buildImageMetadataMap(ctx context.Context, dc *Client) map[string]imageMetadata {
	imgs, err := dc.c.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil
	}
	m := make(map[string]imageMetadata, len(imgs))
	for _, img := range imgs {
		m[img.ID] = imageMetadata{
			RepoDigests: img.RepoDigests,
			Created:     img.Created,
		}
	}
	return m
}

// InspectContainer returns the ContainerInfo for a single container by ID or name.
// Uses the Docker inspect API directly instead of listing all containers.
// Returns nil, nil when the container is not found.
func (dc *Client) InspectContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	inspect, err := dc.c.ContainerInspect(ctx, id)
	if err != nil {
		// Docker returns a 404 when the container doesn't exist.
		if strings.Contains(err.Error(), "No such container") ||
			strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, err
	}
	info := inspectToInfo(inspect)
	return &info, nil
}

// inspectToInfo converts a full ContainerJSON (from inspect) to ContainerInfo.
func inspectToInfo(c types.ContainerJSON) ContainerInfo {
	name := strings.TrimPrefix(c.Name, "/")

	ports := make([]PortBinding, 0)
	if c.NetworkSettings != nil {
		for portProto, bindings := range c.NetworkSettings.Ports {
			parts := strings.SplitN(string(portProto), "/", 2)
			cPort := parts[0]
			proto := "tcp"
			if len(parts) == 2 {
				proto = parts[1]
			}
			if len(bindings) == 0 {
				ports = append(ports, PortBinding{
					ContainerPort: cPort,
					Protocol:      proto,
				})
				continue
			}
			for _, b := range bindings {
				ports = append(ports, PortBinding{
					HostIP:        b.HostIP,
					HostPort:      b.HostPort,
					ContainerPort: cPort,
					Protocol:      proto,
				})
			}
		}
	}

	networkMode := ""
	if c.HostConfig != nil {
		networkMode = string(c.HostConfig.NetworkMode)
	}

	var created int64
	if t, err := time.Parse(time.RFC3339Nano, c.Created); err == nil {
		created = t.Unix()
	}

	info := ContainerInfo{
		ID:          c.ID,
		Name:        name,
		Image:       c.Config.Image,
		ImageID:     c.Image,
		Status:      c.State.Status,
		State:       c.State.Status,
		Created:     created,
		Labels:      c.Config.Labels,
		Ports:       ports,
		NetworkMode: networkMode,
	}

	if project, ok := c.Config.Labels["com.docker.compose.project"]; ok {
		info.ComposeProject = project
		info.ComposeService = c.Config.Labels["com.docker.compose.service"]
		info.ComposeWorkingDir = c.Config.Labels["com.docker.compose.project.working_dir"]
		info.ComposeConfigFile = c.Config.Labels["com.docker.compose.project.config_files"]
	}

	return info
}

func containerToInfo(c types.Container) ContainerInfo {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}

	ports := make([]PortBinding, 0, len(c.Ports))
	for _, p := range c.Ports {
		ports = append(ports, PortBinding{
			HostIP:        p.IP,
			HostPort:      portStr(p.PublicPort),
			ContainerPort: portStr(p.PrivatePort),
			Protocol:      p.Type,
		})
	}

	info := ContainerInfo{
		ID:          c.ID,
		Name:        name,
		Image:       c.Image,
		ImageID:     c.ImageID,
		Status:      c.Status,
		State:       c.State,
		Created:     c.Created,
		Labels:      c.Labels,
		Ports:       ports,
		NetworkMode: c.HostConfig.NetworkMode,
	}

	if project, ok := c.Labels["com.docker.compose.project"]; ok {
		info.ComposeProject = project
		info.ComposeService = c.Labels["com.docker.compose.service"]
		info.ComposeWorkingDir = c.Labels["com.docker.compose.project.working_dir"]
		info.ComposeConfigFile = c.Labels["com.docker.compose.project.config_files"]
	}

	return info
}

func portStr(p uint16) string {
	if p == 0 {
		return ""
	}
	return fmt.Sprintf("%d", p)
}
