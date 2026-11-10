package handlers_test

import (
	"strings"
	"testing"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/storage"
)

// A service with no name is labelled with its raw ID, and IDs, names and node
// names come from observed traffic. Crafted values must not be able to end a
// label and start a new Mermaid statement such as a click directive.
func TestRenderMermaidNeutralisesInjectedStatements(t *testing.T) {
	topo := &storage.Topology{Services: []storage.Service{
		{ID: "10.0.0.1/x\"]\n    click svc_x \"https://evil.example\"\n%%", Node: "n1"},
		{ID: "10.0.0.2/y", Name: "y\r\nclick svc_y \"https://evil.example\"", Node: "n2\u2028click n \"https://evil.example\""},
	}}
	out := handlers.RenderMermaid(topo)

	nodeLines := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "click") || strings.HasPrefix(l, "%%") {
			t.Errorf("injected statement survived as its own line: %q", line)
		}
		if strings.HasPrefix(l, "svc_") {
			nodeLines++
		}
	}
	if nodeLines != 2 {
		t.Errorf("each service must render on exactly one line, got %d:\n%s", nodeLines, out)
	}
	if strings.ContainsAny(out, "\r\u2028") {
		t.Error("line separators must not survive into the diagram")
	}
	if !strings.Contains(out, "#quot;") {
		t.Error("quotes inside labels must still be escaped")
	}
}
