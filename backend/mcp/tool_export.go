package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

type exportMermaidArgs struct {
	Service   string `json:"service"`
	Direction string `json:"direction"`
	Depth     int    `json:"depth"`
	At        string `json:"at"`
	MaxNodes  int    `json:"max_nodes"`
}

func exportMermaidTool() Tool {
	return Tool{
		Name:  "export_mermaid",
		Title: "Export diagram (Mermaid)",
		Description: "Render the topology as a Mermaid flowchart (services grouped by node, edges labelled with ports) for docs, PRs or ADRs - the same output as the InfraLens UI's Mermaid export. " +
			"Give a service to export just its neighbourhood (recommended for large topologies); without one the whole topology is exported if it fits in max_nodes.",
		InputSchema: obj(nil, map[string]Schema{
			"service":   str("Optional: centre the diagram on this service (ID or name) and include only services within depth hops of it."),
			"direction": enum("With service: which neighbours to include.", "both", "both", "downstream", "upstream"),
			"depth":     integer("With service: how many hops out to include.", 1, maxDepth, 2),
			"at":        atArg,
			"max_nodes": integer("Refuse (with a hint to narrow the scope) rather than render more services than this.", 1, 400, 150),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in exportMermaidArgs) (any, error) {
			maxNodes := clamp(in.MaxNodes, 150, 1, 400)
			at, err := parseOptionalTime("at", in.At, time.Now())
			if err != nil {
				return nil, err
			}
			snap, err := env.snapshot(ctx, at)
			if err != nil {
				return nil, err
			}

			out := map[string]any{"format": "mermaid", "as_of": asOf(snap)}
			topo := snap.topo
			if strings.TrimSpace(in.Service) != "" {
				var dirs []service.ImpactDirection
				switch in.Direction {
				case "", "both":
					dirs = []service.ImpactDirection{service.ImpactDownstream, service.ImpactUpstream}
				case "downstream":
					dirs = []service.ImpactDirection{service.ImpactDownstream}
				case "upstream":
					dirs = []service.ImpactDirection{service.ImpactUpstream}
				default:
					return nil, invalidArgs(`direction must be "both", "downstream" or "upstream"`)
				}
				res, err := snap.resolveService("service", in.Service)
				if err != nil {
					return nil, err
				}
				depth := clamp(in.Depth, 2, 1, maxDepth)
				if topo, err = neighbourhood(ctx, env, res.svc.ID, dirs, depth, at); err != nil {
					return nil, err
				}
				out["scope"] = map[string]any{"service": snap.mini(res.svc.ID), "direction": orDefault(in.Direction, "both"), "depth": depth}
			}

			if n := len(topo.Services); n > maxNodes {
				return nil, &ToolError{
					Code:    "too_many_nodes",
					Message: "the diagram would have more services than max_nodes; pass a service to export its neighbourhood, lower depth, or raise max_nodes (up to 400)",
					Details: map[string]any{"services": n, "max_nodes": maxNodes},
				}
			}

			out["services"] = len(topo.Services)
			out["connections"] = len(topo.Connections)
			out["mermaid"] = handlers.RenderMermaid(sanitizedForDiagram(topo))
			return out, nil
		}),
	}
}

// neighbourhood merges GetImpact subgraphs around a service.
func neighbourhood(ctx context.Context, env *Env, id string, dirs []service.ImpactDirection, depth int, at time.Time) (*storage.Topology, error) {
	merged := &storage.Topology{}
	seenSvc, seenConn := map[string]bool{}, map[string]bool{}
	for _, dir := range dirs {
		sub, err := env.Topology.GetImpact(ctx, id, dir, depth, at)
		if err != nil {
			return nil, err
		}
		if sub == nil {
			continue
		}
		for _, svc := range sub.Services {
			if !seenSvc[svc.ID] {
				seenSvc[svc.ID] = true
				merged.Services = append(merged.Services, svc)
			}
		}
		for _, c := range sub.Connections {
			if !seenConn[c.ID] {
				seenConn[c.ID] = true
				merged.Connections = append(merged.Connections, c)
			}
		}
	}
	return merged, nil
}

// sanitizedForDiagram copies a topology with the observed strings the
// renderer prints (names, tech, node) cleaned, so a crafted service name
// cannot inject Mermaid syntax across lines.
func sanitizedForDiagram(t *storage.Topology) *storage.Topology {
	out := &storage.Topology{Services: make([]storage.Service, len(t.Services)), Connections: t.Connections}
	for i, svc := range t.Services {
		svc.Name = diagramText(svc.Name)
		svc.DisplayName = diagramText(svc.DisplayName)
		svc.Tech = diagramText(svc.Tech)
		svc.Node = diagramText(svc.Node)
		out.Services[i] = svc
	}
	return out
}

func diagramText(s string) string {
	s = sanitizeString(s)
	return strings.NewReplacer("[", "(", "]", ")", "{", "(", "}", ")", "`", "'", "|", "/", "<", "‹", ">", "›").Replace(s)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
