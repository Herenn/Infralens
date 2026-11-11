package mcp

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// ---------------------------------------------------------------------------
// search_services
// ---------------------------------------------------------------------------

type searchServicesArgs struct {
	Query     string `json:"query"`
	Namespace string `json:"namespace"`
	Node      string `json:"node"`
	Type      string `json:"type"`
	At        string `json:"at"`
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
}

func searchServicesTool() Tool {
	return Tool{
		Name:  "search_services",
		Title: "Search services",
		Description: "Find services by name, ID, IP, hostname, technology or type (case-insensitive substring), optionally filtered by namespace, node or type. " +
			"With no query, lists all services. Results are sorted by name and paginated; use the returned IDs with the other tools.",
		InputSchema: obj(nil, map[string]Schema{
			"query":     str(`Substring to match against service ID, names, IP, hostname, technology and type, e.g. "payments", "postgres", "10.0.3.".`),
			"namespace": str("Only services in this Kubernetes namespace (exact match)."),
			"node":      str(`Only services on this node/host (exact match). Services outside any node (external endpoints) have node "".`),
			"type":      str(`Only services of this type (exact match), e.g. "database", "cache", "web_server", "application", "message_queue".`),
			"at":        atArg,
			"limit":     limitArg(25, 100),
			"offset":    offsetArg,
		}),
		Run: Typed(func(ctx context.Context, env *Env, in searchServicesArgs) (any, error) {
			at, err := parseOptionalTime("at", in.At, time.Now())
			if err != nil {
				return nil, err
			}
			snap, err := env.snapshot(ctx, at)
			if err != nil {
				return nil, err
			}
			needle := strings.ToLower(strings.TrimSpace(in.Query))
			var matches []*storage.Service
			for _, svc := range snap.sortedServices() {
				if in.Namespace != "" && svc.Namespace != in.Namespace ||
					in.Node != "" && svc.Node != in.Node ||
					in.Type != "" && svc.Type != in.Type {
					continue
				}
				if needle != "" && !matchesQuery(snap.raw[svc.ID], svc, needle) {
					continue
				}
				matches = append(matches, svc)
			}
			lo, hi, out := pageOf(len(matches), in.Offset, clamp(in.Limit, 25, 1, 100))
			items := make([]map[string]any, 0, hi-lo)
			for _, svc := range matches[lo:hi] {
				items = append(items, snap.brief(svc.ID))
			}
			out["services"] = items
			out["as_of"] = asOf(snap)
			return out, nil
		}),
	}
}

