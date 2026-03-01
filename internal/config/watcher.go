package config

import (
	"context"
	"log"
	"os"
	"time"
)

// Watch polls the config file at path every interval. When the file's
// modification time changes (including when it is created for the first time),
// onChange is called. The goroutine exits cleanly when ctx is cancelled.
// If path is empty, Watch returns immediately.
func Watch(ctx context.Context, path string, interval time.Duration, onChange func()) {
	if path == "" {
		return
	}

	lastMod := modTime(path)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t := modTime(path)
			if !t.IsZero() && !t.Equal(lastMod) {
				lastMod = t
				log.Printf("config watcher: %s changed, reloading", path)
				onChange()
			}
		}
	}
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
