package dnscache

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// flowIdle is how long an outbound flow is remembered without being
	// reported again.
	flowIdle = 30 * time.Minute
	// DefaultMaxFlows bounds the outbound-flow set.
	DefaultMaxFlows = 65536
)

type flowKey struct {
	src, dst netip.Addr
	port     uint16
	udp      bool
}

// OutboundFlows remembers which (source, destination, port, protocol) flows
// this host opened, from the collector's connect / first-send events.
//
// Throughput samples carry no direction, and a DNS name only describes where
// an outbound flow was headed. Looking names up only for flows known to be
// outbound keeps inbound flows from picking up a name (and from refreshing
// cache entries by lookup). Safe for concurrent use.
type OutboundFlows struct {
	mu    sync.Mutex
	max   int
	flows map[flowKey]time.Time
	swept time.Time
}

// NewOutboundFlows returns a set bounded at maxFlows (non-positive: default).
func NewOutboundFlows(maxFlows int) *OutboundFlows {
	if maxFlows <= 0 {
		maxFlows = DefaultMaxFlows
	}
	return &OutboundFlows{max: maxFlows, flows: make(map[flowKey]time.Time)}
}

func mkFlowKey(src, dst string, port uint16, udp bool) (flowKey, bool) {
	s, err := netip.ParseAddr(src)
	if err != nil {
		return flowKey{}, false
	}
	d, err := netip.ParseAddr(dst)
	if err != nil {
		return flowKey{}, false
	}
	return flowKey{s.Unmap().WithZone(""), d.Unmap().WithZone(""), port, udp}, true
}

// Add records an outbound flow seen at now.
func (f *OutboundFlows) Add(src, dst string, port uint16, udp bool, now time.Time) {
	if f == nil {
		return
	}
	k, ok := mkFlowKey(src, dst, port, udp)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, known := f.flows[k]; !known && len(f.flows) >= f.max {
		f.sweepLocked(now, true)
		if len(f.flows) >= f.max {
			return // full of live flows: this one just won't get names
		}
	}
	f.flows[k] = now
}

// Contains reports whether the flow is a known outbound flow, refreshing it.
func (f *OutboundFlows) Contains(src, dst string, port uint16, udp bool, now time.Time) bool {
	if f == nil {
		return false
	}
	k, ok := mkFlowKey(src, dst, port, udp)
	if !ok {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweepLocked(now, false)
	last, ok := f.flows[k]
	if !ok || now.Sub(last) >= flowIdle {
		return false
	}
	f.flows[k] = now
	return true
}

// Len returns the number of remembered flows.
func (f *OutboundFlows) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.flows)
}

func (f *OutboundFlows) sweepLocked(now time.Time, force bool) {
	if !force && now.Sub(f.swept) < flowIdle/4 {
		return
	}
	for k, t := range f.flows {
		if now.Sub(t) >= flowIdle {
			delete(f.flows, k)
		}
	}
	f.swept = now
}
