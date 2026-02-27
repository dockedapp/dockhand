package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

// UpgradeResult is returned after a completed upgrade.
type UpgradeResult struct {
	Strategy string // "compose" or "standalone"
	OldImage string
	NewImage string
	Duration time.Duration
}

// UpgradeContainer upgrades a container by ID or name, streaming progress
// lines to output. Auto-detects Compose vs. standalone.
func (dc *Client) UpgradeContainer(ctx context.Context, id string, composeBinary string, output func(string)) (*UpgradeResult, error) {
	info, err := dc.InspectContainer(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}
	if info == nil {
		return nil, fmt.Errorf("container %q not found", id)
	}

	start := time.Now()

	var result *UpgradeResult
	if info.ComposeProject != "" {
		result, err = dc.upgradeCompose(ctx, info, composeBinary, output)
	} else {
		result, err = dc.upgradeStandalone(ctx, info, output)
	}
	if err != nil {
		return nil, err
	}
	result.Duration = time.Since(start)
	return result, nil
}

// upgradeCompose uses docker compose pull + up -d to upgrade a Compose service.
func (dc *Client) upgradeCompose(ctx context.Context, info *ContainerInfo, composeBinary string, output func(string)) (*UpgradeResult, error) {
	workDir := info.ComposeWorkingDir
	if workDir == "" {
		return nil, fmt.Errorf("compose working directory unknown for container %q", info.Name)
	}

	composeFile, err := findComposeFile(workDir)
	if err != nil {
		return nil, err
	}

	service := info.ComposeService
	output(fmt.Sprintf("→ Compose project: %s  service: %s", info.ComposeProject, service))
	output(fmt.Sprintf("→ Compose file: %s", composeFile))
	output("")
	output("Pulling latest image...")

	if err := streamCommand(ctx, output, composeBinary, "compose", "-f", composeFile, "pull", service); err != nil {
		return nil, fmt.Errorf("compose pull: %w", err)
	}
	output("")
	output("Recreating container...")
	if err := streamCommand(ctx, output, composeBinary, "compose", "-f", composeFile, "up", "-d", service); err != nil {
		return nil, fmt.Errorf("compose up: %w", err)
	}

	return &UpgradeResult{Strategy: "compose", OldImage: info.Image, NewImage: info.Image}, nil
}

// upgradeStandalone pulls the new image then stops-recreate-starts the container.
// On any failure after the stop, it attempts a rollback.
func (dc *Client) upgradeStandalone(ctx context.Context, info *ContainerInfo, output func(string)) (*UpgradeResult, error) {
	output(fmt.Sprintf("→ Standalone container: %s  image: %s", info.Name, info.Image))
	output("")

	inspect, err := dc.c.ContainerInspect(ctx, info.ID)
	if err != nil {
		return nil, fmt.Errorf("full inspect: %w", err)
	}

	oldImageRef := inspect.Config.Image

	output("Pulling latest image...")
	if err := dc.pullImage(ctx, oldImageRef, output); err != nil {
		return nil, fmt.Errorf("pull: %w", err)
	}

	newImageInspect, _, err := dc.c.ImageInspectWithRaw(ctx, oldImageRef)
	if err != nil {
		return nil, fmt.Errorf("inspect new image: %w", err)
	}

	output("")
	output("Stopping container...")
	stopTimeout := 30
	if err := dc.c.ContainerStop(ctx, info.ID, container.StopOptions{Timeout: &stopTimeout}); err != nil {
		return nil, fmt.Errorf("stop: %w", err)
	}

	// Rename old container to a backup name so the original name is free
	backupName := strings.TrimPrefix(inspect.Name, "/") + "_old_" + fmt.Sprintf("%d", time.Now().Unix())
	if err := dc.c.ContainerRename(ctx, info.ID, backupName); err != nil {
		_ = dc.c.ContainerStart(ctx, info.ID, container.StartOptions{})
		return nil, fmt.Errorf("rename backup: %w", err)
	}
	originalName := strings.TrimPrefix(inspect.Name, "/")

	output("Creating new container...")
	resp, createErr := dc.c.ContainerCreate(ctx,
		inspect.Config,
		inspect.HostConfig,
		buildNetworkConfig(inspect),
		nil, // platform — use host default
		originalName,
	)
	if createErr != nil {
		_ = dc.c.ContainerRename(ctx, backupName, originalName)
		_ = dc.c.ContainerStart(ctx, info.ID, container.StartOptions{})
		return nil, fmt.Errorf("create: %w (rolled back)", createErr)
	}

	output("Starting new container...")
	if err := dc.c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = dc.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		_ = dc.c.ContainerRename(ctx, backupName, originalName)
		_ = dc.c.ContainerStart(ctx, info.ID, container.StartOptions{})
		return nil, fmt.Errorf("start: %w (rolled back)", err)
	}

	output("Removing old container...")
	_ = dc.c.ContainerRemove(ctx, backupName, container.RemoveOptions{Force: true})

	output("")
	output(fmt.Sprintf("✓ Upgrade complete  old=%s  new=%s", shortID(inspect.Image), shortID(newImageInspect.ID)))

	return &UpgradeResult{
		Strategy: "standalone",
		OldImage: oldImageRef,
		NewImage: newImageInspect.ID,
	}, nil
}

// pullImage pulls an image and streams JSON progress events as readable lines.
func (dc *Client) pullImage(ctx context.Context, ref string, output func(string)) error {
	rc, err := dc.c.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()

	dec := json.NewDecoder(rc)
	for {
		var ev struct {
			Status   string `json:"status"`
			Progress string `json:"progress"`
			Error    string `json:"error"`
			ID       string `json:"id"`
		}
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if ev.Error != "" {
			return fmt.Errorf("pull: %s", ev.Error)
		}
		if ev.Status != "" {
			line := ev.Status
			if ev.ID != "" {
				line = ev.ID + ": " + line
			}
			if ev.Progress != "" {
				line += "  " + ev.Progress
			}
			output(line)
		}
	}
	return nil
}

// buildNetworkConfig reconstructs a NetworkingConfig from a ContainerJSON.
func buildNetworkConfig(inspect types.ContainerJSON) *network.NetworkingConfig {
	cfg := &network.NetworkingConfig{
		EndpointsConfig: make(map[string]*network.EndpointSettings),
	}
	for netName, ep := range inspect.NetworkSettings.Networks {
		cfg.EndpointsConfig[netName] = &network.EndpointSettings{
			IPAMConfig:          ep.IPAMConfig,
			Links:               ep.Links,
			Aliases:             ep.Aliases,
			NetworkID:           ep.NetworkID,
			EndpointID:          ep.EndpointID,
			Gateway:             ep.Gateway,
			IPAddress:           ep.IPAddress,
			IPPrefixLen:         ep.IPPrefixLen,
			IPv6Gateway:         ep.IPv6Gateway,
			GlobalIPv6Address:   ep.GlobalIPv6Address,
			GlobalIPv6PrefixLen: ep.GlobalIPv6PrefixLen,
			MacAddress:          ep.MacAddress,
		}
	}
	return cfg
}

// findComposeFile searches workDir for a compose file in priority order.
func findComposeFile(workDir string) (string, error) {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		path := filepath.Join(workDir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %s", workDir)
}

// streamCommand runs cmd with args, streaming combined stdout+stderr to output.
func streamCommand(ctx context.Context, output func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	drain := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		for s.Scan() {
			output(s.Text())
		}
	}
	go drain(stdout)
	go drain(stderr)
	wg.Wait()

	return cmd.Wait()
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
