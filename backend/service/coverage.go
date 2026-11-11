package service

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Observation coverage: when was InfraLens actually watching a node?
//
// History records intervals during which an entity was continuously
// observed. When the backend or an agent is down nothing is recorded, so in
// raw data an outage is indistinguishable from silence: "last called 8 days
// ago" might mean "we stopped listening 8 days ago", and "not seen for 24h"
// might mean "the agent that sees it was down for 24h". Idle spend measures
// quiet periods, and the drift detector measures how long an edge has been
// gone, against this coverage rather than against the clock. Both use the
// one implementation here.
//
// Evidence that a node was being watched must be something only that node's
// own agent can produce:
//
//   - Agent heartbeats (storage.NodeInterval): every agent reports host
//     metrics every few seconds, recorded per real agent node. Only that
//     agent extends them.
//   - Its own local activity: service and edge intervals - but an interval
//     is extended by the agent that observed the socket, which is NOT always
//     the agent of the node it touches. The client's agent reports its
//     outbound connect as "client/proc -> server-IP:port", the server's agent
//     reports the accept as "client-IP -> server/proc" (processor.go),
//     throughput samples come from the local socket's side, and
//     refreshHistoryFromStats refreshes both endpoint services of the edge a
//     sample lands on. So activity counts for a node only through IDs that
//     only its own agent can produce: process-keyed IDs ("IP/proc") are
//     always the reporting agent's own process. "IP:port" and bare-IP IDs are
//     the far end as another agent saw it and never credit their node.
//     Kubernetes-resolved names are ambiguous (both agents report the same
//     ID, so a live client keeps a dead server's edge and service row fresh)
//     and credit no node.
//
// A node's coverage is the union of both: a node whose metrics posting fails
// but whose agent still reports traffic is still observed (history from
// before heartbeats existed has only the second kind). A node with neither
// was not observed. Only an endpoint with no node at all (an external
// address) is measured against the global coverage - the union over all
// nodes, whose gaps are backend or collection-wide outages.
//
// CLUSTER_NAME groups. Grouped agents record every service and edge under
// the group name, so local activity cannot say which member's agent
// produced it, and a group is observed only while EVERY member agent is
// reporting (the intersection of the members' heartbeats): one live agent
// must not keep a dead member's services and edges "observed". Members come
// and go (autoscaling, replaced hosts): a member counts from its first
// heartbeat, and stops counting once it has been silent for the retirement
// threshold below, so a removed node does not leave its group unobserved for
// good.
//
// Retirement. Outage and retirement look the same at first - an agent stops
// reporting. A node (or group member) whose own agent has been silent for
// longer than retireAfter while InfraLens keeps observing elsewhere is
// treated as retired: from then on its coverage follows the global coverage,
// so what was on it is (after the usual delays) reported gone or offered as
// a decommission candidate instead of staying unknown forever. A shorter
// outage still accrues nothing. Callers choose retireAfter (drift:
// driftRetireAfter; idle spend: the stale threshold).
//
// Known limit: a node where genuinely nothing happened and whose agent sent
// no heartbeats (history from before heartbeats) shows a gap. That errs in
// the safe direction - silence is under-counted, so a service is held back
// rather than flagged and an edge is reported gone later rather than
// falsely.

type span struct{ from, to time.Time }

type coverage []span // sorted, non-overlapping

