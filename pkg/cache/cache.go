package cache

import (
	"sync"
	"time"
)

type Option[K comparable, V any] func(*Cache[K, V])

func WithTTL[K comparable, V any](ttl time.Duration) Option[K, V] {
	return func(c *Cache[K, V]) {
		c.itemTTL = &ttl
	}
}

func WithCleanupInterval[K comparable, V any](interval time.Duration) Option[K, V] {
	return func(c *Cache[K, V]) {
		c.itemCleanupInterval = interval
	}
}

type Cache[K comparable, V any] struct {
	items map[K]item[V]
	lock  sync.Mutex

	itemTTL             *time.Duration
	itemCleanupInterval time.Duration
}

func New[K comparable, V any](
	opts ...Option[K, V],
) *Cache[K, V] {
	cache := &Cache[K, V]{
		items:               make(map[K]item[V]),
		itemCleanupInterval: 10 * time.Second,
	}

	for _, opt := range opts {
		opt(cache)
	}

	go func() {
		if cache.itemTTL == nil {
			return
		}

		for range time.Tick(cache.itemCleanupInterval) {
			cache.lock.Lock()
			for key, item := range cache.items {
				if item.isExpired() {
					delete(cache.items, key)
				}
			}
			cache.lock.Unlock()
		}
	}()

	return cache
}

func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	value, ok := c.items[key]

	return value.value, ok
}

func (c *Cache[K, V]) Set(key K, value V) {
	c.lock.Lock()
	defer c.lock.Unlock()

	var ttl *time.Time
	if c.itemTTL != nil {
		ttl = ptr(time.Now().Add(*c.itemTTL))
	}

	c.items[key] = item[V]{
		value: value,
		ttl:   ttl,
	}
}

func (c *Cache[K, V]) Unset(key K) {
	c.lock.Lock()
	defer c.lock.Unlock()

	delete(c.items, key)
}

type item[V any] struct {
	value V
	ttl   *time.Time
}

func (i item[V]) isExpired() bool {
	if i.ttl == nil {
		return false
	}

	return time.Now().After(*i.ttl)
}

func ptr[T any](v T) *T {
	return &v
}
