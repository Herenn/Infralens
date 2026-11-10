package mcp

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// maxDepth mirrors the service layer's traversal cap (maxImpactDepth), which
// is also the depth the criticality ranking always uses.
const maxDepth = 20

// ---------------------------------------------------------------------------
// Traversal shared by get_dependencies and get_blast_radius
// ---------------------------------------------------------------------------

// traverse walks the graph from root via the service layer's GetImpact (the
// same traversal the UI's blast-radius view uses) and annotates each reached
// service with its hop distance and the ports it is reached through.
func (e *Env) traverse(ctx context.Context, snap *snapshot, root string, dir service.ImpactDirection, depth int) (map[string]any, []reached, error) {
	sub, err := e.Topology.GetImpact(ctx, root, dir, depth, snap.at)
	if err != nil {
		return nil, nil, err
	}
	if sub == nil {
		return nil, nil, &ToolError{Code: "service_not_found", Message: "the service disappeared from the topology while it was being queried; retry"}
	}

	// Re-run BFS over the (small) returned subgraph to recover distances.
	next := make(map[string][]*storage.Connection)
	for i := range sub.Connections {
		c := &sub.Connections[i]
		from := c.SourceID
		if dir == service.ImpactUpstream {
			from = c.TargetID
		}
		next[from] = append(next[from], c)
	}
	hops := map[string]int{root: 0}
	ports := map[string]map[uint16]bool{}
	frontier := []string{root}
	for d := 1; len(frontier) > 0; d++ {
		var nextFrontier []string
		for _, id := range frontier {
			for _, c := range next[id] {
				to := c.TargetID
				if dir == service.ImpactUpstream {
					to = c.SourceID
				}
				if h, seen := hops[to]; seen && h < d {
					continue
				}
				if _, seen := hops[to]; !seen {
					hops[to] = d
					nextFrontier = append(nextFrontier, to)
				}
				if ports[to] == nil {
					ports[to] = map[uint16]bool{}
				}
				ports[to][c.Port] = true
			}
		}
		frontier = nextFrontier
	}

	var list []reached
	byHops := map[string]int{}
	for id, h := range hops {
		if id == root {
			continue
		}
		var ps []int
		for p := range ports[id] {
			ps = append(ps, int(p))
		}
		sort.Ints(ps)
		list = append(list, reached{id: id, hops: h, ports: ps})
		byHops[strconv.Itoa(h)]++
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].hops != list[j].hops {
			return list[i].hops < list[j].hops
		}
		a, b := strings.ToLower(snap.sortName(list[i].id)), strings.ToLower(snap.sortName(list[j].id))
		if a != b {
			return a < b
		}
		return list[i].id < list[j].id
	})
	return map[string]any{"total": len(list), "by_hops": byHops}, list, nil
}

type reached struct {
	id    string
	hops  int
	ports []int
}

func (s *snapshot) reachedItems(list []reached, limit int) []map[string]any {
	if len(list) > limit {
		list = list[:limit]
	}
	items := make([]map[string]any, 0, len(list))
	for _, r := range list {
		items = append(items, map[string]any{"hops": r.hops, "via_ports": r.ports, "service": s.brief(r.id)})
	}
	return items
}

// ---------------------------------------------------------------------------
// get_dependencies
// ---------------------------------------------------------------------------

type getDependenciesArgs struct {
	Service   string `json:"service"`
	Direction string `json:"direction"`
	Depth     int    `json:"depth"`
	At        string `json:"at"`
	Limit     int    `json:"limit"`
}

