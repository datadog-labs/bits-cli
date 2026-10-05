package agent

import (
	"context"
	"sync"
	"time"
)

const skillDiscoveryTimeout = 5 * time.Second

// skillRegistry is immutable once cached. Menu entries and the automatic
// catalog derive from the same discovery result, including user-only skills.
type skillRegistry struct {
	skills  []registeredSkill
	catalog string
}

// skillRegistryCache holds one discovery result per conversation. Discovery is
// bounded, so it runs under the lock: concurrent callers share one scan, and a
// reset cannot interleave with it. Failed scans are not cached.
type skillRegistryCache struct {
	mu      sync.Mutex
	current *skillRegistry
}

func (c *skillRegistryCache) load(ctx context.Context, discover func(context.Context) []registeredSkill) (*skillRegistry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil {
		return c.current, nil
	}
	scanCtx, cancel := context.WithTimeout(ctx, skillDiscoveryTimeout)
	defer cancel()
	skills := discover(scanCtx)
	if err := scanCtx.Err(); err != nil {
		return nil, err
	}
	c.current = &skillRegistry{skills: skills, catalog: renderClientSkills(skills)}
	return c.current, nil
}

func (c *skillRegistryCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = nil
}
