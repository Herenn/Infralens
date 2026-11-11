package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// IDs are handles the model passes back, so they must survive output
// shaping exactly - no truncation, no character replacement - while invisible
// characters in them still never reach the wire unescaped.
func TestServiceIDsRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	prefix := strings.Repeat("a", 245)
	longA, longB := "10.9.9.9/"+prefix+"-one", "10.9.9.9/"+prefix+"-two"
	rlo, zwsp := string(rune(0x202e)), string(rune(0x200b))
	ctl := "10.0.0.9/ctl\x1b[0m" + rlo + "evil" + zwsp + ""
	for _, id := range []string{longA, longB, ctl} {
		if err := f.topology.AddOrUpdateService(ctx, &storage.Service{ID: id, Node: "node-z", LastSeen: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	out := f.mustCall(t, "search_services", map[string]any{"node": "node-z"})
	got := ids(out["services"])
	for _, want := range []string{longA, longB, ctl} {
		if !has(got, want) {
			t.Errorf("search_services lost or altered ID %q; got %q", want, got)
		}
	}
	for _, id := range []string{longA, longB, ctl} {
		res := f.mustCall(t, "get_service", map[string]any{"service": id})
		if res["matched_by"] != "id" || res["service"].(map[string]any)["id"] != id {
			t.Errorf("ID %q did not round-trip: matched_by=%v id=%q", id, res["matched_by"], res["service"].(map[string]any)["id"])
		}
	}

	// On the wire, the control/bidi/zero-width characters are \u escapes.
	params, _ := json.Marshal(map[string]any{"name": "get_service", "arguments": map[string]any{"service": ctl}})
	res, perr := f.handler.callTool(context.Background(), params)
	if perr != nil {
		t.Fatal(perr.Message)
	}
	text := res["content"].([]textContent)[0].Text
	if strings.ContainsAny(text, "\x1b"+rlo+zwsp) {
		t.Errorf("invisible characters reached the wire raw: %q", text)
	}
	if !strings.Contains(text, `\u202e`) || !strings.Contains(text, `\u200b`) {
		t.Errorf("expected \\u escapes in %q", text)
	}
}

// TestPastTimeQueryOnNonUTCHost forces a non-UTC time.Local, so the SQLite
// host is in +03:00 while the model asks in UTC ("...Z").
// The SQLite store now writes and compares every time as UTC (see dbTime in
// storage/sqlite/timestamps.go), so the UTC instant is passed through as is;
// under TZ=UTC in CI this would otherwise pass vacuously.
func TestPastTimeQueryOnNonUTCHost(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("+03", 3*60*60) // named like a real zone abbreviation; the SQLite driver parses it back
	t.Cleanup(func() { time.Local = saved })

	f := newFixture(t)
	at := f.t0.Add(time.Second).UTC().Format(time.RFC3339)
	if !strings.HasSuffix(at, "Z") {
		t.Fatalf("test needs a UTC timestamp, got %s", at)
	}
	got := ids(f.mustCall(t, "search_services", map[string]any{"at": at})["services"])
	if len(got) != 1 || got[0] != idLegacy {
		t.Errorf("services at %s on a UTC+3 host = %v, want only legacy-batch", at, got)
	}
}

func TestHugeRelativeDurationsAreRejected(t *testing.T) {
	now := time.Now()
	for _, in := range []string{"99999999999999w", "11y", "3651d", "1e30h", "522w", "87601h"} {
		if v, err := parseTimeArg("at", in, now); err == nil {
			t.Errorf("parseTimeArg(%q) = %s, want an error", in, v)
		}
	}
	if _, err := parseTimeArg("at", "3650d", now); err != nil {
		t.Errorf("10 years should still be accepted: %v", err)
	}
	f := newFixture(t)
	e := f.mustFail(t, "diff_topology", map[string]any{"from": "99999999999999w"}, "invalid_arguments")
	if !strings.Contains(e["message"].(string), "too far in the past") {
		t.Errorf("message = %v", e["message"])
	}
}

// A service with no name is labelled by its raw ID in the diagram; a crafted
// ID must not inject Mermaid statements through the MCP export either.
func TestExportMermaidIDLabelInjection(t *testing.T) {
	f := newFixture(t)
	evilID := "10.6.6.6/x\"]\n    click svc_x \"https://evil.example\"\n%%"
	if err := f.topology.AddOrUpdateService(context.Background(), &storage.Service{ID: evilID, Node: "node-q", LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	m := f.mustCall(t, "export_mermaid", nil)["mermaid"].(string)
	for _, line := range strings.Split(m, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "click") || strings.HasPrefix(l, "%%") {
			t.Errorf("injected statement in MCP Mermaid export: %q", line)
		}
	}
}
