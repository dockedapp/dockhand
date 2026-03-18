package docker

import (
	"bufio"
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// LogOptions controls how logs are streamed.
type LogOptions struct {
	Tail   string // number of lines or "all"
	Follow bool
}

// StreamLogs streams container logs to out, demultiplexing the Docker
// multiplexed stream (separate stdout/stderr headers) into plain text lines.
// Each call to line(text) receives one line. Blocks until the stream ends
// or ctx is cancelled.
func (dc *Client) StreamLogs(ctx context.Context, id string, opts LogOptions, line func(string)) error {
	tail := opts.Tail
	if tail == "" {
		tail = "100"
	}

	rc, err := dc.c.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
		Tail:       tail,
	})
	if err != nil {
		return err
	}
	defer rc.Close()

	// Docker multiplexes stdout/stderr; stdcopy.StdCopy demultiplexes into two writers.
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, rc)
		pw.CloseWithError(err)
	}()
	// Close the pipe reader when we're done scanning so the StdCopy
	// goroutine gets a write error and exits instead of leaking.
	defer pr.Close()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			line(scanner.Text())
		}
	}
	return scanner.Err()
}
