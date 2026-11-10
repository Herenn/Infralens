package dnscache

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// localRefresh is how often the host's own addresses and the routing
	// table are re-read, so new interfaces and new pods are picked up.
	localRefresh = 30 * time.Second
	// routeMissRefresh rate-limits the extra refresh triggered by a packet
	// for an address with no local route - typically a pod created since the
	// last refresh, whose first lookups (at startup) are exactly the ones
	// worth seeing.
	routeMissRefresh = time.Second

	// maxConsecutiveReadErrors is how many failed reads in a row make the
	// loop close the socket and reopen it, with backoff.
	maxConsecutiveReadErrors = 100
	minReopenBackoff         = time.Second
	maxReopenBackoff         = 5 * time.Minute

	// kernelStatsInterval is how often kernel-side drop counters are read.
	kernelStatsInterval = 5 * time.Second

	readBufferSize = 8 << 10 // > captureSnapLen
)

// errReadTimeout is returned by a packetSource when no packet arrived within
// its poll interval. It is not an error condition: it lets the loop notice
// context cancellation.
var errReadTimeout = errors.New("dnscache: read timeout")

// ErrCaptureUnsupported is returned by Start on platforms without AF_PACKET.
var ErrCaptureUnsupported = errors.New("DNS capture requires Linux (AF_PACKET)")

// packetSource delivers network-layer packets that passed Filter, with where
// each was seen.
type packetSource interface {
	ReadPacket(buf []byte) (int, PacketMeta, error)
	Close() error
}

// kernelStats is optionally implemented by a packetSource that can report
// packets the kernel dropped because the socket buffer was full (the
// counter resets on each read), and the receive buffer it actually got.
type kernelStats interface {
	KernelDrops() (uint64, error)
	RecvBuffer() int
}

// Config configures a Tracker. The zero value is usable.
type Config struct {
	// MaxEntries bounds the name cache; non-positive means DefaultMaxEntries.
	MaxEntries int

	// MaxPPS is the packet budget per second; non-positive means
	// DefaultMaxPPS.
	MaxPPS int

	// LocalAddrs lists this host's own addresses. Defaults to the addresses
	// of every interface in the agent's network namespace (the host's, since
	// the agent runs with hostNetwork).
	LocalAddrs func() ([]netip.Addr, error)

	// Routes loads the routing table used to recognise local workloads.
	// Defaults to the kernel's main table.
	Routes func() (*RouteTable, error)

	// Now is the clock. Overridable for tests.
	Now func() time.Time

	open  func() (packetSource, error) // test hook; default openPacketSource
	sleep func(time.Duration)          // test hook; default time.Sleep
}

// Stats are counters for diagnostics. All are cumulative except Pending,
// Entries and RecvBuffer.
type Stats struct {
	Packets       uint64 // packets handled
	Queries       uint64 // queries from local clients recorded
	QueriesFull   uint64 // queries not recorded: per-client or total pending limit
	Learned       uint64 // responses that matched a query and taught a name
	Unmatched     uint64 // responses with no matching query (unsolicited, late, duplicate, forged)
	NotLocal      uint64 // packets for clients that are not on this host
	OtherLeg      uint64 // copies of a packet seen on a leg other than the one used
	Malformed     uint64 // not a usable DNS query/response, or oversized
	BudgetWaits   uint64 // times the read loop waited for packet budget
	KernelDrops   uint64 // packets the kernel dropped (socket buffer full)
	Reopens       uint64 // capture socket reopened after errors
	Pending       int    // outstanding queries
	Entries       int    // cache size
	RecvBuffer    int    // effective socket receive buffer, bytes (0 if unknown)
	CaptureActive bool   // the capture loop is running
}

// String renders the stats as one log line.
func (s Stats) String() string {
	return fmt.Sprintf("packets=%d queries=%d queries_full=%d learned=%d unmatched=%d not_local=%d other_leg=%d malformed=%d budget_waits=%d kernel_drops=%d reopens=%d pending=%d entries=%d rcvbuf=%d active=%v",
		s.Packets, s.Queries, s.QueriesFull, s.Learned, s.Unmatched, s.NotLocal, s.OtherLeg, s.Malformed,
		s.BudgetWaits, s.KernelDrops, s.Reopens, s.Pending, s.Entries, s.RecvBuffer, s.CaptureActive)
}

