package service

import (
	"fmt"
	"testing"
	"time"
)

// TestAgentNodeRegistryIsBounded: entries expire on write as well as on
// read, emptied groups go away, and the size is capped.
func TestAgentNodeRegistryIsBounded(t *testing.T) {
	ts, _ := newHistoryTestService(t, false)
	ts.NoteAgentNode("cluster", "old-node")
	ts.idle.groupsMu.Lock()
	ts.idle.groups["cluster"]["old-node"] = time.Now().Add(-2 * agentGroupTTL)
	ts.idle.groups["gone-group"] = map[string]time.Time{"x": time.Now().Add(-2 * agentGroupTTL)}
	ts.idle.groupsSize++
	ts.idle.groupsSwept = time.Time{}
	ts.idle.groupsMu.Unlock()

	ts.NoteAgentNode("cluster", "new-node") // a write sweeps
	ts.idle.groupsMu.Lock()
	_, stale := ts.idle.groups["cluster"]["old-node"]
	_, emptied := ts.idle.groups["gone-group"]
	size := ts.idle.groupsSize
	ts.idle.groupsMu.Unlock()
	if stale || emptied || size != 1 {
		t.Errorf("after sweep: stale=%v emptied group kept=%v size=%d, want false/false/1", stale, emptied, size)
	}

	for i := 0; i < maxAgentNodes+50; i++ {
		ts.NoteAgentNode("cluster", fmt.Sprintf("n%d", i))
	}
	ts.idle.groupsMu.Lock()
	size = ts.idle.groupsSize
	n := len(ts.idle.groups["cluster"])
	ts.idle.groupsMu.Unlock()
	if size > maxAgentNodes || n > maxAgentNodes {
		t.Errorf("registry grew to %d (%d in group), cap %d", size, n, maxAgentNodes)
	}
}

// TestLivenessRefreshFitsPruneAge: with PRUNE_MAX_AGE under a few minutes
// the endpoint refresh throttle shrinks so a live connection's endpoints
// are refreshed at least twice per prune window (they were pruned with a
// 40s prune age and the fixed one-minute throttle).
func TestLivenessRefreshFitsPruneAge(t *testing.T) {
	ts, _ := newHistoryTestService(t, false)
	ts.SetPruneMaxAge(40 * time.Second)
	now := time.Now()
	if !ts.claimEndpointRefresh("svc", now) {
		t.Fatal("first refresh refused")
	}
	if ts.claimEndpointRefresh("svc", now.Add(10*time.Second)) {
		t.Error("refreshed again within the throttle")
	}
	if !ts.claimEndpointRefresh("svc", now.Add(14*time.Second)) {
		t.Error("no refresh after a third of the 40s prune age; the endpoint would be pruned while traffic flows")
	}
	ts.SetPruneMaxAge(30 * time.Minute)
	if ts.claimEndpointRefresh("other", now) && ts.claimEndpointRefresh("other", now.Add(30*time.Second)) {
		t.Error("with the default prune age the one-minute throttle should apply")
	}
}
