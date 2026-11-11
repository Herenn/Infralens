package middleware

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Login throttling.
//
// Three layers of token buckets, all of which must have a token:
//
//   - Per client: a burst of 5 attempts, then one more every 20 seconds.
//     Each client has its own bucket, so one client guessing never locks out
//     another - provided "client" means the browser rather than the reverse
//     proxy in front of the backend, which is why clients are identified with
//     clientKey below.
//   - Per directly connected peer: what one TCP peer can spend across every
//     client identity it presents, a fraction (half) of the global cap. With
//     the default TRUSTED_PROXIES any loopback/private peer is believed to be
//     a proxy and the client is read from its X-Forwarded-For, so a peer that
//     is not really a proxy (any pod in the cluster, any container on the
//     compose network, an internet client whose source a port mapping
//     rewrote to a private address) can mint a fresh client identity per
//     request. This bucket is what stops it: whatever it forges, it spends
//     from its own peer budget and cannot drain the global cap for everyone.
//     A real proxy is also one peer, so all failed logins behind one proxy
//     share this budget (60 at once, then one a second) - far above what
//     legitimate typos produce.
//   - Global: a safety cap (about 7200 failed attempts an hour across all
//     clients) bounding the total guess rate of a distributed attack.
//
// A client that has logged in successfully before (from the same identity)
// skips the peer and global buckets and keeps only its own: so neither one
// peer forging identities nor a distributed attack can lock out an admin who
// has signed in from that address before. Someone signing in for the first
// time from a new address during a distributed attack can still be refused
// by the global cap - setting TRUSTED_PROXIES to the real proxy addresses is
// the hardening step (forged forwarding headers are then ignored outright).
const (
	loginClientBurst  = 5
	loginClientRefill = 20 * time.Second // one more attempt per client every 20s
	loginGlobalBurst  = 120
	loginGlobalRefill = 500 * time.Millisecond
	loginPeerBurst    = loginGlobalBurst / 2
	loginPeerRefill   = 2 * loginGlobalRefill // half the global refill rate

	// loginMaxClients bounds the per-client and per-peer maps. Past it, idle
	// (fully refilled) entries are swept; if that frees nothing, new clients
	// are limited by the global cap alone rather than growing memory without
	// bound.
	loginMaxClients = 10000

	// Clients that have logged in successfully are remembered this long, at
	// most loginMaxTrusted of them.
	loginTrustedTTL = 30 * 24 * time.Hour
	loginMaxTrusted = 1000
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// refill tops the bucket up for the time elapsed since it was last touched.
func (b *tokenBucket) refill(now time.Time, burst float64, every time.Duration) {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = math.Min(burst, b.tokens+float64(elapsed)/float64(every))
	}
	b.last = now
}

// wait is how long until the bucket holds one whole token.
func (b *tokenBucket) wait(every time.Duration) time.Duration {
	if b.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - b.tokens) * float64(every))
}

type loginLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	global  tokenBucket
	clients map[string]*tokenBucket
	peers   map[string]*tokenBucket
	trusted map[string]time.Time // client keys with a successful login -> when
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{
		now:     now,
		global:  tokenBucket{tokens: loginGlobalBurst, last: now()},
		clients: make(map[string]*tokenBucket),
		peers:   make(map[string]*tokenBucket),
		trusted: make(map[string]time.Time),
	}
}

// take reserves one login attempt for client, arriving from the directly
// connected peer. A token is spent up front, not after a failure, so a burst
// of concurrent requests cannot all pass the check before any of them is
// counted. Callers refund it on success. When the attempt is refused,
// retryAfter says how long to wait.
func (l *loginLimiter) take(client, peer string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.global.refill(now, loginGlobalBurst, loginGlobalRefill)
	b := bucket(l.clients, client, now, loginClientBurst, loginClientRefill)
	if b != nil {
		b.refill(now, loginClientBurst, loginClientRefill)
		if b.tokens < 1 {
			return false, b.wait(loginClientRefill)
		}
	}
	if at, ok := l.trusted[client]; ok && now.Sub(at) < loginTrustedTTL {
		// Known good: its own bucket only (see the top of this file).
		if b != nil {
			b.tokens--
		}
		return true, 0
	}
	p := bucket(l.peers, peer, now, loginPeerBurst, loginPeerRefill)
	if p != nil {
		p.refill(now, loginPeerBurst, loginPeerRefill)
		if p.tokens < 1 {
			return false, p.wait(loginPeerRefill)
		}
	}
	if l.global.tokens < 1 {
		return false, l.global.wait(loginGlobalRefill)
	}
	l.global.tokens--
	if b != nil {
		b.tokens--
	}
	if p != nil {
		p.tokens--
	}
	return true, 0
}

// refund returns the token spent by a successful login, so a legitimate user
// signing in repeatedly never locks themselves out, and remembers the client
// as known good.
func (l *loginLimiter) refund(client, peer string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.global.tokens = math.Min(loginGlobalBurst, l.global.tokens+1)
	if b, ok := l.clients[client]; ok {
		b.tokens = math.Min(loginClientBurst, b.tokens+1)
	}
	if p, ok := l.peers[peer]; ok {
		p.tokens = math.Min(loginPeerBurst, p.tokens+1)
	}
	if _, ok := l.trusted[client]; !ok && len(l.trusted) >= loginMaxTrusted {
		oldestKey, oldest := "", now
		for k, at := range l.trusted {
			if now.Sub(at) >= loginTrustedTTL {
				delete(l.trusted, k)
				continue
			}
			if at.Before(oldest) {
				oldestKey, oldest = k, at
			}
		}
		if len(l.trusted) >= loginMaxTrusted {
			delete(l.trusted, oldestKey)
		}
	}
	l.trusted[client] = now
}

