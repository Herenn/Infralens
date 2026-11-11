package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// The AI context names a destination by DNS name only where every other view
// does: address-keyed (external) targets. A Kubernetes-resolved target keeps
// its Kubernetes name even if a DNS name was recorded on the edge.
func TestAIContextHostnameOnlyForAddressKeyedTargets(t *testing.T) {
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bus := service.NewEventBus(16)
	defer bus.Close()
	ts := service.NewTopologyService(store, bus)
	ctx := context.Background()
	now := time.Now()

	src := &storage.Service{ID: "10.0.0.5/checkout", Name: "checkout", LastSeen: now}
	k8s := &storage.Service{ID: "Deploy: default/payments", Name: "payments", ResolvedName: "Deploy: default/payments", LastSeen: now}
	ext := &storage.Service{ID: "52.1.2.3:443", Name: ":443", DisplayName: "52.1.2.3", PodIP: "52.1.2.3", LastSeen: now}
	for _, s := range []*storage.Service{src, k8s, ext} {
		if err := ts.AddOrUpdateService(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []*storage.Connection{
		{ID: "c1", SourceID: src.ID, TargetID: k8s.ID, Port: 443, LastSeen: now, Hostnames: storage.ObservedHostnames(now, "payments.default.svc.cluster.local")},
		{ID: "c2", SourceID: src.ID, TargetID: ext.ID, Port: 443, LastSeen: now, Hostnames: storage.ObservedHostnames(now, "api.stripe.com")},
	} {
		if err := ts.AddConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	h := NewAIHandler(ts, nil)
	got := map[string]string{}
	for _, c := range h.buildServiceContext(ctx, src).OutgoingConnections {
		got[c.RemoteIP] = c.RemoteName
	}
	if got["52.1.2.3"] != "api.stripe.com" {
		t.Errorf("external target named %q", got["52.1.2.3"])
	}
	if got[""] != "payments" {
		t.Errorf("Kubernetes target renamed to %q", got[""])
	}
}
