package service

import (
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
// Which coverage an edge's absence is measured against (edgeReporters): the
// nodes of its process-keyed endpoints; for an edge with none (both ends
// Kubernetes names, say), the node recorded on the source's service row,
// else on the target's - a service row's node is set by the agent that saw
// the service locally.
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
// produced it and is not used: a group is observed only through its
// members' heartbeats, and before any member has sent one it is simply
// unobserved. Heartbeats are kept per (group, node), so a node's grouped and
// ungrouped reporting, or same-named nodes in two clusters, never mix.
//
// Membership is time-local. A member is "current" from its first heartbeat
// until it has been silent for memberTimeout (1h, at least
// memberTimeoutBeats heartbeat gaps). The group is observed while at least
// one member reports and no current member is silent: one live agent must
// not keep a dead member's services and edges "observed", but a member that
// stops for good (scale-in, a replaced host) holds the group up for at most
// the timeout, not until it is retired. Silences shorter than the timeout
// count as unobserved. A flapping member - one that, at that moment, has
// reported for less than half (flapMinRatio) of the preceding two timeouts
// (flapWindowTimeouts), or of its life so far if that is shorter - is not
// current either, so a crash-looping agent cannot hold the group unobserved
// indefinitely: it does so for at most about an hour after the flapping
// starts, and for at most half the time after that.
//
// A member that stopped for good is no longer required, but what only it
// could see is no longer seen either. Callers that turn silence into
// verdicts list such members (silentAgents) instead of hiding them: Idle
// spend caps that group's candidates at low confidence and leaves them out
// of the total, and the drift status names them.
//
// Retirement. Outage and retirement look the same at first - an agent stops
// reporting. A node whose own agent has been silent for longer than
// retireAfter (a group: every member) while InfraLens keeps observing
// elsewhere is treated as retired: from then on its coverage follows the
// global coverage, so what was on it is (after the usual delays) reported
// gone or offered as a decommission candidate instead of staying unknown
// forever. A shorter outage still accrues nothing. Callers choose
// retireAfter (drift: driftRetireAfter; idle spend: the stale threshold).
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

// Group membership (see the top of this file).
const (
	memberTimeout      = time.Hour
	memberTimeoutBeats = 4   // the timeout is at least this many heartbeat gaps
	flapWindowTimeouts = 2   // a member's reporting ratio is taken over this many timeouts
	flapMinRatio       = 0.5 // below this ratio a member is flapping, not current
	flapStep           = 5 * time.Minute
)

// agentKey identifies an agent: its node within its CLUSTER_NAME group (""
// for an ungrouped agent).
type agentKey struct{ group, node string }

// silentAgent is a grouped or ungrouped agent that stopped reporting.
type silentAgent struct {
	Group, Node string
	LastSeen    time.Time
}

// observationCoverage is the shared coverage model (see the top of this
// file). Give it heartbeats and evidence, then ask observed(nodes, from, to).
// The drift detector keeps one for its lifetime: it replaces the heartbeats
// on every pass and updates evidence by key as intervals are extended.
//
// Memory scales with nodes and distinct edges, not with intervals: only
// evidence that can still be extended (its interval ended within
// openWindow) is kept by key; everything older is folded into per-node
// merged coverage as soon as it is seen or closes.
type observationCoverage struct {
	heartbeatGrace time.Duration // added to a heartbeat interval's end
	evidenceGrace  time.Duration // added to an evidence interval's end
	retireAfter    time.Duration // silence after which a node counts as retired; 0: never
	now            func() time.Time

	// heartbeats
	hb      map[agentKey]coverage // merged heartbeats (with grace)
	members map[string][]agentKey // CLUSTER_NAME group -> members with heartbeats
	groups  map[string]bool       // known CLUSTER_NAME groups, with or without heartbeats
	hbAll   coverage

	// evidence
	open      map[string]evidenceSpan    // extendable evidence, by caller-chosen key
	openBy    map[string]map[string]span // node ("" for none) -> key -> span, for open
	closed    map[string]coverage        // node ("" for none) -> merged closed evidence
	pending   map[string][]span          // node ("" for none) -> closed, not merged yet
	evMerged  map[string]coverage        // node -> all its evidence, merged (lazily)
	evAll     coverage                   // all evidence, merged (lazily)
	evAllOK   bool
	sinceSwep int

	global   coverage // hbAll ∪ evAll (lazily)
	globalOK bool
	final    map[string]coverage // per node key, final coverage (lazily)
}

type evidenceSpan struct {
	nodes []string
	s     span
	to    time.Time // raw end, without grace
}

func newObservationCoverage(heartbeatGrace, evidenceGrace, retireAfter time.Duration) *observationCoverage {
	return &observationCoverage{
		heartbeatGrace: heartbeatGrace,
		evidenceGrace:  evidenceGrace,
		retireAfter:    retireAfter,
		now:            time.Now,
		hb:             map[agentKey]coverage{},
		members:        map[string][]agentKey{},
		groups:         map[string]bool{},
		open:           map[string]evidenceSpan{},
		openBy:         map[string]map[string]span{},
		closed:         map[string]coverage{},
		pending:        map[string][]span{},
		evMerged:       map[string]coverage{},
		final:          map[string]coverage{},
	}
}

// memberTimeout is how long a group member may be silent before it stops
// being required (see the top of this file).
func (c *observationCoverage) memberTimeout() time.Duration {
	t := memberTimeout
	if f := memberTimeoutBeats * c.heartbeatGrace; f > t {
		t = f
	}
	return t
}

// openWindow: evidence whose interval ended longer ago than this can no
// longer be extended (history opens a new interval after maxGap - the
// heartbeat grace - of silence), with as much again for late writes.
func (c *observationCoverage) openWindow() time.Duration {
	w := 2 * c.heartbeatGrace
	if w < 2*storage.DefaultHistoryMaxGap {
		w = 2 * storage.DefaultHistoryMaxGap
	}
	return w
}

func (c *observationCoverage) invalidate() {
	c.globalOK = false
	c.final = map[string]coverage{}
}

// setHeartbeats replaces all heartbeat coverage with the given intervals.
// Their groups are known groups from then on (see setGroups).
func (c *observationCoverage) setHeartbeats(intervals []storage.NodeInterval) {
	raw := map[agentKey][]span{}
	var all []span
	for _, ni := range intervals {
		if ni.Node == "" {
			continue
		}
		s := span{from: ni.FirstSeen, to: ni.LastSeen.Add(c.heartbeatGrace)}
		k := agentKey{ni.Group, ni.Node}
		raw[k] = append(raw[k], s)
		all = append(all, s)
	}
	c.hb = make(map[agentKey]coverage, len(raw))
	c.members = map[string][]agentKey{}
	for k, ss := range raw {
		c.hb[k] = buildCoverage(ss, time.Time{}, time.Time{})
		if k.group != "" {
			c.members[k.group] = append(c.members[k.group], k)
			c.groups[k.group] = true
		}
	}
	for g := range c.members {
		sort.Slice(c.members[g], func(i, j int) bool { return c.members[g][i].node < c.members[g][j].node })
	}
	c.hbAll = buildCoverage(all, time.Time{}, time.Time{})
	c.invalidate()
}

// setGroups adds CLUSTER_NAME groups known from elsewhere (agents reporting
// now). A known group with no member heartbeats is unobserved; its name is
// never treated as an ungrouped node with local evidence.
func (c *observationCoverage) setGroups(groups []string) {
	for _, g := range groups {
		if g != "" && !c.groups[g] {
			c.groups[g] = true
			c.invalidate()
		}
	}
}

// addEvidence records a service or edge interval as evidence for the given
// nodes (those whose agent produced it - see edgeEvidenceNodes). Every
// interval also counts towards the global coverage. A later call with the
// same key replaces the span while the interval can still be extended; an
// empty key, or an interval that ended more than openWindow ago, is folded
// in for good.
func (c *observationCoverage) addEvidence(key string, nodes []string, from, to time.Time) {
	s := span{from: from, to: to.Add(c.evidenceGrace)}
	if !s.to.After(s.from) {
		return
	}
	if old, ok := c.open[key]; ok && key != "" {
		if old.s == s && sameNodes(old.nodes, nodes) {
			return // re-read, unchanged
		}
		c.dropOpen(key, old)
	}
	if key == "" || to.Before(c.now().Add(-c.openWindow())) {
		c.fold(nodes, s)
	} else {
		c.open[key] = evidenceSpan{nodes: nodes, s: s, to: to}
		for _, n := range nodeKeys(nodes) {
			if c.openBy[n] == nil {
				c.openBy[n] = map[string]span{}
			}
			c.openBy[n][key] = s
			delete(c.evMerged, n)
		}
	}
	c.evAllOK = false
	c.invalidate()
	if c.sinceSwep++; c.sinceSwep >= 4096 && c.sinceSwep >= len(c.open) {
		c.closeOpen()
	}
}

// nodeKeys is the evidence index keys of nodes: "" for evidence crediting no
// node (it still counts towards the global coverage).
func nodeKeys(nodes []string) []string {
	if len(nodes) == 0 {
		return []string{""}
	}
	return nodes
}

func (c *observationCoverage) fold(nodes []string, s span) {
	for _, n := range nodeKeys(nodes) {
		p := append(c.pending[n], s)
		if len(p) > 256 && len(p) > 2*len(c.closed[n]) {
			c.closed[n] = union(c.closed[n], coverage(p))
			p = nil
		}
		c.pending[n] = p
		delete(c.evMerged, n)
	}
}

func (c *observationCoverage) dropOpen(key string, e evidenceSpan) {
	delete(c.open, key)
	for _, n := range nodeKeys(e.nodes) {
		if m := c.openBy[n]; m != nil {
			delete(m, key)
			if len(m) == 0 {
				delete(c.openBy, n)
			}
		}
		delete(c.evMerged, n)
	}
}

// closeOpen folds evidence that can no longer be extended.
func (c *observationCoverage) closeOpen() {
	c.sinceSwep = 0
	cutoff := c.now().Add(-c.openWindow())
	for k, e := range c.open {
		if e.to.Before(cutoff) {
			c.dropOpen(k, e)
			c.fold(e.nodes, e.s)
		}
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

// forget drops evidence before the cutoff (drift's retention) and folds
// evidence that can no longer be extended. Heartbeats are replaced
// wholesale by setHeartbeats instead.
func (c *observationCoverage) forget(before time.Time) {
	c.closeOpen()
	for n := range c.pending {
		c.mergeClosed(n)
	}
	for n, cv := range c.closed {
		if len(cv) > 0 && cv[0].to.Before(before) {
			if cv = cv.from(before); len(cv) == 0 {
				delete(c.closed, n)
			} else {
				c.closed[n] = cv
			}
			delete(c.evMerged, n)
			c.evAllOK = false
		}
	}
	for k, e := range c.open {
		if e.s.to.Before(before) {
			c.dropOpen(k, e)
			c.evAllOK = false
		}
	}
	c.invalidate()
}

func (c *observationCoverage) mergeClosed(n string) {
	if p := c.pending[n]; len(p) > 0 {
		c.closed[n] = union(c.closed[n], coverage(p))
	}
	delete(c.pending, n)
}

// evidenceOf is all evidence of node n ("": crediting no node), merged.
func (c *observationCoverage) evidenceOf(n string) coverage {
	if cv, ok := c.evMerged[n]; ok {
		return cv
	}
	c.mergeClosed(n)
	m := c.openBy[n]
	ss := make([]span, 0, len(m)+len(c.closed[n]))
	ss = append(ss, c.closed[n]...)
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
		seen := map[string]bool{}
		var parts []coverage
		for n := range c.closed {
			seen[n] = true
		}
		for n := range c.pending {
			seen[n] = true
		}
		for n := range c.openBy {
			seen[n] = true
		}
		for n := range seen {
			parts = append(parts, c.evidenceOf(n))
		}
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

// isGroup reports whether a node key is a CLUSTER_NAME group.
func (c *observationCoverage) isGroup(n string) bool {
	return c.groups[n]
}

// nodeCoverage is the final coverage of one node key: a real node, or a
// CLUSTER_NAME group.
func (c *observationCoverage) nodeCoverage(n string) coverage {
	if cv, ok := c.final[n]; ok {
		return cv
	}
	var cv coverage
	if c.isGroup(n) {
		cv = c.groupCoverage(c.members[n])
	} else {
		cv = c.retired(union(c.hb[agentKey{"", n}], c.evidenceOf(n)))
	}
	c.final[n] = cv
	return cv
}

// memberGaps is when a member was current but not reporting: silences up to
// the member timeout after each heartbeat interval, minus the times it was
// flapping (see the top of this file).
func (c *observationCoverage) memberGaps(hb coverage) coverage {
	if len(hb) == 0 {
		return nil
	}
	timeout := c.memberTimeout()
	window := flapWindowTimeouts * timeout
	req := make([]span, len(hb))
	for i, s := range hb {
		req[i] = span{s.from, s.to.Add(timeout)}
	}
	first := hb[0].from
	var out coverage
	for _, g := range buildCoverage(req, time.Time{}, time.Time{}).minus(hb) {
		// Decide per step, at its start: the ratio only falls further into
		// the silence, so this errs towards "current" (unobserved).
		for p := g.from; p.Before(g.to); p = p.Add(flapStep) {
			q := p.Add(flapStep)
			if q.After(g.to) {
				q = g.to
			}
			lo := p.Add(-window)
			if lo.Before(first) {
				lo = first
			}
			if life := p.Sub(lo); life > 0 && float64(hb.covered(lo, p)) < flapMinRatio*float64(life) {
				continue // flapping: not current
			}
			if n := len(out); n > 0 && out[n-1].to.Equal(p) {
				out[n-1].to = q
			} else {
				out = append(out, span{p, q})
			}
		}
	}
	return out
}

// groupCoverage: observed while at least one member reports and no current
// member is silent (see the top of this file). When every member has been
// silent for retireAfter, the group follows the global coverage (retired as
// a whole). No member heartbeats at all: unobserved.
func (c *observationCoverage) groupCoverage(members []agentKey) coverage {
	var hbs, gaps []coverage
	var last time.Time
	for _, m := range members {
		hb := c.hb[m]
		if len(hb) == 0 {
			continue
		}
		hbs = append(hbs, hb)
		gaps = append(gaps, c.memberGaps(hb))
		if hb.end().After(last) {
			last = hb.end()
		}
	}
	cv := union(hbs...).minus(union(gaps...))
	if c.retireAfter > 0 && !last.IsZero() {
		cv = union(cv, c.globalCoverage().from(last.Add(c.retireAfter)))
	}
	return cv
}

// silentAgents lists agents that stopped reporting: their last heartbeat
// ended more than the member timeout before now and at or after since.
// groupedOnly restricts it to CLUSTER_NAME members. Newest silence first.
func (c *observationCoverage) silentAgents(since, now time.Time, groupedOnly bool) []silentAgent {
	cutoff := now.Add(-c.memberTimeout())
	var out []silentAgent
	for k, hb := range c.hb {
		if len(hb) == 0 || (groupedOnly && k.group == "") {
			continue
		}
		last := hb.end().Add(-c.heartbeatGrace)
		if last.Before(cutoff) && !last.Before(since) {
			out = append(out, silentAgent{Group: k.group, Node: k.node, LastSeen: last})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Node < out[j].Node
	})
	return out
}

// memberOutage is a stretch during which a CLUSTER_NAME member was not
// current - silent for longer than the member timeout, or flapping - and so
// did not hold its group back while what only it could see went unseen.
type memberOutage struct {
	Group, Node string
	// From is its last heartbeat before the outage; To the heartbeat that
	// ended it, zero while it lasts.
	From, To time.Time
	// Intermittent: the agent kept reporting now and then (flapping)
	// rather than being silent throughout.
	Intermittent bool
	LastSeen     time.Time // the agent's most recent heartbeat
}

// memberOutages lists, per CLUSTER_NAME member, its most recent outage
// that overlaps [since, now] - whether or not the agent is reporting again.
//
// An outage is the member's own failure, not the backend's: its silence
// counts only while collection was otherwise alive, i.e. while some OTHER
// agent of the installation (any group, or ungrouped) was heartbeating. A
// backend outage silences every agent at once and is never listed - while
// no member reports, the group is unobserved and nothing accrues anyway.
// A whole group going silent while another group or an ungrouped node keeps
// reporting is listed. The rule, per silence of the member:
//
//   - longer than the member timeout: an outage if other agents reported
//     for longer than the timeout during it; it is reported from the first
//     moment they did (the member's last heartbeat, unless the backend was
//     down then too);
//   - shorter (only a flapping member is not current then): an outage if
//     other agents reported during its not-current part.
//
// Outages less than a member timeout apart (a crash loop) are one,
// marked intermittent. Heartbeats should not be clipped to the window, so
// an outage that began before it is still seen. Ordered: ongoing first,
// then newest end first.
func (c *observationCoverage) memberOutages(since, now time.Time) []memberOutage {
	timeout := c.memberTimeout()
	var out []memberOutage
	for k, hb := range c.hb {
		if k.group == "" || len(hb) == 0 {
			continue
		}
		life := coverage{{hb[0].from, now}}
		silence := life.minus(hb)
		notCurrent := silence.minus(c.memberGaps(hb))
		var cur *memberOutage
		var last memberOutage
		found, silences := false, 0
		flush := func() {
			if cur != nil {
				cur.Intermittent = silences > 1
				last, found = *cur, true
			}
		}
		for _, sil := range silence {
			if !sil.to.After(since) || notCurrent.covered(sil.from, sil.to) == 0 {
				continue
			}
			// Within the member's own silence, hbAll is the other agents.
			alive := coverage{sil}.intersect(c.hbAll)
			from := sil.from.Add(-c.heartbeatGrace) // its last heartbeat
			if sil.to.Sub(sil.from) > timeout {
				if alive.total() <= timeout {
					continue // the backend (or everything) was down
				}
				if alive[0].from.After(sil.from) {
					from = alive[0].from
				}
			} else if (coverage{sil}).intersect(notCurrent).intersect(c.hbAll).total() == 0 {
				continue
			}
			if cur != nil && !from.After(cur.To.Add(timeout)) {
				silences++
				cur.To = sil.to
				continue
			}
			flush()
			cur = &memberOutage{Group: k.group, Node: k.node, From: from, To: sil.to}
			silences = 1
		}
		flush()
		if !found {
			continue
		}
		if !last.To.Before(now) {
			last.To = time.Time{} // still going on
		}
		last.LastSeen = hb.end().Add(-c.heartbeatGrace)
		if last.LastSeen.After(now) {
			last.LastSeen = now
		}
		out = append(out, last)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.To.IsZero() != b.To.IsZero() {
			return a.To.IsZero()
		}
		if !a.To.Equal(b.To) {
			return a.To.After(b.To)
		}
		if !a.From.Equal(b.From) {
			return a.From.After(b.From)
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Node < b.Node
	})
	return out
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

// intersect is the part of c also in b.
func (c coverage) intersect(b coverage) coverage {
	return c.minus(c.minus(b))
}

// localEndpointID reports whether a service ID can only have been produced by
// the agent of the node it is on: a process-keyed ID ("IP/proc", see
// makeServiceIDWithProcess). "IP:port" and bare-IP IDs are the far end as
// another agent saw it; resolved (Kubernetes) names may come from either end.
func localEndpointID(id string) bool {
	i := strings.Index(id, "/")
	return i > 0 && net.ParseIP(id[:i]) != nil
}

// edgeEvidenceNodes returns the nodes an edge interval is evidence of
// observation for: the nodes of its process-keyed endpoints only, since
// either agent can extend an edge between Kubernetes names.
func edgeEvidenceNodes(sourceID, targetID string, nodeOf func(string) string) []string {
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

// edgeReporters returns the nodes whose coverage an edge's absence is
// measured against: the nodes of its process-keyed endpoints; failing those,
// the node on the source's service row, else the target's. Empty when
// neither end has a node (external addresses): the global coverage.
func edgeReporters(sourceID, targetID string, nodeOf func(string) string) []string {
	if out := edgeEvidenceNodes(sourceID, targetID, nodeOf); len(out) > 0 {
		return out
	}
	for _, id := range []string{sourceID, targetID} {
		if n := nodeOf(id); n != "" {
			return []string{n}
		}
	}
	return nil
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
	now := in.now
	cov.now = func() time.Time { return now }
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
	// Heartbeats are kept from before the window (all of retention): a
	// member's first heartbeat, its flapping ratio and an outage that began
	// before the window must not look as if the agent joined at its start.
	// Coverage is only ever queried within the window.
	beats := make([]storage.NodeInterval, 0, len(in.historyNodes))
	for _, ni := range in.historyNodes {
		if ni.LastSeen.After(hi) {
			ni.LastSeen = hi
		}
		if !ni.LastSeen.Before(ni.FirstSeen) {
			beats = append(beats, ni)
		}
	}
	cov.setHeartbeats(beats)
	groups := make([]string, 0, len(in.groups))
	for g := range in.groups {
		groups = append(groups, g)
	}
	cov.setGroups(groups)
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
			cov.addEvidence("", edgeEvidenceNodes(ci.SourceID, ci.TargetID, nodeOf), from, to)
		}
	}
	return cov
}
