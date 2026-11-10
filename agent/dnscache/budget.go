package dnscache

import "time"

// DefaultMaxPPS is the default packet budget: how many captured packets per
// second the agent will read and parse. A busy node's legitimate DNS traffic
// (query + response per lookup) fits comfortably; a flood beyond it is left
// in the socket buffer, where the kernel drops it (counted, see Stats) at no
// further cost to the agent.
const DefaultMaxPPS = 5000

// tokenBucket is a plain token bucket; not safe for concurrent use (the
// capture goroutine owns it).
type tokenBucket struct {
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(perSecond int, now time.Time) *tokenBucket {
	r := float64(perSecond)
	return &tokenBucket{rate: r, burst: r, tokens: r, last: now}
}

// take consumes a token if one is available at now. Otherwise it reports how
// long until the next one.
func (b *tokenBucket) take(now time.Time) (bool, time.Duration) {
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = min(b.burst, b.tokens+el*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
	return false, max(wait, time.Millisecond)
}

// refund returns a token taken for a read that delivered no packet.
func (b *tokenBucket) refund() {
	b.tokens = min(b.burst, b.tokens+1)
}
