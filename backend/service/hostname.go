package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// DNS names for destinations
//
// Agents report the DNS name a source resolved a destination's address from
// (dst_hostname, matched from the DNS query and answer that workload sent
// and received). Names are stored per connection, with when each was last
// observed, because that is what was observed: one address can serve many
// names (CDNs, shared load balancers) and each caller asked for its own. A
// service's names are derived on read from its incoming edges
// (AnnotateHostnames), so service IDs - and therefore history, drift
// baselines and every existing link - are unchanged: an external node is
// still keyed "52.1.2.3:443" and merely labelled "api.stripe.com".
//
// Names are display labels, not verified facts: whoever answers a workload's
// DNS decides what it is told, and so what it is labelled with. They are
// ranked by recency and expire (storage.HostnameMaxAge), so a wrong or stale
// name loses the label as soon as the right one is observed again, and
// disappears if it is not.

const (
	maxHostnameLen  = 253
	maxHostnameLbl  = 63
	hostnameRecheck = time.Minute
)

// normalizeHostname validates and canonicalizes an agent-reported DNS name:
// ASCII lowercase, no trailing dot, letters/digits/'-'/'_' labels of 1-63
// bytes, at most 253 bytes in all, and a final label that is not all digits
// (which is what rejects IP literals like "52.1.2.3" posing as a name).
// Anything else is rejected rather than escaped: the value is untrusted agent
// input that ends up in the UI, Mermaid/DOT exports and webhook text, and
// restricting it to the hostname alphabet means none of them has to escape
// it. (Mirrors agent/dnscache.NormalizeName - a fuzz test keeps them equal;
// the backend deliberately does not import agent code outside tests.)
func normalizeHostname(s string) (string, bool) {
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	if len(s) == 0 || len(s) > maxHostnameLen {
		return "", false
	}
	out := make([]byte, len(s))
	label, numeric := 0, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c == '-', c == '_':
			numeric = false
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
			numeric = false
		case c == '.':
			if label == 0 {
				return "", false
			}
			label, numeric = 0, true
			out[i] = c
			continue
		default:
			return "", false
		}
		if label++; label > maxHostnameLbl {
			return "", false
		}
		out[i] = c
	}
	if label == 0 || numeric {
		return "", false
	}
	return string(out), true
}

// isAddressKeyedID reports whether a service ID is built from an IP address
// ("IP", "IP:port", "IP/process") rather than a resolved Kubernetes name.
// Only those get DNS names as a label: a Kubernetes-resolved service already
// has a better one.
func isAddressKeyedID(id string) bool {
	return leadingIP(id) != nil
}

// serviceHostnames ranks the DNS names a service was reached by, from the
// connections targeting it: the most recently observed first, then names
// used by more callers, then alphabetical so the order is stable. Names not
// observed within storage.HostnameMaxAge of ref are left out (a zero ref
// keeps everything).
func serviceHostnames(id string, conns []storage.Connection, ref time.Time) []string {
	type score struct {
		edges  int
		latest time.Time
	}
	cutoff := ref.Add(-storage.HostnameMaxAge)
	scores := make(map[string]*score)
	for _, c := range conns {
		if c.TargetID != id {
			continue
		}
		for _, h := range c.Hostnames {
			if !ref.IsZero() && !h.LastSeen.After(cutoff) {
				continue
			}
			s, ok := scores[h.Name]
			if !ok {
				s = &score{}
				scores[h.Name] = s
			}
			s.edges++
			if h.LastSeen.After(s.latest) {
				s.latest = h.LastSeen
			}
		}
	}
	if len(scores) == 0 {
		return nil
	}
	names := make([]string, 0, len(scores))
	for h := range scores {
		names = append(names, h)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := scores[names[i]], scores[names[j]]
		if !a.latest.Equal(b.latest) {
			return a.latest.After(b.latest)
		}
		if a.edges != b.edges {
			return a.edges > b.edges
		}
		return names[i] < names[j]
	})
	if len(names) > storage.MaxHostnames {
		names = names[:storage.MaxHostnames]
	}
	return names
}