// bucket returns key's bucket in m, creating it if there is room. nil means
// the map is full of active entries and this key is governed by the other
// buckets only.
func bucket(m map[string]*tokenBucket, key string, now time.Time, burst float64, every time.Duration) *tokenBucket {
	if key == "" {
		return nil
	}
	if b, ok := m[key]; ok {
		return b
	}
	if len(m) >= loginMaxClients {
		for k, b := range m {
			b.refill(now, burst, every)
			if b.tokens >= burst {
				delete(m, k)
			}
		}
		if len(m) >= loginMaxClients {
			return nil
		}
	}
	b := &tokenBucket{tokens: burst, last: now}
	m[key] = b
	return b
}

// client returns client's bucket (see bucket); kept for tests.
func (l *loginLimiter) client(key string, now time.Time) *tokenBucket {
	return bucket(l.clients, key, now, loginClientBurst, loginClientRefill)
}

// Client identification behind reverse proxies (TRUSTED_PROXIES).
//
// Every bundled deployment puts a proxy in front of the backend (the
// frontend's nginx, an ingress controller), so the connection's address is
// the proxy's and is shared by every browser. The client's real address is in
// X-Forwarded-For - but anyone can send that header, so it is only read when
// the connection comes from a proxy we trust.
//
//   - TRUSTED_PROXIES unset (default): a connection from a loopback or
//     private address (127/8, ::1, 10/8, 172.16/12, 192.168/16, fc00::/7) is
//     treated as a proxy, and the client is the rightmost X-Forwarded-For
//     entry - the one that proxy appended itself - or X-Real-IP. Exactly one
//     hop: entries further left came from the client and are never used.
//     The resulting identity is scoped to the reporting proxy (client@peer),
//     so a client that connects directly from a private address and forges
//     the header can mint identities for itself, but never lands in the
//     bucket of a client behind the real proxy, and everything it mints
//     spends from its own peer budget (see the top of this file) - so it
//     cannot drain the global cap either. TRUSTED_PROXIES listing only the
//     real proxies removes the forging altogether.
//   - TRUSTED_PROXIES=<CIDRs or IPs, comma-separated>: only those addresses
//     are proxies, and the client is the rightmost X-Forwarded-For entry
//     that is not one of them (multi-hop chains such as LB -> ingress ->
//     nginx). List every proxy hop; nothing else.
//   - TRUSTED_PROXIES=none: never read forwarding headers.
type proxyConfig struct {
	trusted  []*net.IPNet
	explicit bool // TRUSTED_PROXIES was set: walk multiple hops
}

// defaultTrustedProxies covers where the bundled proxies connect from:
// loopback and the private ranges Docker networks and Kubernetes pod
// networks use.
var defaultTrustedProxies = []string{
	"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
}

// parseTrustedProxies parses TRUSTED_PROXIES: "" for the default, "none",
// or a comma-separated list of CIDRs and/or bare IPs.
func parseTrustedProxies(raw string) (*proxyConfig, error) {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "":
		pc := &proxyConfig{}
		for _, c := range defaultTrustedProxies {
			_, n, _ := net.ParseCIDR(c)
			pc.trusted = append(pc.trusted, n)
		}
		return pc, nil
	case "none":
		return &proxyConfig{explicit: true}, nil
	}

	pc := &proxyConfig{explicit: true}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP address or CIDR", part)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			pc.trusted = append(pc.trusted, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP address or CIDR", part)
		}
		pc.trusted = append(pc.trusted, n)
	}
	return pc, nil
}

func (pc *proxyConfig) isTrusted(ip net.IP) bool {
	for _, n := range pc.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientKey identifies the client of r for login throttling.
func (pc *proxyConfig) clientKey(r *http.Request) string {
	peerHost := hostOf(r.RemoteAddr)
	peer := net.ParseIP(peerHost)
	if peer == nil || !pc.isTrusted(peer) {
		return addrKey(peerHost)
	}

	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}

	client := ""
	if pc.explicit {
		for i := len(hops) - 1; i >= 0; i-- {
			ip := net.ParseIP(hostOf(hops[i]))
			if ip == nil || !pc.isTrusted(ip) || i == 0 {
				client = hops[i]
				break
			}
		}
	} else if len(hops) > 0 {
		client = hops[len(hops)-1]
	}
	if client == "" {
		client = strings.TrimSpace(r.Header.Get("X-Real-IP"))
	}
	if client == "" {
		return addrKey(peerHost)
	}
	if pc.explicit {
		return addrKey(hostOf(client))
	}
	return addrKey(hostOf(client)) + "@" + peer.String()
}

// peerKey identifies the directly connected peer of r (the TCP source), as
// a bucket key.
func peerKey(r *http.Request) string {
	return addrKey(hostOf(r.RemoteAddr))
}

// hostOf strips an optional port (and IPv6 brackets) from an address.
func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

// addrKey normalises an address for use as a bucket key, grouping IPv6 by
// /64 since a single host routinely controls a whole /64 and could otherwise
// rotate through it.
func addrKey(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return ip.String()
}