func matchesQuery(raw map[string]any, svc *storage.Service, needle string) bool {
	for _, v := range append(nameValues(raw), svc.ID, svc.Tech, svc.Type) {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}

// asOf labels which moment a result describes.
func asOf(snap *snapshot) string {
	if snap.historical() {
		return formatTime(snap.at)
	}
	return "now"
}

// ---------------------------------------------------------------------------
// get_service
// ---------------------------------------------------------------------------

type getServiceArgs struct {
	Service string `json:"service"`
	At      string `json:"at"`
}

// maxEdgesPerDirection caps the direct dependencies/dependents get_service
// lists; the totals are always reported.
const maxEdgesPerDirection = 50

func getServiceTool() Tool {
	return Tool{
		Name:  "get_service",
		Title: "Get service details",
		Description: "Everything known about one service: identity, type and detected technology, node and namespace, labels, process fingerprint (when the agent inspected it and the caller authenticated with a key), " +
			"its direct dependencies (services it connects to, with ports) and direct dependents (services that connect to it), and its blast-radius rank. " +
			"For transitive dependencies use get_dependencies or get_blast_radius.",
		InputSchema: obj([]string{"service"}, map[string]Schema{
			"service": serviceArg,
			"at":      atArg,
		}),
		Run: Typed(func(ctx context.Context, env *Env, in getServiceArgs) (any, error) {
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
			id := res.svc.ID

			var outbound, inbound []*storage.Connection
			for i := range snap.topo.Connections {
				c := &snap.topo.Connections[i]
				if c.SourceID == id {
					outbound = append(outbound, c)
				}
				if c.TargetID == id {
					inbound = append(inbound, c)
				}
			}

			out := map[string]any{
				"service":      snap.serviceView(id),
				"matched_by":   res.matchedBy,
				"as_of":        asOf(snap),
				"dependencies": snap.edgeList(outbound, func(c *storage.Connection) string { return c.TargetID }),
				"dependents":   snap.edgeList(inbound, func(c *storage.Connection) string { return c.SourceID }),
			}

			if !snap.historical() {
				if crit, err := criticalityOf(ctx, env, id); err == nil && crit != nil {
					out["criticality"] = crit
				}
				// The fingerprint (process, ports, version banners) is not served
				// by any public REST endpoint, so it is only returned to callers
				// that authenticated with a key - never in open/no-auth mode.
				if env.Inspections != nil && isAuthenticated(ctx) {
					if insp, err := env.Inspections.GetInspection(ctx, id); err == nil && insp != nil {
						if fp := fingerprint(insp); len(fp) > 0 {
							out["process_fingerprint"] = fp
						}
					}
				}
			}
			return out, nil
		}),
	}
}

// edgeList renders one direction of a service's direct edges, peers sorted by
// name, capped at maxEdgesPerDirection.
func (s *snapshot) edgeList(conns []*storage.Connection, peer func(*storage.Connection) string) map[string]any {
	sort.Slice(conns, func(i, j int) bool {
		a, b := strings.ToLower(s.sortName(peer(conns[i]))), strings.ToLower(s.sortName(peer(conns[j])))
		if a != b {
			return a < b
		}
		return conns[i].Port < conns[j].Port
	})
	n := len(conns)
	shown := conns
	if n > maxEdgesPerDirection {
		shown = conns[:maxEdgesPerDirection]
	}
	items := make([]map[string]any, 0, len(shown))
	for _, c := range shown {
		item := map[string]any{"service": s.brief(peer(c))}
		for k, v := range s.edgeFacts(c) {
			item[k] = v
		}
		items = append(items, item)
	}
	return map[string]any{"total": n, "returned": len(items), "items": items}
}

func (s *snapshot) sortName(id string) string {
	if svc, ok := s.byID[id]; ok {
		return displayName(svc)
	}
	return id
}

// edgeFacts is the per-connection detail shared by every edge rendering.
func (s *snapshot) edgeFacts(c *storage.Connection) map[string]any {
	out := map[string]any{"port": c.Port}
	if c.Protocol != "" {
		out["protocol"] = c.Protocol
	}
	if !c.LastSeen.IsZero() {
		out["last_seen"] = formatTime(c.LastSeen)
	}
	if !s.historical() {
		if rate := c.BytesSentRate + c.BytesRecvRate; rate > 0 {
			out["bytes_per_sec"] = math.Round(rate)
		}
	}
	return out
}

// criticalityOf reports a service's blast radius and its rank among all
// services, from the memoized criticality ranking.
func criticalityOf(ctx context.Context, env *Env, id string) (map[string]any, error) {
	ranked, err := env.Topology.GetCriticality(ctx, math.MaxInt32)
	if err != nil {
		return nil, err
	}
	for i, sc := range ranked {
		if sc.Service.ID == id {
			return map[string]any{
				"blast_radius": sc.BlastRadius,
				"rank":         i + 1,
				"of":           len(ranked),
				"meaning":      "blast_radius = number of other services that transitively depend on this one (would be affected if it went down); rank 1 = most critical",
			}, nil
		}
	}
	return nil, nil
}

// fingerprint is the safe subset of an agent inspection record. Command
// lines, environment variable names, config file paths and source code
// context are deliberately left out: they routinely carry secrets or bulk,
// and none of them answers an architecture question better than this does.
func fingerprint(insp *storage.ServiceInspection) map[string]any {
	out := map[string]any{}
	if insp.ProcessName != "" {
		out["process_name"] = insp.ProcessName
	}
	var ports []int
	if insp.ListenPorts != "" && json.Unmarshal([]byte(insp.ListenPorts), &ports) == nil && len(ports) > 0 {
		if len(ports) > 20 {
			ports = ports[:20]
		}
		out["listen_ports"] = ports
	}
	var db struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	}
	if insp.DBInfo != "" && json.Unmarshal([]byte(insp.DBInfo), &db) == nil && db.Type != "" {
		out["database"] = map[string]any{"type": db.Type, "version": db.Version}
	}
	var httpInfo struct {
		ServerHeader string `json:"server_header"`
		PoweredBy    string `json:"x_powered_by"`
	}
	if insp.HTTPInfo != "" && json.Unmarshal([]byte(insp.HTTPInfo), &httpInfo) == nil {
		if httpInfo.ServerHeader != "" {
			out["http_server"] = httpInfo.ServerHeader
		}
		if httpInfo.PoweredBy != "" {
			out["http_powered_by"] = httpInfo.PoweredBy
		}
	}
	var k8s struct {
		PodName   string `json:"pod_name"`
		Namespace string `json:"namespace"`
	}
	if insp.K8sMetadata != "" && json.Unmarshal([]byte(insp.K8sMetadata), &k8s) == nil {
		if k8s.PodName != "" {
			out["pod_name"] = k8s.PodName
		}
		if k8s.Namespace != "" {
			out["pod_namespace"] = k8s.Namespace
		}
	}
	if !insp.InspectedAt.IsZero() && len(out) > 0 {
		out["inspected_at"] = formatTime(insp.InspectedAt)
	}
	cleaned, _ := sanitizeValue(toMap(out)).(map[string]any)
	return cleaned
}
