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
// What counts as evidence that a node was being watched is the subtle part.
// It must be something only that node's own agent can produce:
//
//   - Agent heartbeats (storage.NodeInterval): every agent reports host
//     metrics every few seconds, recorded as node intervals. Only the node's
//     own agent extends them. This is the coverage from the first recorded
//     heartbeat on (the "epoch").
//   - Before the epoch (history written by v3.0, before any heartbeat was
//     recorded) only service and edge intervals are available, and an
//     interval is extended by the agent that observed the socket - which is
//     NOT always the agent of the node it touches. The client's agent reports
//     its outbound connect as "client/proc -> server-IP:port", the server's
//     agent reports the accept as "client-IP -> server/proc" (processor.go),
//     throughput samples come from the local socket's side, and
//     refreshHistoryFromStats refreshes both endpoint services of the edge
//     the sample landed on. So evidence counts for a node only through IDs
//     that only its own agent can produce: process-keyed IDs ("IP/proc") are
//     always the reporting agent's own process - a service with such an ID,
//     or an edge with such an endpoint, credits that endpoint's node.
//     "IP:port" and bare-IP IDs are the far end as another agent saw it and
//     never credit their node. Kubernetes-resolved names are ambiguous (both
//     agents report the same ID, so a live client keeps a dead server's edge
//     and service row fresh) and credit no node.
//
// A node with no evidence of either kind (no heartbeat ever, or no local
// edge before the epoch) falls back to the global coverage - the union over
// all nodes, whose gaps are backend or collection-wide outages. After the
// epoch a node without heartbeats in a stretch was not observed in it,
// whatever its peers reported.
//
// Known limit: before the epoch, a node where genuinely nothing happened for
// a while also shows a gap. That errs in the safe direction - silence is
// under-counted, so a service is held back rather than flagged and an edge
// is reported gone later rather than falsely.

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

// covered is how much of [from, to] the coverage includes.
func (c coverage) covered(from, to time.Time) time.Duration {
	var d time.Duration
	for _, s := range c {
		a, b := s.from, s.to
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
// file). Build it with newObservationCoverage, give it heartbeats and
// evidence, and ask observed(nodes, from, to). The drift detector keeps one
// for its lifetime: it replaces the heartbeats on every pass and updates
// evidence by key as intervals are extended.
type observationCoverage struct {
	heartbeatGrace time.Duration // added to a heartbeat interval's end
	edgeGrace      time.Duration // added to an evidence interval's end

	epoch time.Time // first recorded heartbeat; zero: none yet

	heartbeat    map[string][]span
	heartbeatAll []span
	evidence     map[string]evidenceSpan // by caller-chosen key

	cache map[string]coverage // merged unions, rebuilt lazily after a change
}

type evidenceSpan struct {
	nodes []string
	s     span
}

func newObservationCoverage(heartbeatGrace, edgeGrace time.Duration) *observationCoverage {
	return &observationCoverage{
		heartbeatGrace: heartbeatGrace,
		edgeGrace:      edgeGrace,
		heartbeat:      map[string][]span{},
		evidence:       map[string]evidenceSpan{},
		cache:          map[string]coverage{},
	}
}

// addHeartbeat records that node's agent was reporting over [from, to].
func (c *observationCoverage) addHeartbeat(node string, from, to time.Time) {
	if node == "" {
		return
	}
	s := span{from: from, to: to.Add(c.heartbeatGrace)}
	c.heartbeat[node] = append(c.heartbeat[node], s)
	c.heartbeatAll = append(c.heartbeatAll, s)
	if c.epoch.IsZero() || from.Before(c.epoch) {
		c.epoch = from
	}
	c.changed()
}

// setHeartbeats replaces all heartbeat coverage with the given intervals.
func (c *observationCoverage) setHeartbeats(intervals []storage.NodeInterval) {
	c.heartbeat = map[string][]span{}
	c.heartbeatAll = nil
	c.epoch = time.Time{}
	for _, ni := range intervals {
		c.addHeartbeat(ni.Node, ni.FirstSeen, ni.LastSeen)
	}
	c.changed()
}

// addEvidence records a service or edge interval as pre-epoch evidence for
// the given nodes (those whose agent produced it - see edgeReporters). Every
// interval also counts towards the global coverage. A later call with the
// same key replaces the span (an interval extended in place); an empty key
// always adds.
func (c *observationCoverage) addEvidence(key string, nodes []string, from, to time.Time) {
	if key == "" {
		key = fmt.Sprintf("#%d", len(c.evidence))
	}
	c.evidence[key] = evidenceSpan{nodes: nodes, s: span{from: from, to: to.Add(c.edgeGrace)}}
	c.changed()
}

func (c *observationCoverage) changed() {
	if len(c.cache) > 0 {
		c.cache = map[string]coverage{}
	}
}

// forget drops evidence that ended before the cutoff (drift's retention).
// Heartbeats are replaced wholesale by setHeartbeats instead.
func (c *observationCoverage) forget(before time.Time) {
	for k, e := range c.evidence {
		if e.s.to.Before(before) {
			delete(c.evidence, k)
		}
	}
	c.changed()
}

func (c *observationCoverage) merged(key string, build func() []span) coverage {
	if cv, ok := c.cache[key]; ok {
		return cv
	}
	cv := buildCoverage(build(), time.Time{}, time.Time{})
	c.cache[key] = cv
	return cv
}

// observed is how much of [from, to] at least one of nodes was being watched.
// No nodes (an endpoint with no node, such as an external address) means the
// global coverage.
func (c *observationCoverage) observed(nodes []string, from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	split := to
	if !c.epoch.IsZero() {
		switch {
		case !c.epoch.After(from):
			split = from
		case c.epoch.Before(to):
			split = c.epoch
		}
	}
	key := strings.Join(nodes, "\x00")
	var d time.Duration
	if split.After(from) { // before the first heartbeat: evidence
		cv := c.merged("e\x01"+key, func() []span {
			want := make(map[string]bool, len(nodes))
			for _, n := range nodes {
				want[n] = true
			}
			var mine, all []span
			for _, e := range c.evidence {
				all = append(all, e.s)
				for _, n := range e.nodes {
					if want[n] {
						mine = append(mine, e.s)
						break
					}
				}
			}
			if len(mine) == 0 {
				return all // no evidence of this node at all: global
			}
			return mine
		})
		d += cv.covered(from, split)
	}
	if to.After(split) { // from the first heartbeat on: heartbeats only
		cv := c.merged("h\x01"+key, func() []span {
			if len(nodes) == 0 {
				return c.heartbeatAll
			}
			var ss []span
			for _, n := range nodes {
				ss = append(ss, c.heartbeat[n]...)
			}
			return ss
		})
		d += cv.covered(split, to)
	}
	return d
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
// from node intervals, pre-epoch evidence from the window's service and edge
// intervals.
func buildIdleCoverage(in idleInputs, nodeOf func(id string) string) *observationCoverage {
	grace := in.maxGap
	if grace <= 0 {
		grace = storage.DefaultHistoryMaxGap
	}
	cov := newObservationCoverage(grace, grace)
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
	for _, ni := range in.historyNodes {
		// The epoch is the first heartbeat ever recorded, even if it lies
		// before the window: otherwise a window starting after it would be
		// judged on edge evidence. (addHeartbeat only ever lowers it.)
		if cov.epoch.IsZero() || ni.FirstSeen.Before(cov.epoch) {
			cov.epoch = ni.FirstSeen
		}
		if from, to, ok := clip(ni.FirstSeen, ni.LastSeen); ok {
			cov.addHeartbeat(ni.Node, from, to)
		}
	}
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