// Tracker learns names from captured DNS traffic and answers "what name did
// this source resolve this destination from". A nil *Tracker is valid and
// knows no names, so callers need no feature checks.
//
// # What is believed
//
// A response teaches a name only if all of these hold:
//
//   - its client (destination address) is this host or a workload on it
//     (see RouteTable), and the copy is the one on the client's own leg:
//     for a pod, transmitted on the interface that routes to it; for the
//     host, received. Every other copy of a forwarded packet is ignored.
//   - it answers a query this tracker saw that client send, on the client's
//     own leg (for a pod: received from the interface that routes to it, so
//     a query spoofing a pod's address from elsewhere does not count), at
//     most QueryWindow earlier, with the same client port, server address,
//     transaction ID, question name and type. The query is then consumed.
//
// HandlePacket is called only from the capture goroutine; Hostname and Stats
// are safe from any goroutine.
type Tracker struct {
	cache      *Cache
	now        func() time.Time
	localAddrs func() ([]netip.Addr, error)
	loadRoutes func() (*RouteTable, error)
	open       func() (packetSource, error)
	sleep      func(time.Duration)
	maxPPS     int

	localMu sync.Mutex
	local   map[netip.Addr]struct{}
	localAt time.Time

	// Capture-goroutine state.
	pending  *pendingQueries
	routes   *RouteTable
	routesAt time.Time
	parser   dnsmessage.Parser
	nameBuf  [maxNameLen + 1]byte
	scratch  answerScratch
	answers  []Answer

	packets, queries, queriesFull, learned, unmatched atomic.Uint64
	notLocal, otherLeg, malformed, budgetWaits        atomic.Uint64
	kernelDrops, reopens                              atomic.Uint64
	pendingLen, recvBuffer                            atomic.Int64
	active                                            atomic.Bool
}

// NewTracker creates a tracker. It does not capture anything until Start.
func NewTracker(cfg Config) *Tracker {
	t := &Tracker{
		cache:      NewCache(cfg.MaxEntries),
		now:        cfg.Now,
		localAddrs: cfg.LocalAddrs,
		loadRoutes: cfg.Routes,
		open:       cfg.open,
		sleep:      cfg.sleep,
		maxPPS:     cfg.MaxPPS,
		pending:    newPendingQueries(),
		answers:    make([]Answer, 0, maxAddrsPerAnswer),
	}
	if t.now == nil {
		t.now = time.Now
	}
	if t.localAddrs == nil {
		t.localAddrs = interfaceAddrs
	}
	if t.loadRoutes == nil {
		t.loadRoutes = loadRoutes
	}
	if t.open == nil {
		t.open = openPacketSource
	}
	if t.sleep == nil {
		t.sleep = time.Sleep
	}
	if t.maxPPS <= 0 {
		t.maxPPS = DefaultMaxPPS
	}
	return t
}

// hostIdentity is the client key shared by every address of this host.
var hostIdentity = netip.Addr{}

// clientKey maps an address to the identity a cache entry is filed under.
// Loopback and the host's own addresses collapse into one host identity:
// a host process (or hostNetwork pod) commonly resolves through a loopback
// stub resolver, or from one interface address, and then connects from
// another. Pod addresses are not local to the host's network namespace, so
// pods keep separate entries.
func (t *Tracker) clientKey(a netip.Addr, now time.Time) netip.Addr {
	a = a.Unmap().WithZone("")
	if t.isHost(a, now) {
		return hostIdentity
	}
	return a
}

func (t *Tracker) isHost(a netip.Addr, now time.Time) bool {
	return a.IsLoopback() || t.isLocal(a, now)
}

func (t *Tracker) isLocal(a netip.Addr, now time.Time) bool {
	t.localMu.Lock()
	defer t.localMu.Unlock()
	if t.local == nil || now.Sub(t.localAt) >= localRefresh {
		// On error keep the previous set rather than forgetting the host.
		if addrs, err := t.localAddrs(); err == nil || t.local == nil {
			set := make(map[netip.Addr]struct{}, len(addrs))
			for _, la := range addrs {
				set[la.Unmap().WithZone("")] = struct{}{}
			}
			t.local = set
		}
		t.localAt = now
	}
	_, ok := t.local[a]
	return ok
}