func getDependenciesTool() Tool {
	return Tool{
		Name:  "get_dependencies",
		Title: "Dependencies and dependents",
		Description: `Walk the dependency graph from one service. direction "downstream" = what the service depends on (services it connects to), "upstream" = what depends on it (services that connect to it), "both" = both lists. ` +
			"depth=1 gives direct neighbours; larger depths follow the chain transitively. Each result carries its hop distance and the ports it is reached through.",
		InputSchema: obj([]string{"service"}, map[string]Schema{
			"service":   serviceArg,
			"direction": enum("Which way to walk.", "downstream", "downstream", "upstream", "both"),
			"depth":     integer("How many hops to follow (1 = direct neighbours only).", 1, maxDepth, 1),
			"at":        atArg,
			"limit":     limitArg(50, 200),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in getDependenciesArgs) (any, error) {
			dirs := []service.ImpactDirection{service.ImpactDownstream}
			switch in.Direction {
			case "", "downstream":
			case "upstream":
				dirs = []service.ImpactDirection{service.ImpactUpstream}
			case "both":
				dirs = []service.ImpactDirection{service.ImpactDownstream, service.ImpactUpstream}
			default:
				return nil, invalidArgs(`direction must be "downstream", "upstream" or "both"`)
			}
			depth := clamp(in.Depth, 1, 1, maxDepth)
			limit := clamp(in.Limit, 50, 1, 200)

			at, err := parseOptionalTime("at", in.At, time.Now())
			if err != nil {
				return nil, err
			}
			snap, err := env.snapshot(ctx, at)
			if err != nil {
				return nil, err
			}
			res, err := snap.resolveService("service", in.Service)
			if err != nil {
				return nil, err
			}

			out := map[string]any{
				"service":    snap.brief(res.svc.ID),
				"matched_by": res.matchedBy,
				"depth":      depth,
				"as_of":      asOf(snap),
			}
			for _, dir := range dirs {
				section, list, err := env.traverse(ctx, snap, res.svc.ID, dir, depth)
				if err != nil {
					return nil, err
				}
				section["services"] = snap.reachedItems(list, limit)
				section["returned"] = len(section["services"].([]map[string]any))
				out[string(dir)] = section
			}
			return out, nil
		}),
	}
}

// ---------------------------------------------------------------------------
// get_blast_radius
// ---------------------------------------------------------------------------

type getBlastRadiusArgs struct {
	Service string `json:"service"`
	Depth   int    `json:"depth"`
	At      string `json:"at"`
	Limit   int    `json:"limit"`
}

func getBlastRadiusTool() Tool {
	return Tool{
		Name:  "get_blast_radius",
		Title: "Blast radius (what breaks if it goes down)",
		Description: "Answer \"what breaks if this service goes down?\": every service that transitively depends on it (its upstream callers, their callers, and so on), " +
			"grouped by hop distance, with the total count. Defaults to the full depth the criticality ranking uses, so the count matches rank_criticality.",
		InputSchema: obj([]string{"service"}, map[string]Schema{
			"service": serviceArg,
			"depth":   integer("How many hops of callers to follow.", 1, maxDepth, maxDepth),
			"at":      atArg,
			"limit":   limitArg(50, 200),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in getBlastRadiusArgs) (any, error) {
			depth := clamp(in.Depth, maxDepth, 1, maxDepth)
			at, err := parseOptionalTime("at", in.At, time.Now())
			if err != nil {
				return nil, err
			}
			snap, err := env.snapshot(ctx, at)
			if err != nil {
				return nil, err
			}
			res, err := snap.resolveService("service", in.Service)
			if err != nil {
				return nil, err
			}
			section, list, err := env.traverse(ctx, snap, res.svc.ID, service.ImpactUpstream, depth)
			if err != nil {
				return nil, err
			}
			items := snap.reachedItems(list, clamp(in.Limit, 50, 1, 200))
			out := map[string]any{
				"service":      snap.brief(res.svc.ID),
				"matched_by":   res.matchedBy,
				"as_of":        asOf(snap),
				"depth":        depth,
				"blast_radius": section["total"],
				"by_hops":      section["by_hops"],
				"affected":     items,
				"returned":     len(items),
			}
			if !snap.historical() && depth == maxDepth {
				if crit, err := criticalityOf(ctx, env, res.svc.ID); err == nil && crit != nil {
					out["rank"] = crit["rank"]
					out["of"] = crit["of"]
				}
			}
			return out, nil
		}),
	}
}

// ---------------------------------------------------------------------------
// list_connections
// ---------------------------------------------------------------------------

type listConnectionsArgs struct {
	Service string `json:"service"`
	At      string `json:"at"`
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
}

