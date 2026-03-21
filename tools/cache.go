package tools

import (
	"fmt"
	"sync"
	"time"
)

const (
	CacheTTLSearch  = 5 * time.Minute
	CacheTTLProduct = 10 * time.Minute
	CacheTTLBonus   = 2 * time.Minute
	CacheTTLStores  = 30 * time.Minute
)

type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

// Cache is a simple in-memory TTL cache storing serialised JSON bytes.
type Cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

// GlobalCache is the shared cache instance used by all tool handlers.
var GlobalCache = &Cache{entries: make(map[string]cacheEntry)}

// Get returns cached bytes for key. Returns nil, false on miss or expiry.
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return e.data, true
}

// Set stores data under key with the given TTL.
func (c *Cache) Set(key string, data []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{data: data, expiresAt: time.Now().Add(ttl)}
}

// Invalidate removes all entries whose key starts with prefix.
func (c *Cache) Invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.entries, k)
		}
	}
}

// Size returns the number of non-expired entries currently in the cache.
func (c *Cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	n := 0
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		} else {
			n++
		}
	}
	return n
}

// Cache key helpers.

func SearchCacheKey(query string, limit int) string {
	return fmt.Sprintf("search:%s:%d", query, limit)
}

func ProductCacheKey(id int) string {
	return fmt.Sprintf("product:%d", id)
}

func ProductFullCacheKey(id int) string {
	return fmt.Sprintf("product_full:%d", id)
}