// AnnotateHostnames fills Service.Hostnames, in place, for every
// address-keyed service from the connections targeting it, as of now.
func AnnotateHostnames(services []storage.Service, conns []storage.Connection) {
	annotateHostnamesAt(services, conns, time.Now())
}

// annotateHostnamesAt is AnnotateHostnames with expiry measured from ref
// (the instant a historical topology is reconstructed for).
func annotateHostnamesAt(services []storage.Service, conns []storage.Connection, ref time.Time) {
	if len(conns) == 0 {
		return
	}
	byTarget := make(map[string][]storage.Connection)
	for _, c := range conns {
		if len(c.Hostnames) > 0 {
			byTarget[c.TargetID] = append(byTarget[c.TargetID], c)
		}
	}
	if len(byTarget) == 0 {
		return
	}
	for i := range services {
		if !isAddressKeyedID(services[i].ID) {
			continue
		}
		if in := byTarget[services[i].ID]; len(in) > 0 {
			services[i].Hostnames = serviceHostnames(services[i].ID, in, ref)
		}
	}
}

// ServiceHostname is the one DNS name InfraLens labels a service with, or ""
// when it has none. It is the single rule behind every human-facing name for
// an external endpoint - the topology API's "hostname", the Changes feed and
// webhook (DriftDisplayName, edgeHostname), Idle-spend evidence, MCP output
// and the Mermaid/DOT exports - so they all say "api.stripe.com" for the
// same address. Hostnames is only ever filled for address-keyed services
// (see AnnotateHostnames), most relevant first, so Kubernetes-resolved
// services keep their own names.
func ServiceHostname(svc *storage.Service) string {
	if svc == nil || len(svc.Hostnames) == 0 || !isAddressKeyedID(svc.ID) {
		return ""
	}
	return svc.Hostnames[0]
}

// edgeHostname is ServiceHostname for the target of one connection (or
// history interval): the name its source most recently resolved the target
// from, when the target is address-keyed.
func edgeHostname(targetID string, hs []storage.Hostname) string {
	if !isAddressKeyedID(targetID) {
		return ""
	}
	return primaryHostname(hs)
}

// primaryHostname is the name to show when only one fits: the most recently
// observed. "" when there is none.
func primaryHostname(hs []storage.Hostname) string {
	best := -1
	for i := range hs {
		if best < 0 || hs[i].LastSeen.After(hs[best].LastSeen) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return hs[best].Name
}

// annotateTopology is AnnotateHostnames over a whole topology. Nil-safe.
func annotateTopology(t *storage.Topology) {
	if t != nil {
		AnnotateHostnames(t.Services, t.Connections)
	}
}

// annotateTopologyAt annotates a topology reconstructed for instant at.
func annotateTopologyAt(t *storage.Topology, at time.Time) {
	if t != nil {
		annotateHostnamesAt(t.Services, t.Connections, at)
	}
}

// annotateService fills one service's Hostnames from its incoming edges.
// Only address-keyed services need the extra (indexed) query.
func (ts *TopologyService) annotateService(ctx context.Context, svc *storage.Service) {
	if svc == nil || !isAddressKeyedID(svc.ID) {
		return
	}
	conns, err := ts.store.Connections().List(ctx, storage.ConnectionFilter{TargetID: svc.ID})
	if err != nil {
		return // names are decoration; never fail the read for them
	}
	svc.Hostnames = serviceHostnames(svc.ID, conns, time.Now())
}

// connLocks serializes read-modify-write of one connection's names across
// goroutines (lock striping: a fixed set of mutexes chosen by ID hash). The
// event path (AddConnection) and the throughput path (AddConnectionHostnames)
// both merge into the stored list; without this, concurrent reports for one
// edge from several agents lost names (review PoC: 95 of 100 rounds with six
// concurrent names). Per process: a multi-replica backend would also need
// the merge in SQL - the shipped deployments run one replica.
type connLocks [64]sync.Mutex

func (l *connLocks) lock(id string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	m := &l[h.Sum32()%uint32(len(l))]
	m.Lock()
	return m.Unlock
}

// hostnameMemo remembers recently confirmed (connection, names) pairs so the
// throughput path - one report per socket per second - does not re-read a
// connection just to find its names already stored.
type hostnameMemo struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	swept time.Time
}

