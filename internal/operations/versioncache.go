package operations

import (
	"sync"
	"time"
)

const versionCacheTTL = 5 * time.Minute

type versionEntry struct {
	version   string
	fetchedAt time.Time
}

// VersionCache is a simple in-memory TTL cache for operation version strings.
type VersionCache struct {
	mu      sync.RWMutex
	entries map[string]versionEntry
}

// NewVersionCache creates an empty VersionCache.
func NewVersionCache() *VersionCache {
	return &VersionCache{entries: make(map[string]versionEntry)}
}

// Set stores a version string for the given operation name.
func (c *VersionCache) Set(name, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[name] = versionEntry{version: version, fetchedAt: time.Now()}
}

// Get retrieves a cached version string. Returns ("", false) if missing or expired.
func (c *VersionCache) Get(name string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[name]
	if !ok {
		return "", false
	}
	if time.Since(e.fetchedAt) > versionCacheTTL {
		return "", false
	}
	return e.version, true
}