func buildCoverage(spans []span, lo, hi time.Time) coverage {
	clipped := make([]span, 0, len(spans))
	for _, s := range spans {
		if !lo.IsZero() && s.from.Before(lo) {
			s.from = lo
		}
		if !hi.IsZero() && s.to.After(hi) {
			s.to = hi
		}
		if s.to.After(s.from) {
			clipped = append(clipped, s)
		}
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].from.Before(clipped[j].from) })
	var out coverage
	for _, s := range clipped {
		if n := len(out); n > 0 && !s.from.After(out[n-1].to) {
			if s.to.After(out[n-1].to) {
				out[n-1].to = s.to
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// union merges coverages.
func union(cs ...coverage) coverage {
	n := 0
	for _, c := range cs {
		n += len(c)
	}
	all := make([]span, 0, n)
	for _, c := range cs {
		all = append(all, c...)
	}
	return buildCoverage(all, time.Time{}, time.Time{})
}

// minus is c without b.
func (c coverage) minus(b coverage) coverage {
	var out coverage
	j := 0
	for _, s := range c {
		cur := s
		for j < len(b) && !b[j].to.After(cur.from) {
			j++
		}
		k := j
		for k < len(b) && b[k].from.Before(cur.to) {
			if b[k].from.After(cur.from) {
				out = append(out, span{cur.from, b[k].from})
			}
			if b[k].to.After(cur.from) {
				cur.from = b[k].to
			}
			if !cur.from.Before(cur.to) {
				break
			}
			k++
		}
		if cur.from.Before(cur.to) {
			out = append(out, cur)
		}
	}
	return out
}

// from keeps the part of c at or after t.
func (c coverage) from(t time.Time) coverage {
	var out coverage
	for _, s := range c {
		if !s.to.After(t) {
			continue
		}
		if s.from.Before(t) {
			s.from = t
		}
		out = append(out, s)
	}
	return out
}

func (c coverage) end() time.Time {
	if len(c) == 0 {
		return time.Time{}
	}
	return c[len(c)-1].to
}

// covered is how much of [from, to] the coverage includes.
func (c coverage) covered(from, to time.Time) time.Duration {
	i := sort.Search(len(c), func(i int) bool { return c[i].to.After(from) })
	var d time.Duration
	for ; i < len(c) && c[i].from.Before(to); i++ {
		a, b := c[i].from, c[i].to
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		if b.After(a) {
			d += b.Sub(a)
		}
	}
	return d
}

func (c coverage) total() time.Duration {
	var d time.Duration
	for _, s := range c {
		d += s.to.Sub(s.from)
	}
	return d
}

// observationCoverage is the shared coverage model (see the top of this
// file). Give it heartbeats and evidence, then ask observed(nodes, from, to).
// The drift detector keeps one for its lifetime: it replaces the heartbeats
// on every pass and updates evidence by key as intervals are extended.
//
// Evidence is indexed by node as it arrives; per-node merged coverage is
// rebuilt only for nodes whose evidence changed, so a pass over a large
// pre-heartbeat history costs little more than one over a small one.
type observationCoverage struct {
	heartbeatGrace time.Duration // added to a heartbeat interval's end
	evidenceGrace  time.Duration // added to an evidence interval's end
	retireAfter    time.Duration // silence after which a node counts as retired; 0: never

	// heartbeats
	hb      map[string]coverage // real agent node -> merged heartbeats (with grace)
	members map[string][]string // CLUSTER_NAME group -> member agent nodes
	hbAll   coverage

	// evidence
	evidence   map[string]evidenceSpan    // by caller-chosen key
	evByNode   map[string]map[string]span // node -> key -> span
	evMerged   map[string]coverage        // node -> merged evidence (lazily)
	evNodeless map[string]span            // evidence crediting no node, by key
	evAll      coverage                   // all evidence, merged (lazily)
	evAllOK    bool

	global   coverage // hbAll ∪ evAll (lazily)
	globalOK bool
	final    map[string]coverage // per node key, final coverage (lazily)
	seq      int
}

type evidenceSpan struct {
	nodes []string
	s     span
}

func newObservationCoverage(heartbeatGrace, evidenceGrace, retireAfter time.Duration) *observationCoverage {
	return &observationCoverage{
		heartbeatGrace: heartbeatGrace,
		evidenceGrace:  evidenceGrace,
		retireAfter:    retireAfter,
		hb:             map[string]coverage{},
		members:        map[string][]string{},
		evidence:       map[string]evidenceSpan{},
		evByNode:       map[string]map[string]span{},
		evMerged:       map[string]coverage{},
		evNodeless:     map[string]span{},
		final:          map[string]coverage{},
	}
}

// setHeartbeats replaces all heartbeat coverage with the given intervals.
func (c *observationCoverage) setHeartbeats(intervals []storage.NodeInterval) {
	raw := map[string][]span{}
	groupsOf := map[string]map[string]bool{}
	var all []span
	for _, ni := range intervals {
		if ni.Node == "" {
			continue
		}
		s := span{from: ni.FirstSeen, to: ni.LastSeen.Add(c.heartbeatGrace)}
		raw[ni.Node] = append(raw[ni.Node], s)
		all = append(all, s)
		if ni.Group != "" {
			if groupsOf[ni.Group] == nil {
				groupsOf[ni.Group] = map[string]bool{}
			}
			groupsOf[ni.Group][ni.Node] = true
		}
	}
	c.hb = make(map[string]coverage, len(raw))
	for n, ss := range raw {
		c.hb[n] = buildCoverage(ss, time.Time{}, time.Time{})
	}
	c.members = make(map[string][]string, len(groupsOf))
	for g, ms := range groupsOf {
		for m := range ms {
			c.members[g] = append(c.members[g], m)
		}
		sort.Strings(c.members[g])
	}
	c.hbAll = buildCoverage(all, time.Time{}, time.Time{})
	c.globalOK = false
	c.final = map[string]coverage{}
}

// addHeartbeat adds one ungrouped heartbeat interval (tests, idle inputs
// built span by span). Prefer setHeartbeats.
func (c *observationCoverage) addHeartbeat(node string, from, to time.Time) {
	if node == "" {
		return
	}
	s := span{from: from, to: to.Add(c.heartbeatGrace)}
	c.hb[node] = union(c.hb[node], coverage{s})
	c.hbAll = union(c.hbAll, coverage{s})
	c.globalOK = false
	c.final = map[string]coverage{}
}

// addEvidence records a service or edge interval as evidence for the given
// nodes (those whose agent produced it - see edgeReporters). Every interval
// also counts towards the global coverage. A later call with the same key
// replaces the span (an interval extended in place); an empty key always
// adds.
func (c *observationCoverage) addEvidence(key string, nodes []string, from, to time.Time) {
	if key == "" {
		c.seq++
		key = fmt.Sprintf("#%d", c.seq)
	}
	s := span{from: from, to: to.Add(c.evidenceGrace)}
	if old, ok := c.evidence[key]; ok {
		if old.s == s && sameNodes(old.nodes, nodes) {
			return // re-read, unchanged
		}
		c.dropEvidence(key, old)
	}
	c.evidence[key] = evidenceSpan{nodes: nodes, s: s}
	if len(nodes) == 0 {
		c.evNodeless[key] = s
	}
	for _, n := range nodes {
		if c.evByNode[n] == nil {
			c.evByNode[n] = map[string]span{}
		}
		c.evByNode[n][key] = s
		delete(c.evMerged, n)
	}
	c.evAllOK, c.globalOK = false, false
	c.final = map[string]coverage{}
}

func (c *observationCoverage) dropEvidence(key string, e evidenceSpan) {
	delete(c.evidence, key)
	delete(c.evNodeless, key)
	for _, n := range e.nodes {
		if m := c.evByNode[n]; m != nil {
			delete(m, key)
			if len(m) == 0 {
				delete(c.evByNode, n)
			}
		}
		delete(c.evMerged, n)
	}
}

func sameNodes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// forget drops evidence that ended before the cutoff (drift's retention).
// Heartbeats are replaced wholesale by setHeartbeats instead.
func (c *observationCoverage) forget(before time.Time) {
	for k, e := range c.evidence {
		if e.s.to.Before(before) {
			c.dropEvidence(k, e)
			c.evAllOK, c.globalOK = false, false
		}
	}
	c.final = map[string]coverage{}
}

func (c *observationCoverage) evidenceOf(n string) coverage {
	if cv, ok := c.evMerged[n]; ok {
		return cv
	}
	m := c.evByNode[n]
	ss := make([]span, 0, len(m))
	for _, s := range m {
		ss = append(ss, s)
	}
	cv := buildCoverage(ss, time.Time{}, time.Time{})
	c.evMerged[n] = cv
	return cv
}

// globalCoverage is everything observed anywhere.
func (c *observationCoverage) globalCoverage() coverage {
	if c.globalOK {
		return c.global
	}
	if !c.evAllOK {
		parts := make([]coverage, 0, len(c.evByNode)+1)
		for n := range c.evByNode {
			parts = append(parts, c.evidenceOf(n))
		}
		nodeless := make([]span, 0, len(c.evNodeless))
		for _, s := range c.evNodeless {
			nodeless = append(nodeless, s)
		}
		parts = append(parts, buildCoverage(nodeless, time.Time{}, time.Time{}))
		c.evAll, c.evAllOK = union(parts...), true
	}
	c.global, c.globalOK = union(c.hbAll, c.evAll), true
	return c.global
}

// retired extends base with the global coverage from retireAfter after its
// end on: a node silent that long while InfraLens observed elsewhere is
// gone, not down. An empty base (never observed) is left empty.
func (c *observationCoverage) retired(base coverage) coverage {
	if c.retireAfter <= 0 || len(base) == 0 {
		return base
	}
	return union(base, c.globalCoverage().from(base.end().Add(c.retireAfter)))
}

// nodeCoverage is the final coverage of one node key: a real node, or a
// CLUSTER_NAME group.
func (c *observationCoverage) nodeCoverage(n string) coverage {
	if cv, ok := c.final[n]; ok {
		return cv
	}
	var cv coverage
	if ms := c.members[n]; len(ms) > 0 {
		cv = c.groupCoverage(ms)
	} else {
		cv = c.retired(union(c.hb[n], c.evidenceOf(n)))
	}
	c.final[n] = cv
	return cv
}

// groupCoverage: observed while at least one member is active and every
// active member's agent is heartbeating. A member is active from its first
// heartbeat until it has been silent for retireAfter (it then counts as
// retired and drops out). Local activity cannot be attributed to a member
// and is not used. When every member has retired, the group follows the
// global coverage (retired as a whole).
func (c *observationCoverage) groupCoverage(members []string) coverage {
	var active, gaps []coverage
	var last time.Time
	for _, m := range members {
		hb := c.hb[m]
		if len(hb) == 0 {
			continue
		}
		until := hb.end()
		if c.retireAfter > 0 {
			until = until.Add(c.retireAfter)
		} else {
			until = time.Unix(1<<40, 0) // never retires
		}
		window := coverage{{from: hb[0].from, to: until}}
		active = append(active, window)
		gaps = append(gaps, window.minus(hb))
		if hb.end().After(last) {
			last = hb.end()
		}
	}
	cv := union(active...).minus(union(gaps...))
	if c.retireAfter > 0 && !last.IsZero() {
		cv = union(cv, c.globalCoverage().from(last.Add(c.retireAfter)))
	}
	return cv
}

// observed is how much of [from, to] at least one of nodes was being watched.
// No nodes (an endpoint with no node, such as an external address) means the
// global coverage.
func (c *observationCoverage) observed(nodes []string, from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	switch len(nodes) {
	case 0:
		return c.globalCoverage().covered(from, to)
	case 1:
		return c.nodeCoverage(nodes[0]).covered(from, to)
	}
	key := strings.Join(nodes, "\x00")
	cv, ok := c.final[key]
	if !ok {
		parts := make([]coverage, 0, len(nodes))
		for _, n := range nodes {
			parts = append(parts, c.nodeCoverage(n))
		}
		cv = union(parts...)
		c.final[key] = cv
	}
	return cv.covered(from, to)
}

// total is the global observed time in [from, to].
func (c *observationCoverage) total(from, to time.Time) time.Duration {
	return c.observed(nil, from, to)
}

// localEndpointID reports whether a service ID can only have been produced by
// the agent of the node it is on: a process-keyed ID ("IP/proc", see
// makeServiceIDWithProcess). "IP:port" and bare-IP IDs are the far end as
// another agent saw it; resolved (Kubernetes) names may come from either end.
func localEndpointID(id string) bool {
	i := strings.Index(id, "/")
	return i > 0 && net.ParseIP(id[:i]) != nil
}

// edgeReporters returns the nodes whose agent could have recorded an edge:
// the nodes of its local endpoints. Empty when neither end has a known node.
func edgeReporters(sourceID, targetID string, nodeOf func(string) string) []string {
	var out []string
	for _, id := range []string{sourceID, targetID} {
		if !localEndpointID(id) {
			continue
		}
		if n := nodeOf(id); n != "" && (len(out) == 0 || out[0] != n) {
			out = append(out, n)
		}
	}
	return out
}

// buildIdleCoverage is the coverage for one idle-spend report: heartbeats
// from node intervals, local activity from the window's service and edge
// intervals.
func buildIdleCoverage(in idleInputs, nodeOf func(id string) string) *observationCoverage {
	grace := in.maxGap
	if grace <= 0 {
		grace = storage.DefaultHistoryMaxGap
	}
	cov := newObservationCoverage(grace, grace, in.retireAfter)
	lo, hi := in.coverageStart, in.now
	clip := func(from, to time.Time) (time.Time, time.Time, bool) {
		if from.Before(lo) {
			from = lo
		}
		if to.After(hi) {
			to = hi
		}
		return from, to, !to.Before(from)
	}
	beats := make([]storage.NodeInterval, 0, len(in.historyNodes))
	for _, ni := range in.historyNodes {
		if from, to, ok := clip(ni.FirstSeen, ni.LastSeen); ok {
			ni.FirstSeen, ni.LastSeen = from, to
			beats = append(beats, ni)
		}
	}
	cov.setHeartbeats(beats)
	for _, si := range in.historyServices {
		if from, to, ok := clip(si.FirstSeen, si.LastSeen); ok {
			var nodes []string
			if si.Node != "" && localEndpointID(si.ServiceID) {
				nodes = []string{si.Node}
			}
			cov.addEvidence("", nodes, from, to)
		}
	}
	for _, ci := range in.historyEdges {
		if from, to, ok := clip(ci.FirstSeen, ci.LastSeen); ok {
			cov.addEvidence("", edgeReporters(ci.SourceID, ci.TargetID, nodeOf), from, to)
		}
	}
	return cov
}
