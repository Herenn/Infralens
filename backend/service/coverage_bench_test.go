package service

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Coverage cost at scale (ported from the review's benchmarks): 500 nodes,
// 5,000 edges, and - right after a v3.0 upgrade - 200,000 intervals of
// pre-heartbeat evidence. The first coverage implementation rebuilt every
// node's coverage from all evidence after each heartbeat refresh: ~3s per
// drift pass and per idle-spend report. Evidence is now indexed by node as
// it arrives and merged once per changed node.

func benchBeats(nodes int, now time.Time) []storage.NodeInterval {
	r := rand.New(rand.NewSource(1))
	var beats []storage.NodeInterval
	for n := 0; n < nodes; n++ {
		t := now.Add(-30 * 24 * time.Hour)
		for t.Before(now) {
			end := t.Add(time.Duration(6+r.Intn(18)) * time.Hour)
			if end.After(now) {
				end = now
			}
			beats = append(beats, storage.NodeInterval{Node: fmt.Sprintf("node-%d", n), FirstSeen: t, LastSeen: end})
			t = end.Add(time.Duration(6+r.Intn(20)) * time.Minute)
		}
	}
	return beats
}

type benchEdge struct {
	nodes []string
	last  time.Time
}

// driftPassWorld builds a coverage with preEpochEvidence evidence intervals,
// the heartbeats, and the edges one pass evaluates.
func driftPassWorld(preEpochEvidence int) (*observationCoverage, []storage.NodeInterval, []benchEdge, time.Time) {
	now := time.Now()
	beats := benchBeats(500, now)
	if preEpochEvidence > 0 {
		// upgraded 1 day ago: heartbeats only in the last day
		var kept []storage.NodeInterval
		for _, bt := range beats {
			if bt.LastSeen.After(now.Add(-24 * time.Hour)) {
				if bt.FirstSeen.Before(now.Add(-24 * time.Hour)) {
					bt.FirstSeen = now.Add(-24 * time.Hour)
				}
				kept = append(kept, bt)
			}
		}
		beats = kept
	}
	cov := newObservationCoverage(5*time.Minute, 15*time.Minute, 72*time.Hour)
	r := rand.New(rand.NewSource(2))
	for i := 0; i < preEpochEvidence; i++ {
		from := now.Add(-time.Duration(r.Int63n(int64(29 * 24 * time.Hour)))).Add(-24 * time.Hour)
		cov.addEvidence(fmt.Sprintf("k%d", i), []string{fmt.Sprintf("node-%d", r.Intn(500))}, from, from.Add(time.Hour))
	}
	var edges []benchEdge
	for i := 0; i < 5000; i++ {
		last := now.Add(-time.Duration(r.Int63n(int64(time.Hour))))
		if preEpochEvidence > 0 && i%5 == 0 {
			last = now.Add(-25 * time.Hour)
		}
		edges = append(edges, benchEdge{[]string{fmt.Sprintf("node-%d", r.Intn(500))}, last})
	}
	return cov, beats, edges, now
}

// driftPass is one drift pass: heartbeats replaced, gone() for every edge.
func driftPass(cov *observationCoverage, beats []storage.NodeInterval, edges []benchEdge, now time.Time) {
	cov.setHeartbeats(beats)
	for _, ed := range edges {
		cov.observed(ed.nodes, ed.last, now)
	}
}

func BenchmarkCoverageDriftPass(b *testing.B) {
	for _, evidence := range []int{0, 200000} {
		b.Run(fmt.Sprintf("evidence=%d", evidence), func(b *testing.B) {
			cov, beats, edges, now := driftPassWorld(evidence)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				driftPass(cov, beats, edges, now)
			}
		})
	}
}

func idleUpgradeWorld() (idleInputs, map[string]string) {
	now := time.Now()
	r := rand.New(rand.NewSource(3))
	in := idleInputs{now: now, maxGap: 5 * time.Minute, coverageStart: now.Add(-30 * 24 * time.Hour), historyEnabled: true, retireAfter: 7 * 24 * time.Hour}
	for n := 0; n < 500; n++ {
		in.historyNodes = append(in.historyNodes, storage.NodeInterval{Node: fmt.Sprintf("node-%d", n), FirstSeen: now.Add(-time.Hour), LastSeen: now})
	}
	nodeOf := map[string]string{}
	for i := 0; i < 100000; i++ {
		n := fmt.Sprintf("node-%d", r.Intn(500))
		id := fmt.Sprintf("10.%d.%d.%d/p", i>>16&255, i>>8&255, i&255)
		nodeOf[id] = n
		from := now.Add(-time.Duration(r.Int63n(int64(29 * 24 * time.Hour))))
		in.historyServices = append(in.historyServices, storage.ServiceInterval{ServiceID: id, Node: n, FirstSeen: from, LastSeen: from.Add(time.Hour)})
		in.historyEdges = append(in.historyEdges, storage.ConnectionInterval{SourceID: id, TargetID: "10.9.9.9:5432", FirstSeen: from, LastSeen: from.Add(time.Hour)})
	}
	return in, nodeOf
}

func idleCoveragePass(in idleInputs, nodeOf map[string]string) {
	cov := buildIdleCoverage(in, func(id string) string { return nodeOf[id] })
	cov.total(in.coverageStart, in.now)
	for n := 0; n < 500; n++ {
		cov.observed([]string{fmt.Sprintf("node-%d", n)}, in.now.Add(-10*24*time.Hour), in.now)
	}
}

func BenchmarkCoverageIdleAfterUpgrade(b *testing.B) {
	in, nodeOf := idleUpgradeWorld()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idleCoveragePass(in, nodeOf)
	}
}

// TestCoverageCostBudget keeps the worst case (200k intervals of evidence,
// 500 nodes) inside a budget, generous enough for slow CI machines and the
// race detector but far below the ~3s the first implementation took.
func TestCoverageCostBudget(t *testing.T) {
	if testing.Short() || os.Getenv("INFRALENS_SKIP_PERF") != "" {
		t.Skip("perf budget")
	}
	budget := 750 * time.Millisecond
	if raceEnabled {
		budget *= 8
	}
	cov, beats, edges, now := driftPassWorld(200000)
	driftPass(cov, beats, edges, now) // the first pass builds the evidence index
	began := time.Now()
	driftPass(cov, beats, edges, now)
	if took := time.Since(began); took > budget {
		t.Errorf("a drift pass over 200k evidence intervals took %s, budget %s", took, budget)
	}
	in, nodeOf := idleUpgradeWorld()
	began = time.Now()
	idleCoveragePass(in, nodeOf)
	if took := time.Since(began); took > 2*budget {
		t.Errorf("idle coverage after an upgrade took %s, budget %s", took, 2*budget)
	}
}