func interfaceAddrs() ([]netip.Addr, error) {
	ifAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ifAddrs))
	for _, ia := range ifAddrs {
		var ip net.IP
		switch v := ia.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if a, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// refreshRoutes reloads the routing table; on error the previous table stays.
func (t *Tracker) refreshRoutes(now time.Time) {
	if rt, err := t.loadRoutes(); err == nil && rt != nil {
		t.routes = rt
	}
	t.routesAt = now
}

// localDevice is RouteTable.LocalDevice with periodic and on-miss refresh.
func (t *Tracker) localDevice(a netip.Addr, now time.Time) (int, bool) {
	if t.routes == nil || now.Sub(t.routesAt) >= localRefresh {
		t.refreshRoutes(now)
	}
	if dev, ok := t.routes.LocalDevice(a); ok {
		return dev, true
	}
	if now.Sub(t.routesAt) >= routeMissRefresh {
		t.refreshRoutes(now)
		return t.routes.LocalDevice(a)
	}
	return 0, false
}

// onClientLeg reports whether this copy of a packet is the one seen on the
// client's own leg - see the Tracker doc. query is the packet's direction:
// true for client -> server.
func (t *Tracker) onClientLeg(client netip.Addr, meta PacketMeta, query bool, now time.Time) bool {
	if !client.IsValid() || client.IsUnspecified() || client.IsMulticast() {
		t.notLocal.Add(1)
		return false
	}
	out := meta.PktType == PacketOutgoing
	if t.isHost(client, now) {
		// Host processes: the query is transmitted, the response received.
		if query == out {
			return true
		}
		t.otherLeg.Add(1)
		return false
	}
	dev, ok := t.localDevice(client, now)
	if !ok {
		t.notLocal.Add(1)
		return false
	}
	// Pods: the query is received from the pod's interface, the response
	// transmitted to it.
	if meta.Ifindex == dev && query != out {
		return true
	}
	t.otherLeg.Add(1)
	return false
}

// HandlePacket processes one captured network-layer packet and where it was
// seen. Anything that is not a DNS query or a matching response on the
// client's own leg is counted and ignored. Capture goroutine only.
func (t *Tracker) HandlePacket(meta PacketMeta, b []byte) {
	t.packets.Add(1)
	defer func() { t.pendingLen.Store(int64(t.pending.len())) }()

	pkt, err := ParseUDPPacket(b)
	if err != nil || len(pkt.Payload) < 12 || len(pkt.Payload) > maxDNSMessage {
		t.malformed.Add(1)
		return
	}
	isResponse := pkt.Payload[2]&0x80 != 0
	now := t.now()
	switch {
	case !isResponse && pkt.DstPort == dnsPort:
		t.handleQuery(meta, pkt, now)
	case isResponse && pkt.SrcPort == dnsPort:
		t.handleResponse(meta, pkt, now)
	default:
		t.malformed.Add(1)
	}
}

func (t *Tracker) handleQuery(meta PacketMeta, pkt UDPPacket, now time.Time) {
	client, server := pkt.Src.Unmap(), pkt.Dst.Unmap()
	if !t.onClientLeg(client, meta, true, now) {
		return
	}
	h, err := parseHead(&t.parser, pkt.Payload, t.nameBuf[:], false)
	if err != nil {
		t.malformed.Add(1)
		return
	}
	k := queryKey{client: client, server: server, port: pkt.SrcPort, id: h.id}
	if t.pending.add(k, string(h.qname), h.qtype, now) {
		t.queries.Add(1)
	} else {
		t.queriesFull.Add(1)
	}
}

func (t *Tracker) handleResponse(meta PacketMeta, pkt UDPPacket, now time.Time) {
	client, server := pkt.Dst.Unmap(), pkt.Src.Unmap()
	if !t.onClientLeg(client, meta, false, now) {
		return
	}
	h, err := parseHead(&t.parser, pkt.Payload, t.nameBuf[:], true)
	if err != nil {
		t.malformed.Add(1)
		return
	}
	// Matching happens before the answer section is read, so an
	// unsolicited response costs a header and question parse, nothing more.
	k := queryKey{client: client, server: server, port: pkt.DstPort, id: h.id}
	name, ok := t.pending.take(k, h.qname, h.qtype, now)
	if !ok {
		t.unmatched.Add(1)
		return
	}
	t.answers = parseAnswers(&t.parser, &h, &t.scratch, t.answers[:0])
	key := t.clientKey(client, now)
	learned := false
	for _, a := range t.answers {
		// Sinkholing resolvers answer blocked names with loopback; labelling
		// local traffic with an ad domain would be wrong, not helpful.
		if a.Addr.IsLoopback() {
			continue
		}
		t.cache.Add(key, a.Addr, name, time.Duration(a.TTL)*time.Second, now)
		learned = true
	}
	if learned {
		t.learned.Add(1)
	}
}

// Hostname returns the DNS name src resolved dst from, or "" if unknown.
// src and dst are IP address strings as the collector reports them.
func (t *Tracker) Hostname(src, dst string) string {
	if t == nil {
		return ""
	}
	s, err := netip.ParseAddr(src)
	if err != nil {
		return ""
	}
	d, err := netip.ParseAddr(dst)
	if err != nil {
		return ""
	}
	now := t.now()
	name, _ := t.cache.Lookup(t.clientKey(s, now), d.Unmap().WithZone(""), now)
	return name
}

// Stats returns diagnostic counters. Safe on a nil Tracker.
func (t *Tracker) Stats() Stats {
	if t == nil {
		return Stats{}
	}
	return Stats{
		Packets:       t.packets.Load(),
		Queries:       t.queries.Load(),
		QueriesFull:   t.queriesFull.Load(),
		Learned:       t.learned.Load(),
		Unmatched:     t.unmatched.Load(),
		NotLocal:      t.notLocal.Load(),
		OtherLeg:      t.otherLeg.Load(),
		Malformed:     t.malformed.Load(),
		BudgetWaits:   t.budgetWaits.Load(),
		KernelDrops:   t.kernelDrops.Load(),
		Reopens:       t.reopens.Load(),
		Pending:       int(t.pendingLen.Load()),
		Entries:       t.cache.Len(),
		RecvBuffer:    int(t.recvBuffer.Load()),
		CaptureActive: t.active.Load(),
	}
}

// Start opens the capture socket and processes DNS traffic in a background
// goroutine until ctx is cancelled.
//
// It fails open: when capture is unavailable (non-Linux, missing CAP_NET_RAW,
// a kernel without AF_PACKET) it returns the reason and starts nothing. The
// tracker stays valid and simply knows no names. Later read failures make the
// loop close and reopen the socket with backoff; logf receives a line for
// each such event (at most one per backoff step).
func (t *Tracker) Start(ctx context.Context, logf func(format string, args ...any)) error {
	if t == nil {
		return errors.New("dnscache: nil tracker")
	}
	src, err := t.open()
	if err != nil {
		return err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	t.active.Store(true) // visible as soon as Start returns
	go t.run(ctx, src, logf)
	return nil
}

// sleepCtx sleeps for d or until ctx is done.
func (t *Tracker) sleepCtx(ctx context.Context, d time.Duration) {
	const step = 100 * time.Millisecond
	for d > 0 && ctx.Err() == nil {
		s := min(d, step)
		t.sleep(s)
		d -= s
	}
}

// run is the capture loop. It owns src and closes it on return.
func (t *Tracker) run(ctx context.Context, src packetSource, logf func(format string, args ...any)) {
	t.active.Store(true)
	defer t.active.Store(false)
	defer func() {
		if src != nil {
			_ = src.Close()
		}
	}()

	buf := make([]byte, readBufferSize)
	bucket := newTokenBucket(t.maxPPS, t.now())
	failures := 0
	backoff := minReopenBackoff
	var statsAt time.Time
	t.readKernelStats(src)

	for ctx.Err() == nil {
		if src == nil {
			s, err := t.open()
			if err != nil {
				logf("DNS name capture: reopen failed: %v (retrying in %s)", err, backoff)
				t.sleepCtx(ctx, backoff)
				backoff = min(backoff*2, maxReopenBackoff)
				continue
			}
			src, backoff = s, minReopenBackoff
			t.reopens.Add(1)
			logf("DNS name capture: socket reopened")
		}

		now := t.now()
		if now.Sub(statsAt) >= kernelStatsInterval {
			t.readKernelStats(src)
			statsAt = now
		}
		if ok, wait := bucket.take(now); !ok {
			// Over budget: stop reading. The kernel queues, then drops (and
			// counts) the excess; the agent spends nothing on it.
			t.budgetWaits.Add(1)
			t.sleep(wait)
			continue
		}

		n, meta, err := src.ReadPacket(buf)
		if err != nil {
			bucket.refund() // the budget counts packets, not reads
		}
		if errors.Is(err, errReadTimeout) {
			continue
		}
		if err != nil {
			if failures++; failures >= maxConsecutiveReadErrors {
				logf("DNS name capture: %d consecutive read errors (last: %v); reopening the socket in %s", failures, err, backoff)
				_ = src.Close()
				src, failures = nil, 0
				t.sleepCtx(ctx, backoff)
				backoff = min(backoff*2, maxReopenBackoff)
			}
			continue
		}
		failures = 0
		n = min(n, len(buf))
		if err := t.safeHandle(meta, buf[:n]); err != nil {
			// A parser bug, not a transient fault: reopening would hit the
			// same packets again. Stop capture; keep what was learned.
			logf("DNS name capture stopped: %v; names learned so far are kept", err)
			return
		}
	}
}

func (t *Tracker) readKernelStats(src packetSource) {
	ks, ok := src.(kernelStats)
	if !ok {
		return
	}
	if drops, err := ks.KernelDrops(); err == nil {
		t.kernelDrops.Add(drops)
	}
	t.recvBuffer.Store(int64(ks.RecvBuffer()))
}

// safeHandle runs HandlePacket, converting a panic into an error. Parsing is
// fuzzed and should never panic, but this code handles hostile network input
// inside an agent that must keep running: if a bug slips through, losing
// hostname capture is acceptable and losing the agent is not.
func (t *Tracker) safeHandle(meta PacketMeta, b []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic while parsing a captured packet: %v", r)
		}
	}()
	t.HandlePacket(meta, b)
	return nil
}
