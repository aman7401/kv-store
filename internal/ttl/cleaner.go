package ttl

import (
	"sync"
	"time"
)

// Cleaner runs a background goroutine that periodically evicts expired keys.
// It calls the provided evict callback for each expired key found.
type Cleaner struct {
	interval time.Duration
	keys     func() []string       // snapshot of current keys
	isExpired func(key string) bool // true if key should be evicted
	evict    func(key string)
	stop     chan struct{}
	once     sync.Once
}

func NewCleaner(
	interval time.Duration,
	keys func() []string,
	isExpired func(key string) bool,
	evict func(key string),
) *Cleaner {
	return &Cleaner{
		interval:  interval,
		keys:      keys,
		isExpired: isExpired,
		evict:     evict,
		stop:      make(chan struct{}),
	}
}

func (c *Cleaner) Start() {
	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.sweep()
			case <-c.stop:
				return
			}
		}
	}()
}

func (c *Cleaner) Stop() {
	c.once.Do(func() { close(c.stop) })
}

func (c *Cleaner) sweep() {
	for _, key := range c.keys() {
		if c.isExpired(key) {
			c.evict(key)
		}
	}
}
