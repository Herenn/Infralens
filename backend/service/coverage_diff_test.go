package service

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

type rIv struct {
	key      string
	nodes    []string
	from, to time.Time
}

// Ported from the fourth review: incremental evidence (drift-style, pass by
// pass, with extensions, late re-reads and retention forget, so with
// evidence folded as it closes) vs from scratch at the end: per-node and
// global coverage over the retention window must be identical.
func TestReviewR3CoverageDifferential(t *testing.T) {
	const maxGap = 5 * time.Minute
	const retention = 3 * 24 * time.Hour
	for seed := int64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewSource(seed))
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		clock := start
		inc := newObservationCoverage(maxGap, 30*time.Minute, 0)
		inc.now = func() time.Time { return clock }
		nNodes := 2 + r.Intn(6)
		nodes := func() []string {
			switch r.Intn(5) {
			case 0:
				return nil
			case 1:
				return []string{fmt.Sprintf("n%d", r.Intn(nNodes)), fmt.Sprintf("n%d", r.Intn(nNodes))}
			default:
				return []string{fmt.Sprintf("n%d", r.Intn(nNodes))}
			}
		}
		var all []*rIv
		open := map[string]*rIv{}
		id := 0
		lastCheck := clock
		for step := 0; step < 1500; step++ {
			clock = clock.Add(time.Duration(1+r.Intn(10)) * time.Minute)
			// sightings: extend open intervals within maxGap, open new ones
			for k, iv := range open {
				if r.Intn(3) == 0 {
					if clock.Sub(iv.to) <= maxGap {
						iv.to = clock
					} else {
						delete(open, k)
					}
				}
			}
			if r.Intn(2) == 0 {
				id++
				iv := &rIv{key: fmt.Sprintf("%d|c%d", id, id), nodes: nodes(), from: clock, to: clock}
				all = append(all, iv)
				open[iv.key] = iv
			}
			// a pass every ~5 steps: re-read intervals overlapping [lastCheck-maxGap, now]
			if step%5 == 0 {
				lo := lastCheck.Add(-maxGap)
				for _, iv := range all {
					if !iv.to.Before(lo) && !iv.from.After(clock) {
						inc.addEvidence(iv.key, iv.nodes, iv.from, iv.to)
					}
				}
				inc.forget(clock.Add(-retention))
				lastCheck = clock
			}
		}
		// final pass, as check() would
		lo := lastCheck.Add(-maxGap)
		for _, iv := range all {
			if !iv.to.Before(lo) && !iv.from.After(clock) {
				inc.addEvidence(iv.key, iv.nodes, iv.from, iv.to)
			}
		}
		inc.forget(clock.Add(-retention))

		scratch := newObservationCoverage(maxGap, 30*time.Minute, 0)
		scratch.now = func() time.Time { return clock }
		for _, iv := range all {
			if !iv.to.Before(clock.Add(-retention)) {
				scratch.addEvidence(iv.key, iv.nodes, iv.from, iv.to)
			}
		}
		from := clock.Add(-retention).Add(time.Hour) // skip the grace tail at the retention edge
		check := func(name string, a, b coverage) {
			// compare on 1-minute resolution sub-windows
			for w := from; w.Before(clock); w = w.Add(17 * time.Minute) {
				e := w.Add(17 * time.Minute)
				if x, y := a.covered(w, e), b.covered(w, e); x != y {
					t.Fatalf("seed %d %s: [%s,%s] incremental %s vs scratch %s", seed, name, w.Format("15:04"), e.Format("15:04"), x, y)
				}
			}
		}
		for i := 0; i < nNodes; i++ {
			n := fmt.Sprintf("n%d", i)
			check(n, inc.nodeCoverage(n), scratch.nodeCoverage(n))
		}
		check("global", inc.globalCoverage(), scratch.globalCoverage())
	}
}
