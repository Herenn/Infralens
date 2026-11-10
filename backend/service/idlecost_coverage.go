package service

import (
	"sort"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Observation coverage: when was InfraLens actually watching?
//
// History records intervals during which an entity was continuously observed.
// When the backend or an agent is down, nothing is recorded - so in raw data
// an outage is indistinguishable from silence, and "last called 8 days ago"
// might mean "we stopped listening 8 days ago". drift.go guards the same trap
// for disappearing dependencies; here it is solved by measuring every quiet
// period against coverage rather than against the clock.
//
// Coverage of a node is the union of the history intervals of every service
// on that node and every connection touching one, each extended by maxGap (the
// recorder's own tolerance for "still present"). An agent that is up records
// something on its node at least as often as anything there talks; one that is
// down records nothing, and the gap shows. The global coverage is the union
// over all nodes: its gaps are backend outages.
//
// Known limit: a node where genuinely nothing happens for a while also shows a
// gap. That errs in the safe direction - silence is under-counted, so a
// service is held back rather than flagged.

type span struct{ from, to time.Time }

type coverage []span // sorted, non-overlapping

func buildCoverage(spans []span, lo, hi time.Time) coverage {
	clipped := make([]span, 0, len(spans))
	for _, s := range spans {
		if s.from.Before(lo) {
			s.from = lo
		}
		if s.to.After(hi) {
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

// observationIndex holds per-node and global coverage for one report.
type observationIndex struct {
	byNode map[string]coverage
	global coverage
}

func buildObservationIndex(in idleInputs, nodeOf func(id string) string) observationIndex {
	grace := in.maxGap
	if grace <= 0 {
		grace = storage.DefaultHistoryMaxGap
	}
	lo, hi := in.coverageStart, in.now
	perNode := map[string][]span{}
	var all []span
	add := func(node string, from, to time.Time) {
		s := span{from: from, to: to.Add(grace)}
		all = append(all, s)
		if node != "" {
			perNode[node] = append(perNode[node], s)
		}
	}
	for _, si := range in.historyServices {
		add(si.Node, si.FirstSeen, si.LastSeen)
	}
	for _, ci := range in.historyEdges {
		src, dst := nodeOf(ci.SourceID), nodeOf(ci.TargetID)
		add(src, ci.FirstSeen, ci.LastSeen)
		if dst != src {
			add(dst, ci.FirstSeen, ci.LastSeen)
		}
	}
	idx := observationIndex{byNode: map[string]coverage{}, global: buildCoverage(all, lo, hi)}
	for n, spans := range perNode {
		idx.byNode[n] = buildCoverage(spans, lo, hi)
	}
	return idx
}

// observed is how much of [from, to] node was being watched. Nodes with no
// recorded history at all fall back to global coverage, which is the only
// evidence there is (and still subtracts backend outages).
func (o observationIndex) observed(node string, from, to time.Time) time.Duration {
	if c, ok := o.byNode[node]; ok {
		return c.covered(from, to)
	}
	return o.global.covered(from, to)
}
