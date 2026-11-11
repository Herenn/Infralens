package dnscache

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

var (
	podA   = netip.MustParseAddr("10.244.1.7")
	podB   = netip.MustParseAddr("10.244.1.8")
	stripe = netip.MustParseAddr("52.1.2.3")
	t0     = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
)

func TestCacheAddLookup(t *testing.T) {
	c := NewCache(64)
	c.Add(podA, stripe, "api.stripe.com", 60*time.Second, t0)

	if name, ok := c.Lookup(podA, stripe, t0.Add(time.Second)); !ok || name != "api.stripe.com" {
		t.Errorf("Lookup = %q, %v", name, ok)
	}
	// Per client: podB never resolved this address.
	if _, ok := c.Lookup(podB, stripe, t0); ok {
		t.Error("podB must not see podA's resolution")
	}
	if _, ok := c.Lookup(podA, netip.MustParseAddr("52.1.2.4"), t0); ok {
		t.Error("unknown address must miss")
	}
}

// The same CDN address can serve many names; each client keeps the name it
// asked for, and a newer answer for the same pair replaces the old one.
func TestCacheSharedAddressPerClient(t *testing.T) {
	c := NewCache(64)
	cdn := netip.MustParseAddr("104.16.0.1")
	c.Add(podA, cdn, "a.example.com", time.Minute, t0)
	c.Add(podB, cdn, "b.example.org", time.Minute, t0)

	if n, _ := c.Lookup(podA, cdn, t0); n != "a.example.com" {
		t.Errorf("podA = %q", n)
	}
	if n, _ := c.Lookup(podB, cdn, t0); n != "b.example.org" {
		t.Errorf("podB = %q", n)
	}

	c.Add(podA, cdn, "c.example.com", time.Minute, t0.Add(time.Second))
	if n, _ := c.Lookup(podA, cdn, t0.Add(2*time.Second)); n != "c.example.com" {
		t.Errorf("after re-resolution podA = %q, want newest name", n)
	}
}

func TestCacheRetention(t *testing.T) {
	c := NewCache(64)
	c.Add(podA, stripe, "api.stripe.com", 0, t0) // TTL 0

	// Kept for MinRetention even with TTL 0: clients use resolved addresses
	// past the TTL.
	if _, ok := c.Lookup(podA, stripe, t0.Add(MinRetention-time.Second)); !ok {
		t.Fatal("entry must survive a short TTL")
	}
	// That hit counted as use and extended the entry.
	if _, ok := c.Lookup(podA, stripe, t0.Add(2*MinRetention-2*time.Second)); !ok {
		t.Fatal("a lookup hit must extend retention")
	}
	// Unused for longer than MinRetention: gone, and removed.
	if _, ok := c.Lookup(podA, stripe, t0.Add(4*MinRetention)); ok {
		t.Fatal("unused entry must expire")
	}
	if c.Len() != 0 {
		t.Errorf("expired entry not removed, len=%d", c.Len())
	}
}

// Lookups extend an entry, but never past HardRetention after it was
// learned (the reviewed version could be kept alive forever this way).
func TestCacheHardRetentionCap(t *testing.T) {
	c := NewCache(64)
	c.Add(podA, stripe, "api.stripe.com", 0, t0)
	now := t0
	for now.Before(t0.Add(HardRetention - MinRetention)) {
		now = now.Add(MinRetention / 2)
		if _, ok := c.Lookup(podA, stripe, now); !ok {
			t.Fatalf("entry in use dropped at %s", now.Sub(t0))
		}
	}
	if _, ok := c.Lookup(podA, stripe, t0.Add(HardRetention)); ok {
		t.Error("entry outlived HardRetention through lookups")
	}

	// A new matching answer restarts it.
	c.Add(podA, stripe, "api.stripe.com", 0, t0)
	c.Add(podA, stripe, "api.stripe.com", 0, t0.Add(HardRetention-time.Minute))
	if _, ok := c.Lookup(podA, stripe, t0.Add(HardRetention+time.Minute)); !ok {
		t.Error("re-learning must restart retention")
	}
}

