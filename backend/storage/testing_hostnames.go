package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// HostnamesSuite verifies DNS-name persistence on connections and their
// history intervals. Part of StoreSuite, so it runs against every backend.
func HostnamesSuite(t *testing.T, store Store) {
	ctx := context.Background()
	seen := time.Now().Add(-time.Minute).Truncate(time.Second).UTC()
	two := []Hostname{{Name: "api.stripe.com", LastSeen: seen}, {Name: "files.stripe.com", LastSeen: seen.Add(-time.Hour)}}

	t.Run("Upsert_RoundTrip_And_Preserve", func(t *testing.T) {
		id := fmt.Sprintf("conn-host-%d", time.Now().UnixNano())
		conn := &Connection{ID: id, SourceID: "a", TargetID: "52.1.2.3:443", Port: 443, Count: 1, Hostnames: two}
		if err := store.Connections().Upsert(ctx, conn); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		defer func() {
			if err := store.Connections().Delete(ctx, id); err != nil {
				t.Errorf("cleanup: Connections().Delete: %v", err)
			}
		}()

		got, err := store.Connections().Get(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("Get: %v %v", got, err)
		}
		if !EqualHostnames(got.Hostnames, two) {
			t.Errorf("Get hostnames = %v, want %v (names and last-seen times)", got.Hostnames, two)
		}

		// A sighting without names (old agent, no DNS seen) must not erase them.
		if err := store.Connections().Upsert(ctx, &Connection{ID: id, SourceID: "a", TargetID: "52.1.2.3:443", Port: 443}); err != nil {
			t.Fatalf("Upsert without names: %v", err)
		}
		list, err := store.Connections().List(ctx, ConnectionFilter{SourceID: "a", TargetID: "52.1.2.3:443"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var found bool
		for _, c := range list {
			if c.ID == id {
				found = true
				if !EqualHostnames(c.Hostnames, two) {
					t.Errorf("List hostnames after nameless upsert = %v", c.Hostnames)
				}
			}
		}
		if !found {
			t.Fatal("connection missing from List")
		}

		// A sighting with names replaces them (callers pass the merged list).
		one := []Hostname{{Name: "b.example", LastSeen: seen}}
		if err := store.Connections().Upsert(ctx, &Connection{ID: id, SourceID: "a", TargetID: "52.1.2.3:443", Port: 443, Hostnames: one}); err != nil {
			t.Fatalf("Upsert with names: %v", err)
		}
		got, _ = store.Connections().Get(ctx, id)
		if !EqualHostnames(got.Hostnames, one) {
			t.Errorf("hostnames after replacing upsert = %v", got.Hostnames)
		}
	})

	t.Run("SetHostnames", func(t *testing.T) {
		id := fmt.Sprintf("conn-sethost-%d", time.Now().UnixNano())
		if err := store.Connections().Upsert(ctx, &Connection{ID: id, SourceID: "a", TargetID: "b", Port: 443, Count: 1}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := store.Connections().Delete(ctx, id); err != nil {
				t.Errorf("cleanup: Connections().Delete: %v", err)
			}
		}()
		before, _ := store.Connections().Get(ctx, id)

		if err := store.Connections().SetHostnames(ctx, id, two[:1]); err != nil {
			t.Fatalf("SetHostnames: %v", err)
		}
		after, _ := store.Connections().Get(ctx, id)
		if !EqualHostnames(after.Hostnames, two[:1]) {
			t.Errorf("hostnames = %v", after.Hostnames)
		}
		if after.Count != before.Count || !after.LastSeen.Equal(before.LastSeen) {
			t.Errorf("SetHostnames changed count/last_seen: %+v -> %+v", before, after)
		}

		if err := store.Connections().SetHostnames(ctx, id, nil); err != nil {
			t.Fatalf("clearing: %v", err)
		}
		if got, _ := store.Connections().Get(ctx, id); len(got.Hostnames) != 0 {
			t.Errorf("hostnames after clear = %v", got.Hostnames)
		}

		if err := store.Connections().SetHostnames(ctx, "does-not-exist", two); err != nil {
			t.Errorf("SetHostnames on a missing ID must be a no-op, got %v", err)
		}
	})

	t.Run("History_RecordsAndKeepsHostnames", func(t *testing.T) {
		h := store.History()
		base := time.Now().Add(-time.Hour).Truncate(time.Second)
		id := fmt.Sprintf("hist-host-%d", time.Now().UnixNano())
		conn := &Connection{ID: id, SourceID: id + "-src", TargetID: "52.1.2.3:443", Port: 443}

		// Opened without a name: the connect event raced the DNS answer.
		if err := h.RecordConnection(ctx, conn, base, 5*time.Minute); err != nil {
			t.Fatalf("RecordConnection: %v", err)
		}
		// Extended with one.
		conn.Hostnames = two[:1]
		if err := h.RecordConnection(ctx, conn, base.Add(time.Minute), 5*time.Minute); err != nil {
			t.Fatalf("RecordConnection: %v", err)
		}
		// Extended again without: must keep it.
		conn.Hostnames = nil
		if err := h.RecordConnection(ctx, conn, base.Add(2*time.Minute), 5*time.Minute); err != nil {
			t.Fatalf("RecordConnection: %v", err)
		}

		at, err := h.ConnectionsAt(ctx, base.Add(2*time.Minute), 5*time.Minute)
		if err != nil {
			t.Fatalf("ConnectionsAt: %v", err)
		}
		var got *ConnectionInterval
		for i := range at {
			if at[i].ConnectionID == id {
				got = &at[i]
			}
		}
		if got == nil {
			t.Fatal("interval missing")
		}
		if !EqualHostnames(got.Hostnames, two[:1]) {
			t.Errorf("ConnectionsAt hostnames = %v", got.Hostnames)
		}

		between, err := h.ConnectionsBetween(ctx, base, base.Add(3*time.Minute))
		if err != nil {
			t.Fatalf("ConnectionsBetween: %v", err)
		}
		for _, ci := range between {
			if ci.ConnectionID == id && !EqualHostnames(ci.Hostnames, two[:1]) {
				t.Errorf("ConnectionsBetween hostnames = %v", ci.Hostnames)
			}
		}

		// A new interval after a gap starts with whatever the sighting carries.
		conn.Hostnames = two
		if err := h.RecordConnection(ctx, conn, base.Add(30*time.Minute), 5*time.Minute); err != nil {
			t.Fatalf("RecordConnection after gap: %v", err)
		}
		between, _ = h.ConnectionsBetween(ctx, base.Add(29*time.Minute), base.Add(31*time.Minute))
		for _, ci := range between {
			if ci.ConnectionID == id && !EqualHostnames(ci.Hostnames, two) {
				t.Errorf("new interval hostnames = %v", ci.Hostnames)
			}
		}
	})
}
