package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// TestHostnameNamesEverySurface: an external endpoint reached by a DNS name
// is called by that name - not its bare address - in the Changes feed
// (target_name, through the edge's own name), the webhook text, and the
// shared name rule Idle-spend evidence, MCP and exports use
// (ServiceHostname). Kubernetes-resolved services keep their own names.
func TestHostnameNamesEverySurface(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)

	n := &recordingNotifier{}
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, n)

	const ext = "198.51.100.20:443"
	h.advance(time.Minute)
	conn := &storage.Connection{
		ID: "api->" + ext, SourceID: "api", TargetID: ext, Port: 443, Protocol: "tcp",
		Hostnames: storage.ObservedHostnames(h.clock, "api.stripe.com"),
	}
	if err := h.store.History().RecordConnection(ctx, conn, h.clock, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	// Current state knows the external only by address, as the processor
	// records it.
	if err := h.store.Services().Upsert(ctx, &storage.Service{ID: ext, Name: ":443", DisplayName: "198.51.100.20"}); err != nil {
		t.Fatal(err)
	}
	h.advance(4 * time.Minute)
	d.step(ctx)

	feed := h.feed()
	assertFeed(t, feed, "appeared:api->"+ext)
	if got := feed[0].TargetName; got != "api.stripe.com" {
		t.Errorf("feed target_name = %q, want api.stripe.com", got)
	}

	n.mu.Lock()
	reports := append([]*DriftReport(nil), n.reports...)
	n.mu.Unlock()
	if len(reports) != 1 || len(reports[0].Appeared) != 1 {
		t.Fatalf("notifier reports = %+v", reports)
	}
	if got := reports[0].Appeared[0].TargetHostname; got != "api.stripe.com" {
		t.Errorf("webhook edge target_hostname = %q", got)
	}
	if text := driftSummaryText(reports[0]); !strings.Contains(text, "api.stripe.com [198.51.100.20:443]") {
		t.Errorf("webhook text does not name the host:\n%s", text)
	}

	// The shared rule, on annotated services.
	services := []storage.Service{
		{ID: ext, Name: ":443", DisplayName: "198.51.100.20"},
		{ID: "default/payments", Name: "payments", DisplayName: "default/payments"},
	}
	conns := []storage.Connection{
		{ID: "a", SourceID: "api", TargetID: ext, Hostnames: storage.ObservedHostnames(time.Now(), "api.stripe.com")},
		{ID: "b", SourceID: "api", TargetID: "default/payments", Hostnames: storage.ObservedHostnames(time.Now(), "payments.default.svc.cluster.local")},
	}
	AnnotateHostnames(services, conns)
	if got := ServiceHostname(&services[0]); got != "api.stripe.com" {
		t.Errorf("ServiceHostname(external) = %q", got)
	}
	if got := DriftDisplayName(&services[0]); got != "api.stripe.com" {
		t.Errorf("DriftDisplayName(external) = %q", got)
	}
	if got := displayName(services[0]); got != "api.stripe.com" {
		t.Errorf("idle-spend displayName(external) = %q", got)
	}
	if got := ServiceHostname(&services[1]); got != "" {
		t.Errorf("a Kubernetes-resolved service must keep its name, got hostname %q", got)
	}
	if got := displayName(services[1]); got != "payments" {
		t.Errorf("idle-spend displayName(k8s) = %q", got)
	}
}
