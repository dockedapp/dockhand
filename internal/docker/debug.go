package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// RestartContainer stops then starts a container.
func (dc *Client) RestartContainer(ctx context.Context, id string) error {
	if err := dc.StopContainer(ctx, id); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := dc.StartContainer(ctx, id); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

// SystemInfo returns Docker system info as raw JSON.
func (dc *Client) SystemInfo(ctx context.Context) (json.RawMessage, error) {
	info, err := dc.c.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker info: %w", err)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal info: %w", err)
	}
	return raw, nil
}

// DiskUsage returns Docker disk usage as raw JSON.
func (dc *Client) DiskUsage(ctx context.Context) (json.RawMessage, error) {
	usage, err := dc.c.DiskUsage(ctx, dockertypes.DiskUsageOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker df: %w", err)
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		return nil, fmt.Errorf("marshal disk usage: %w", err)
	}
	return raw, nil
}

// ContainerStats returns one-shot container stats as raw JSON.
func (dc *Client) ContainerStats(ctx context.Context, id string) (json.RawMessage, error) {
	resp, err := dc.c.ContainerStats(ctx, id, false)
	if err != nil {
		return nil, fmt.Errorf("container stats %s: %w", id, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read stats body: %w", err)
	}
	return json.RawMessage(raw), nil
}

// ContainerLogLines returns up to lines of container log output as a string slice.
func (dc *Client) ContainerLogLines(ctx context.Context, id string, lines int) ([]string, error) {
	rc, err := dc.c.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Tail:       fmt.Sprintf("%d", lines),
	})
	if err != nil {
		return nil, fmt.Errorf("container logs %s: %w", id, err)
	}
	defer rc.Close()

	pr, pw := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, pw, rc)
		pw.CloseWithError(copyErr)
	}()
	defer pr.Close()

	var result []string
	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		result = append(result, scanner.Text())
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "closed pipe") {
		return result, err
	}
	return result, nil
}
