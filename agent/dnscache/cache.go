package dnscache

import (
	"container/list"
	"net/netip"
	"sync"
	"time"
)

const (
	// DefaultMaxEntries bounds the cache. One entry is one (client, address)
	// pair - about 200 bytes - so the default costs a few MiB at most.
	DefaultMaxEntries = 32768

	// MinRetention is how long a learned name is kept, even when the record's
	// TTL is shorter, and how far each lookup hit extends it.
	//
	// Retaining past a short TTL is deliberate: the cache answers "what name
	// did this client use to get this address", which stays true after the
	// TTL - connection pools, nginx upstreams and JVMs keep using a resolved
	// address well past it, and the answer is not repeated on the wire.
	MinRetention = 15 * time.Minute

	// HardRetention caps how long an entry lives after it was learned, no
	// matter how often it is looked up. Only a new matching answer for the
	// same pair restarts it. The backend keeps the name once reported, so a
	// long-lived flow loses nothing when the agent forgets it.
	HardRetention = 24 * time.Hour
)

type cacheKey struct {
	client netip.Addr // the zero Addr is the host identity, see Tracker
	addr   netip.Addr
}

type cacheEntry struct {
	key       cacheKey
	name      string
	expires   time.Time
	hardUntil time.Time
}

// Cache is a bounded, TTL-aware map from (client, address) to the DNS name the
// client resolved that address from. Safe for concurrent use.
//
// Eviction is per client: each client has its own LRU list and quota, and
// when the cache as a whole is full the entry evicted comes from whichever
// client holds the most. One client - however many names it resolves - can
// therefore only ever push out its own entries, never another's, unless that
// other client holds more than it does.
type Cache struct {
	mu        sync.Mutex
	max       int
	perClient int
	hostQuota int
	entries   map[cacheKey]*list.Element
	clients   map[netip.Addr]*list.List // front = most recently used
}

// NewCache returns a cache holding at most maxEntries pairs; a non-positive
// value means DefaultMaxEntries. Each client may hold at most 1/16 of that
// (the host identity, shared by every host process, 1/4).
func NewCache(maxEntries int) *Cache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	return &Cache{
		max:       maxEntries,
		perClient: max(maxEntries/16, 1),
		hostQuota: max(maxEntries/4, 1),
		entries:   make(map[cacheKey]*list.Element),
		clients:   make(map[netip.Addr]*list.List),
	}
}

func (c *Cache) quota(client netip.Addr) int {
	if client == hostIdentity {
		return c.hostQuota
	}
	return c.perClient
}

// Add records that client received addr as an answer for name. A newer
// answer for the same pair replaces the name and restarts its retention: the
// most recent resolution is the best predictor of what the next connection
// to that address is for.
func (c *Cache) Add(client, addr netip.Addr, name string, ttl time.Duration, now time.Time) {
	k := cacheKey{client.Unmap(), addr.Unmap()}
	hard := now.Add(HardRetention)
	expires := now.Add(min(max(ttl, MinRetention), HardRetention))

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[k]; ok {
		e := el.Value.(*cacheEntry)
		if e.name != name || expires.After(e.expires) {
			e.expires = expires
		}
		e.name = name
		e.hardUntil = hard
		c.clients[k.client].MoveToFront(el)
		return
	}

	// Evict first (removeElement drops a client's list when it empties),
	// then fetch or create this client's list.
	if lru := c.clients[k.client]; lru != nil {
		for lru.Len() > 0 && lru.Len() >= c.quota(k.client) {
			c.removeElement(lru, lru.Back())
		}
	}
	for len(c.entries) >= c.max {
		vl := c.clients[c.largestClient()]
		c.removeElement(vl, vl.Back())
	}
	lru := c.clients[k.client]
	if lru == nil {
		lru = list.New()
		c.clients[k.client] = lru
	}
	c.entries[k] = lru.PushFront(&cacheEntry{key: k, name: name, expires: expires, hardUntil: hard})
}

// largestClient returns the client holding the most entries. Linear in the
// number of clients, which is the number of pods on a node plus one; only
// reached when the whole cache is full.
func (c *Cache) largestClient() netip.Addr {
	var best netip.Addr
	bestN := -1
	for cl, l := range c.clients {
		if l.Len() > bestN {
			best, bestN = cl, l.Len()
		}
	}
	return best
}

// Lookup returns the name client resolved addr from, if it is still held.
// A hit counts as use: it keeps the entry for at least MinRetention from now,
// but never past HardRetention after it was learned.
func (c *Cache) Lookup(client, addr netip.Addr, now time.Time) (string, bool) {
	k := cacheKey{client.Unmap(), addr.Unmap()}

	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[k]
	if !ok {
		return "", false
	}
	lru := c.clients[k.client]
	e := el.Value.(*cacheEntry)
	if !now.Before(e.expires) {
		c.removeElement(lru, el)
		return "", false
	}
	if keep := now.Add(MinRetention); keep.After(e.expires) {
		e.expires = keep
	}
	if e.expires.After(e.hardUntil) {
		e.expires = e.hardUntil
	}
	lru.MoveToFront(el)
	return e.name, true
}

// Len returns the number of entries, including any that have expired but not
// yet been looked up or evicted.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache) removeElement(lru *list.List, el *list.Element) {
	e := lru.Remove(el).(*cacheEntry)
	delete(c.entries, e.key)
	if lru.Len() == 0 {
		delete(c.clients, e.key.client)
	}
}