func listConnectionsTool() Tool {
	return Tool{
		Name:  "list_connections",
		Title: "List connections",
		Description: "List observed connections (edges: source service -> target service on a port), optionally only those touching one service, now or at a past time. " +
			"Sorted by source then target name and paginated. Prefer get_service or get_dependencies for questions about one service's neighbours.",
		InputSchema: obj(nil, map[string]Schema{
			"service": str("Optional: only connections where this service (ID or name) is the source or target."),
			"at":      atArg,
			"limit":   limitArg(50, 200),
			"offset":  offsetArg,
		}),
		Run: Typed(func(ctx context.Context, env *Env, in listConnectionsArgs) (any, error) {
			at, err := parseOptionalTime("at", in.At, time.Now())
			if err != nil {
				return nil, err
			}
			snap, err := env.snapshot(ctx, at)
			if err != nil {
				return nil, err
			}
			var only string
			out := map[string]any{"as_of": asOf(snap)}
			if strings.TrimSpace(in.Service) != "" {
				res, err := snap.resolveService("service", in.Service)
				if err != nil {
					return nil, err
				}
				only = res.svc.ID
				out["service"] = snap.mini(only)
			}

			var conns []*storage.Connection
			for i := range snap.topo.Connections {
				c := &snap.topo.Connections[i]
				if only == "" || c.SourceID == only || c.TargetID == only {
					conns = append(conns, c)
				}
			}
			sort.Slice(conns, func(i, j int) bool {
				a, b := conns[i], conns[j]
				if x, y := strings.ToLower(snap.sortName(a.SourceID)), strings.ToLower(snap.sortName(b.SourceID)); x != y {
					return x < y
				}
				if x, y := strings.ToLower(snap.sortName(a.TargetID)), strings.ToLower(snap.sortName(b.TargetID)); x != y {
					return x < y
				}
				return a.Port < b.Port
			})

			lo, hi, page := pageOf(len(conns), in.Offset, clamp(in.Limit, 50, 1, 200))
			items := make([]map[string]any, 0, hi-lo)
			for _, c := range conns[lo:hi] {
				items = append(items, snap.edgeItem(c))
			}
			for k, v := range page {
				out[k] = v
			}
			out["connections"] = items
			return out, nil
		}),
	}
}

// edgeItem renders a connection with both endpoints as minimal references.
func (s *snapshot) edgeItem(c *storage.Connection) map[string]any {
	item := map[string]any{"source": s.mini(c.SourceID), "target": s.mini(c.TargetID)}
	for k, v := range s.edgeFacts(c) {
		item[k] = v
	}
	return item
}

// ---------------------------------------------------------------------------
// rank_criticality
// ---------------------------------------------------------------------------

type rankCriticalityArgs struct {
	Limit int `json:"limit"`
}

func rankCriticalityTool() Tool {
	return Tool{
		Name:  "rank_criticality",
		Title: "Most critical services",
		Description: "Rank services in the current topology by blast radius - how many other services transitively depend on each one - highest first. " +
			"These are the single points of failure: the services whose outage would affect the most others.",
		InputSchema: obj(nil, map[string]Schema{
			"limit": limitArg(10, 100),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in rankCriticalityArgs) (any, error) {
			ranked, err := env.Topology.GetCriticality(ctx, clamp(in.Limit, 10, 1, 100))
			if err != nil {
				return nil, err
			}
			items := make([]map[string]any, 0, len(ranked))
			for i := range ranked {
				items = append(items, map[string]any{
					"rank":         i + 1,
					"blast_radius": ranked[i].BlastRadius,
					"service":      briefOf(toMap(&ranked[i].Service), false),
				})
			}
			return map[string]any{"as_of": "now", "depth": maxDepth, "ranking": items, "returned": len(items)}, nil
		}),
	}
}

// ---------------------------------------------------------------------------
// find_orphans
// ---------------------------------------------------------------------------

type findOrphansArgs struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func findOrphansTool() Tool {
	return Tool{
		Name:  "find_orphans",
		Title: "Services nothing talks to",
		Description: "List services in the current topology with no connections at all - nothing calls them and they call nothing. " +
			"Often leftovers, misconfigurations, or services the agent has not yet seen traffic for. For services that stopped being observed, use find_stale_services.",
		InputSchema: obj(nil, map[string]Schema{
			"limit":  limitArg(50, 200),
			"offset": offsetArg,
		}),
		Run: Typed(func(ctx context.Context, env *Env, in findOrphansArgs) (any, error) {
			orphans, err := env.Topology.GetOrphanServices(ctx)
			if err != nil {
				return nil, err
			}
			list := make([]*storage.Service, 0, len(orphans))
			for i := range orphans {
				list = append(list, &orphans[i])
			}
			sortServices(list)
			lo, hi, out := pageOf(len(list), in.Offset, clamp(in.Limit, 50, 1, 200))
			items := make([]map[string]any, 0, hi-lo)
			for _, svc := range list[lo:hi] {
				items = append(items, briefOf(toMap(svc), false))
			}
			out["services"] = items
			out["as_of"] = "now"
			return out, nil
		}),
	}
}

// ---------------------------------------------------------------------------
// topology_overview
// ---------------------------------------------------------------------------

type topologyOverviewArgs struct {
	At  string `json:"at"`
	Top int    `json:"top"`
}