func TestCacheLongTTLCapped(t *testing.T) {
	c := NewCache(64)
	c.Add(podA, stripe, "api.stripe.com", 30*24*time.Hour, t0)
	if _, ok := c.Lookup(podA, stripe, t0.Add(HardRetention+time.Second)); ok {
		t.Error("TTL must be capped at HardRetention")
	}
}

func addrN(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{100, byte(i >> 16), byte(i >> 8), byte(i)})
}

// One client can only ever push out its own entries: neither its quota nor a
// full cache lets it evict another client's names (PoC: 512 forged packets
// used to empty the whole cache).
func TestCachePerClientQuotaAndFairEviction(t *testing.T) {
	const max = 64 // per-client quota 4, host quota 16
	c := NewCache(max)
	c.Add(podA, stripe, "api.stripe.com", time.Hour, t0)

	attacker := netip.MustParseAddr("10.244.1.66")
	for i := 0; i < 10000; i++ {
		c.Add(attacker, addrN(i), fmt.Sprintf("h%d.example", i), time.Hour, t0)
	}
	if _, ok := c.Lookup(podA, stripe, t0); !ok {
		t.Fatal("one client evicted another's entry")
	}
	if c.Len() != 1+max/16 {
		t.Errorf("len = %d, want victim + attacker quota %d", c.Len(), max/16)
	}

	// Many clients filling the whole cache: eviction comes from the largest.
	for p := 0; p < 40; p++ {
		cl := netip.AddrFrom4([4]byte{10, 244, 9, byte(p)})
		for i := 0; i < 4; i++ {
			c.Add(cl, addrN(i), "x.example", time.Hour, t0)
		}
	}
	if c.Len() > max {
		t.Fatalf("len = %d exceeds bound %d", c.Len(), max)
	}
	if _, ok := c.Lookup(podA, stripe, t0); !ok {
		t.Error("the smallest client's only entry was evicted while larger ones existed")
	}
}

func TestCacheHostQuota(t *testing.T) {
	c := NewCache(64)
	for i := 0; i < 100; i++ {
		c.Add(hostIdentity, addrN(i), "h.example", time.Hour, t0)
	}
	if c.Len() != 16 {
		t.Errorf("host identity holds %d, want quota 16", c.Len())
	}
}

func TestCacheLRUWithinClient(t *testing.T) {
	c := NewCache(64) // quota 4
	for i := 0; i < 4; i++ {
		c.Add(podA, addrN(i), "h.example", time.Hour, t0)
	}
	c.Lookup(podA, addrN(0), t0) // touch the oldest
	c.Add(podA, addrN(99), "h.example", time.Hour, t0)
	if _, ok := c.Lookup(podA, addrN(0), t0); !ok {
		t.Error("recently used entry evicted")
	}
	if _, ok := c.Lookup(podA, addrN(1), t0); ok {
		t.Error("least recently used entry kept")
	}
}

func TestCacheDefaultSize(t *testing.T) {
	if c := NewCache(0); c.max != DefaultMaxEntries {
		t.Errorf("max = %d", c.max)
	}
	if c := NewCache(1); c.perClient != 1 || c.hostQuota != 1 {
		t.Errorf("tiny cache quotas %d/%d", c.perClient, c.hostQuota)
	}
}

func TestCacheConcurrent(t *testing.T) {
	c := NewCache(256)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			cl := netip.AddrFrom4([4]byte{10, 0, 0, byte(g)})
			for i := 0; i < 2000; i++ {
				a := netip.AddrFrom4([4]byte{10, 1, byte(g), byte(i)})
				c.Add(cl, a, "x.example", time.Minute, t0)
				c.Lookup(cl, a, t0)
			}
		}(g)
	}
	wg.Wait()
	if c.Len() > 256 {
		t.Errorf("len = %d exceeds bound", c.Len())
	}
}