// claim reports whether key needs checking at now, recording that it has
// been. Entries are swept once per interval so the map tracks only what is
// currently reporting.
func (m *hostnameMemo) claim(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = make(map[string]time.Time)
	}
	if last, ok := m.seen[key]; ok && now.Sub(last) < hostnameRecheck {
		return false
	}
	m.seen[key] = now
	if now.Sub(m.swept) > hostnameRecheck {
		for k, t := range m.seen {
			if now.Sub(t) >= hostnameRecheck {
				delete(m.seen, k)
			}
		}
		m.swept = now
	}
	return true
}

// forget drops a key so the next report re-checks it, used when a check
// could not complete.
func (m *hostnameMemo) forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.seen, key)
}

// publishTargetUpdated tells live clients that a service's derived names
// changed. Its names come from its incoming connections, so storing a name
// on a connection changes the service as clients see it, but nothing about
// the service row itself - without this, a long-lived connection whose name
// arrived after the connect event kept showing the IP until a reload.
func (ts *TopologyService) publishTargetUpdated(ctx context.Context, targetID string) {
	svc, err := ts.store.Services().Get(ctx, targetID)
	if err != nil || svc == nil {
		return
	}
	ts.eventBus.PublishServiceUpdated(ServiceEvent{
		ServiceID: svc.ID,
		Name:      svc.Name,
		Node:      svc.Node,
		Type:      svc.Type,
		Healthy:   svc.Healthy,
	})
}

// AddConnectionHostnames merges names observed now into an existing
// connection's DNS names. A connection that does not exist is not an error -
// the throughput path tries candidate IDs that may not apply. When the stored
// names change, the history interval and live clients (connection and target
// service) are updated too.
func (ts *TopologyService) AddConnectionHostnames(ctx context.Context, connID string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	key := connID + "\x00" + strings.Join(names, ",")
	now := time.Now()
	if !ts.hostnames.claim(key, now) {
		return nil
	}

	unlock := ts.connLocks.lock(connID)
	conn, err := ts.store.Connections().Get(ctx, connID)
	if err != nil || conn == nil {
		unlock()
		ts.hostnames.forget(key)
		if err != nil {
			return fmt.Errorf("getting connection for hostnames: %w", err)
		}
		return nil
	}
	merged := storage.MergeHostnames(storage.ObservedHostnames(now, names...), conn.Hostnames, now)
	if storage.EqualHostnames(merged, conn.Hostnames) {
		unlock()
		return nil
	}
	if err := ts.store.Connections().SetHostnames(ctx, connID, merged); err != nil {
		unlock()
		ts.hostnames.forget(key)
		return fmt.Errorf("storing connection hostnames: %w", err)
	}
	conn.Hostnames = merged
	// A throughput report is a sighting, so recording one now is accurate,
	// and it is what puts the name on the history interval promptly rather
	// than at the next throttled refresh.
	ts.recordConnectionHistory(ctx, conn, now)
	unlock()

	ts.eventBus.PublishConnectionUpdated(ConnectionEvent{
		ConnectionID:  conn.ID,
		SourceID:      conn.SourceID,
		TargetID:      conn.TargetID,
		Port:          conn.Port,
		BytesSentRate: conn.BytesSentRate,
		BytesRecvRate: conn.BytesRecvRate,
	})
	ts.publishTargetUpdated(ctx, conn.TargetID)
	return nil
}