// overviewGroupCap bounds each breakdown (namespaces, nodes, types, tech).
const overviewGroupCap = 20

func topologyOverviewTool() Tool {
	return Tool{
		Name:  "topology_overview",
		Title: "Topology overview",
		Description: "Summarise the whole topology (now, or at a past time) without dumping it: service and connection counts, breakdowns by namespace, node, type and technology, " +
			"external endpoints, orphan count, the most connected services, the most critical services (current topology only), and the time range history covers. Start here.",
		InputSchema: obj(nil, map[string]Schema{
			"at":  atArg,
			"top": integer("How many entries to include in the most-connected and most-critical lists.", 1, 50, 10),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in topologyOverviewArgs) (any, error) {
			top := clamp(in.Top, 10, 1, 50)
			at, err := parseOptionalTime("at", in.At, time.Now())
			if err != nil {
				return nil, err
			}
			snap, err := env.snapshot(ctx, at)
			if err != nil {
				return nil, err
			}

			byNS, byNode, byType, byTech := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
			external := 0
			for _, svc := range snap.topo.Services {
				byNS[orNone(svc.Namespace)]++
				if svc.Node == "" {
					external++
				} else {
					byNode[svc.Node]++
				}
				byType[orNone(svc.Type)]++
				byTech[orNone(svc.Tech)]++
			}

			in1, out1 := map[string]int{}, map[string]int{}
			connected := map[string]bool{}
			for _, c := range snap.topo.Connections {
				_, okS := snap.byID[c.SourceID]
				_, okT := snap.byID[c.TargetID]
				if !okS || !okT {
					continue
				}
				out1[c.SourceID]++
				in1[c.TargetID]++
				connected[c.SourceID], connected[c.TargetID] = true, true
			}

			hubs := snap.sortedServices()
			sort.SliceStable(hubs, func(i, j int) bool {
				return in1[hubs[i].ID]+out1[hubs[i].ID] > in1[hubs[j].ID]+out1[hubs[j].ID]
			})
			if len(hubs) > top {
				hubs = hubs[:top]
			}
			mostConnected := make([]map[string]any, 0, len(hubs))
			for _, svc := range hubs {
				if in1[svc.ID]+out1[svc.ID] == 0 {
					break
				}
				mostConnected = append(mostConnected, map[string]any{
					"service":      snap.mini(svc.ID),
					"dependents":   in1[svc.ID],
					"dependencies": out1[svc.ID],
				})
			}

			out := map[string]any{
				"as_of":              asOf(snap),
				"services":           len(snap.topo.Services),
				"connections":        len(snap.topo.Connections),
				"external_endpoints": external,
				"orphans":            len(snap.topo.Services) - len(connected),
				"by_namespace":       topGroups(byNS),
				"by_node":            topGroups(byNode),
				"by_type":            topGroups(byType),
				"by_technology":      topGroups(byTech),
				"most_connected":     mostConnected,
				"external_note":      "external_endpoints are services observed outside any monitored node (node is empty), e.g. internet clients or third-party APIs",
			}

			if !snap.historical() {
				ranked, err := env.Topology.GetCriticality(ctx, top)
				if err != nil {
					return nil, err
				}
				crit := make([]map[string]any, 0, len(ranked))
				for _, sc := range ranked {
					if sc.BlastRadius == 0 {
						break
					}
					crit = append(crit, map[string]any{"service": snap.mini(sc.Service.ID), "blast_radius": sc.BlastRadius})
				}
				out["most_critical"] = crit
			}

			hist := map[string]any{"enabled": env.Topology.HistoryEnabled()}
			if env.Topology.HistoryEnabled() {
				if bounds, ok, err := env.Topology.GetHistoryBounds(ctx); err == nil && ok {
					hist["earliest"] = formatTime(bounds.Earliest)
					hist["latest"] = formatTime(bounds.Latest)
				}
			}
			out["history"] = hist
			return out, nil
		}),
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// topGroups renders a count map as a descending list capped at
// overviewGroupCap, folding the remainder into an "(other)" entry.
func topGroups(counts map[string]int) []map[string]any {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	out := make([]map[string]any, 0, overviewGroupCap+1)
	other, otherGroups := 0, 0
	for i, k := range keys {
		if i >= overviewGroupCap {
			other += counts[k]
			otherGroups++
			continue
		}
		out = append(out, map[string]any{"name": sanitizeString(k), "services": counts[k]})
	}
	if otherGroups > 0 {
		out = append(out, map[string]any{"name": "(other)", "groups": otherGroups, "services": other})
	}
	return out
}
