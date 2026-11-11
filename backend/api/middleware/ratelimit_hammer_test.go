package middleware

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// TestLoginLimiterConcurrentBounds (from the review's hammer): 32 goroutines
// taking and refunding across 30,000 client and 20,000 peer identities. No
// bucket goes negative and every map stays within its bound. Run with -race.
func TestLoginLimiterConcurrentBounds(t *testing.T) {
	l := newLoginLimiter(time.Now)
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g)))
			for i := 0; i < 5000; i++ {
				c := fmt.Sprintf("c%d", r.Intn(30000))
				p := fmt.Sprintf("p%d", r.Intn(20000))
				if ok, _ := l.take(c, p); ok && r.Intn(10) == 0 {
					l.refund(c, p)
				}
			}
		}(g)
	}
	wg.Wait()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.clients {
		if b.tokens < 0 {
			t.Fatalf("client bucket %s went negative: %v", k, b.tokens)
		}
	}
	for k, b := range l.peers {
		if b.tokens < 0 {
			t.Fatalf("peer bucket %s went negative: %v", k, b.tokens)
		}
	}
	if l.global.tokens < 0 || len(l.trusted) > loginMaxTrusted || len(l.clients) > loginMaxClients || len(l.peers) > loginMaxClients {
		t.Fatalf("bounds violated: global=%.1f trusted=%d clients=%d peers=%d", l.global.tokens, len(l.trusted), len(l.clients), len(l.peers))
	}
}
