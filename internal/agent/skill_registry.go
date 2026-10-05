package agent

import (
	"context"
	"sync"
	"time"
)

// skillRegistry is immutable once ready closes. Menu entries and the automatic
// catalog derive from the same discovery result, including user-only skills.
type skillRegistry struct {
	ready   chan struct{}
	cancel  context.CancelFunc
	skills  []LocalSkill
	catalog string
	err     error
}

// skillRegistryCache synchronizes discovery independently of engine operations.
// A caller can stop waiting without cancelling another caller's discovery.
type skillRegistryCache struct {
	mu      sync.Mutex
	current *skillRegistry
}

func (c *skillRegistryCache) load(ctx context.Context, discover func(context.Context) []LocalSkill) (*skillRegistry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	registry := c.current
	if registry == nil {
		scanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		registry = &skillRegistry{ready: make(chan struct{}), cancel: cancel}
		c.current = registry
		go func() {
			defer cancel()
			registry.skills = discover(scanCtx)
			registry.catalog = renderClientSkills(registry.skills)
			registry.err = scanCtx.Err()
			// Failed scans can be retried; an old scan must never evict a newer
			// conversation's registry after reset.
			c.mu.Lock()
			if registry.err != nil && c.current == registry {
				c.current = nil
			}
			close(registry.ready)
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-registry.ready:
		return registry, registry.err
	}
}

func (c *skillRegistryCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil {
		c.current.cancel()
		c.current = nil
	}
}
