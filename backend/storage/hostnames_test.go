package storage

import (
	"fmt"
	"testing"
	"time"
)

var hostT0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func names(hs []Hostname) string { return fmt.Sprint(HostnameNames(hs)) }

func TestMergeHostnamesOrdersByRecency(t *testing.T) {
	older := []Hostname{{"a.example", hostT0.Add(-time.Hour)}, {"b.example", hostT0.Add(-2 * time.Hour)}}
	got := MergeHostnames(ObservedHostnames(hostT0, "b.example"), older, hostT0)
	if names(got) != "[b.example a.example]" {
		t.Errorf("re-observed name not first: %v", got)
	}
	if !got[0].LastSeen.Equal(hostT0) {
		t.Errorf("last-seen not refreshed: %v", got[0])
	}
}

// A re-observation within HostnameRefresh is not a change, so steady
// traffic does not rewrite the row on every report.
func TestMergeHostnamesRefreshGranularity(t *testing.T) {
	older := []Hostname{{"a.example", hostT0}}
	got := MergeHostnames(ObservedHostnames(hostT0.Add(HostnameRefresh-time.Second), "a.example"), older, hostT0)
	if !EqualHostnames(got, older) {
		t.Errorf("sub-granularity refresh changed the list: %v", got)
	}
	got = MergeHostnames(ObservedHostnames(hostT0.Add(HostnameRefresh), "a.example"), older, hostT0)
	if EqualHostnames(got, older) {
		t.Error("refresh past the granularity was ignored")
	}
}

// Names age out (review: a stale or forged name used to stay the primary
// label indefinitely).
func TestMergeHostnamesExpires(t *testing.T) {
	older := []Hostname{{"stale.example", hostT0.Add(-HostnameMaxAge - time.Second)}, {"fresh.example", hostT0.Add(-time.Hour)}}
	if got := MergeHostnames(nil, older, hostT0); names(got) != "[fresh.example]" {
		t.Errorf("expiry: %v", got)
	}
	if got := MergeHostnames(nil, older[:1], hostT0); got != nil {
		t.Errorf("all expired must be nil, got %v", got)
	}
	// The zero time disables expiry (history reads).
	if got := MergeHostnames(nil, older, time.Time{}); len(got) != 2 {
		t.Errorf("zero now expired names: %v", got)
	}
}

func TestMergeHostnamesCapAndEmpty(t *testing.T) {
	if MergeHostnames(nil, nil, hostT0) != nil || MergeHostnames([]Hostname{{"", hostT0}}, nil, hostT0) != nil {
		t.Error("empty input must be nil")
	}
	var many []Hostname
	for i := 0; i < MaxHostnames+5; i++ {
		many = append(many, Hostname{fmt.Sprintf("h%02d.example", i), hostT0.Add(-time.Duration(i) * time.Minute)})
	}
	got := MergeHostnames(ObservedHostnames(hostT0.Add(time.Hour), "new.example"), many, hostT0)
	if len(got) != MaxHostnames || got[0].Name != "new.example" {
		t.Errorf("cap/order broken: %v", got)
	}
}

func TestEncodeDecodeHostnames(t *testing.T) {
	in := []Hostname{{"api.stripe.com", hostT0}, {"files.stripe.com", hostT0.Add(-time.Hour)}}
	enc := EncodeHostnames(in)
	if enc != fmt.Sprintf("api.stripe.com|%d,files.stripe.com|%d", hostT0.Unix(), hostT0.Add(-time.Hour).Unix()) {
		t.Errorf("encoding = %q", enc)
	}
	if got := DecodeHostnames(enc); !EqualHostnames(got, in) {
		t.Errorf("round trip = %v", got)
	}
	if DecodeHostnames("") != nil || DecodeHostnames(",,") != nil || DecodeHostnames("a.example|x,|5,b") != nil {
		t.Error("malformed encodings must decode to nil")
	}
	if EncodeHostnames(nil) != "" {
		t.Error("nil must encode empty")
	}
}

func TestMergeConnectionIntervalsUnionsHostnames(t *testing.T) {
	rows := []ConnectionInterval{
		{ConnectionID: "c", FirstSeen: hostT0, LastSeen: hostT0.Add(time.Minute), Hostnames: []Hostname{{"old.example", hostT0}}},
		{ConnectionID: "c", FirstSeen: hostT0.Add(30 * time.Second), LastSeen: hostT0.Add(2 * time.Minute), Hostnames: []Hostname{{"new.example", hostT0.Add(time.Hour)}}},
	}
	merged := MergeConnectionIntervals(rows)
	if len(merged) != 1 {
		t.Fatalf("merged = %v", merged)
	}
	if names(merged[0].Hostnames) != "[new.example old.example]" {
		t.Errorf("hostnames = %v", merged[0].Hostnames)
	}
}
