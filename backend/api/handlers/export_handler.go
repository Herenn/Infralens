package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// ExportHandler renders the topology as text-based graph formats
// (Mermaid flowchart or Graphviz DOT) for embedding in docs and PRs.
type ExportHandler struct {
	topology *service.TopologyService
}

// NewExportHandler creates a new export handler.
func NewExportHandler(topology *service.TopologyService) *ExportHandler {
	return &ExportHandler{topology: topology}
}

// HandleExport handles GET /api/v1/topology/export?format=mermaid|dot
func (h *ExportHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	topology, err := h.topology.GetTopology(r.Context())
	if err != nil {
		http.Error(w, "Failed to get topology", http.StatusInternalServerError)
		return
	}

	var out string
	var filename string
	switch format {
	case "mermaid", "":
		out = renderMermaid(topology)
		filename = "infralens-topology.mmd"
	case "dot", "graphviz":
		out = renderDOT(topology)
		filename = "infralens-topology.dot"
	default:
		http.Error(w, "Unsupported format (use: mermaid, dot)", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	fmt.Fprint(w, out)
}

// nodeLabel returns a human-friendly label for a service.
func nodeLabel(svc storage.Service) string {
	label := svc.DisplayName
	if label == "" {
		label = svc.Name
	}
	if label == "" {
		label = svc.ID
	}
	if svc.Tech != "" && !strings.EqualFold(svc.Tech, label) {
		label = fmt.Sprintf("%s (%s)", label, svc.Tech)
	}
	return label
}

// groupServicesByNode groups services by node with stable ordering.
func groupServicesByNode(services []storage.Service) (map[string][]storage.Service, []string) {
	groups := make(map[string][]storage.Service)
	for _, svc := range services {
		node := svc.Node
		if node == "" {
			node = "External Network"
		}
		groups[node] = append(groups[node], svc)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return groups, names
}

// mermaidID sanitizes a service ID into a valid Mermaid node identifier.
func mermaidID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return "svc_" + b.String()
}

// renderMermaid renders the topology as a Mermaid flowchart, with services
// grouped into subgraphs per node.
func renderMermaid(t *storage.Topology) string {
	var b strings.Builder
	b.WriteString("flowchart LR\n")

	groups, nodeNames := groupServicesByNode(t.Services)
	for i, nodeName := range nodeNames {
		fmt.Fprintf(&b, "    subgraph node%d [\"%s\"]\n", i, escapeMermaid(nodeName))
		svcs := groups[nodeName]
		sort.Slice(svcs, func(a, c int) bool { return svcs[a].ID < svcs[c].ID })
		for _, svc := range svcs {
			fmt.Fprintf(&b, "        %s[\"%s\"]\n", mermaidID(svc.ID), escapeMermaid(nodeLabel(svc)))
		}
		b.WriteString("    end\n")
	}

	conns := append([]storage.Connection(nil), t.Connections...)
	sort.Slice(conns, func(a, c int) bool { return conns[a].ID < conns[c].ID })
	for _, conn := range conns {
		label := fmt.Sprintf(":%d", conn.Port)
		if conn.Protocol == "udp" {
			label += "/udp"
		}
		fmt.Fprintf(&b, "    %s -->|\"%s\"| %s\n",
			mermaidID(conn.SourceID), escapeMermaid(label), mermaidID(conn.TargetID))
	}

	return b.String()
}

func escapeMermaid(s string) string {
	return strings.ReplaceAll(s, "\"", "#quot;")
}

// renderDOT renders the topology as a Graphviz digraph, with services
// grouped into clusters per node.
func renderDOT(t *storage.Topology) string {
	var b strings.Builder
	b.WriteString("digraph infralens {\n")
	b.WriteString("    rankdir=LR;\n")
	b.WriteString("    node [shape=box, style=rounded];\n\n")

	groups, nodeNames := groupServicesByNode(t.Services)
	for i, nodeName := range nodeNames {
		fmt.Fprintf(&b, "    subgraph cluster_%d {\n", i)
		fmt.Fprintf(&b, "        label=%q;\n", nodeName)
		svcs := groups[nodeName]
		sort.Slice(svcs, func(a, c int) bool { return svcs[a].ID < svcs[c].ID })
		for _, svc := range svcs {
			fmt.Fprintf(&b, "        %q [label=%q];\n", svc.ID, nodeLabel(svc))
		}
		b.WriteString("    }\n\n")
	}

	conns := append([]storage.Connection(nil), t.Connections...)
	sort.Slice(conns, func(a, c int) bool { return conns[a].ID < conns[c].ID })
	for _, conn := range conns {
		label := fmt.Sprintf(":%d", conn.Port)
		attrs := fmt.Sprintf("label=%q", label)
		if conn.Protocol == "udp" {
			attrs += ", style=dashed"
		}
		fmt.Fprintf(&b, "    %q -> %q [%s];\n", conn.SourceID, conn.TargetID, attrs)
	}

	b.WriteString("}\n")
	return b.String()
}
