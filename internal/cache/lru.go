package cache

import (
	"container/list"
	"hash/maphash"
	"sync"
	"time"
)

// lru is a sharded, size-bounded LRU with a per-entry expiry. Get on a hit does not
// allocate (struct keys, pointer values, time.Now without formatting).
//
// Written instead of hashicorp/golang-lru/v2/expirable because that one starts a
// never-stopped janitor goroutine per instance and takes one global lock; sharding keeps
// lock contention low on many-core redirect nodes.
type lru[K comparable, V any] struct {
	shards []lruShard[K, V]
	mask   uint64
	seed   maphash.Seed
	hash   func(maphash.Seed, K) uint64
	now    func() time.Time
}

type lruShard[K comparable, V any] struct {
	mu    sync.Mutex
	cap   int
	ll    list.List
	items map[K]*list.Element
	_     [40]byte // keep shards on separate cache lines
}

type lruEntry[K comparable, V any] struct {
	key     K
	val     V
	expires int64 // unix nanos
}

func newLRU[K comparable, V any](size, shards int, hash func(maphash.Seed, K) uint64) *lru[K, V] {
	if shards <= 0 {
		shards = 64
	}
	n := 1
	for n < shards {
		n <<= 1
	}
	if size < n {
		size = n
	}
	c := &lru[K, V]{shards: make([]lruShard[K, V], n), mask: uint64(n - 1), seed: maphash.MakeSeed(), hash: hash, now: time.Now}
	per := size / n
	for i := range c.shards {
		c.shards[i].cap = per
		c.shards[i].items = make(map[K]*list.Element)
	}
	return c
}

func (c *lru[K, V]) shard(k K) *lruShard[K, V] {
	return &c.shards[c.hash(c.seed, k)&c.mask]
}

func (c *lru[K, V]) Get(k K) (v V, ok bool) {
	s := c.shard(k)
	now := c.now().UnixNano()
	s.mu.Lock()
	if e, hit := s.items[k]; hit {
		ent := e.Value.(*lruEntry[K, V])
		if now < ent.expires {
			s.ll.MoveToFront(e)
			v, ok = ent.val, true
		} else {
			s.ll.Remove(e)
			delete(s.items, k)
		}
	}
	s.mu.Unlock()
	return v, ok
}

func (c *lru[K, V]) Add(k K, v V, ttl time.Duration) {
	s := c.shard(k)
	exp := c.now().Add(ttl).UnixNano()
	s.mu.Lock()
	if e, hit := s.items[k]; hit {
		ent := e.Value.(*lruEntry[K, V])
		ent.val, ent.expires = v, exp
		s.ll.MoveToFront(e)
		s.mu.Unlock()
		return
	}
	s.items[k] = s.ll.PushFront(&lruEntry[K, V]{key: k, val: v, expires: exp})
	for s.ll.Len() > s.cap {
		old := s.ll.Back()
		s.ll.Remove(old)
		delete(s.items, old.Value.(*lruEntry[K, V]).key)
	}
	s.mu.Unlock()
}

func (c *lru[K, V]) Remove(k K) {
	s := c.shard(k)
	s.mu.Lock()
	if e, hit := s.items[k]; hit {
		s.ll.Remove(e)
		delete(s.items, k)
	}
	s.mu.Unlock()
}

func (c *lru[K, V]) Len() int {
	n := 0
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		n += s.ll.Len()
		s.mu.Unlock()
	}
	return n
}

func (c *lru[K, V]) Purge() {
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		s.ll.Init()
		clear(s.items)
		s.mu.Unlock()
	}
}
