package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// ---------------------------------------------------------------------------
// diff_topology
// ---------------------------------------------------------------------------

type diffTopologyArgs struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Limit int    `json:"limit"`
}

func diffTopologyTool() Tool {
	return Tool{
		Name:  "diff_topology",
		Title: "What changed between two times",
		Description: "Compare the topology at two points in time and list what appeared and what disappeared: services and connections (dependencies). " +
			`Answers "what changed in the architecture since last Tuesday?" - e.g. from="7d" (to defaults to now). Requires topology history.`,
		InputSchema: obj([]string{"from"}, map[string]Schema{
			"from":  str("Start of the comparison. Accepts " + timeArgHelp + "."),
			"to":    str(`End of the comparison; defaults to "now". Same formats as from.`),
			"limit": limitArg(50, 200),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in diffTopologyArgs) (any, error) {
			now := time.Now()
			from, err := parseTimeArg("from", in.From, now)
			if err != nil {
				return nil, err
			}
			to := now
			if strings.TrimSpace(in.To) != "" {
				if to, err = parseTimeArg("to", in.To, now); err != nil {
					return nil, err
				}
			}
			if to.Before(from) {
				return nil, invalidArgs("to (%s) is before from (%s); the diff is directional, so swap them", formatTime(to), formatTime(from))
			}
			limit := clamp(in.Limit, 50, 1, 200)

			diff, err := env.Topology.GetTopologyDiff(ctx, from, to)
			if err != nil {
				return nil, err
			}

			// Names for edge endpoints: the diff's own services first (they
			// may no longer exist), then the current topology.
			names, err := env.snapshot(ctx, time.Time{})
			if err != nil {
				return nil, err
			}
			hist := &snapshot{at: to, topo: &storage.Topology{}, byID: map[string]*storage.Service{}, raw: map[string]map[string]any{}}
			for _, list := range [][]storage.Service{diff.AddedServices, diff.RemovedServices} {
				for i := range list {
					svc := &list[i]
					hist.byID[svc.ID] = svc
					hist.raw[svc.ID] = toMap(svc)
				}
			}
			for id, svc := range names.byID {
				if _, ok := hist.byID[id]; !ok {
					hist.byID[id] = svc
				}
			}

			out := map[string]any{
				"from": formatTime(from),
				"to":   formatTime(to),
				"summary": map[string]any{
					"added_services":      len(diff.AddedServices),
					"removed_services":    len(diff.RemovedServices),
					"added_connections":   len(diff.AddedConnections),
					"removed_connections": len(diff.RemovedConnections),
				},
				"added_services":      hist.serviceList(diff.AddedServices, limit),
				"removed_services":    hist.serviceList(diff.RemovedServices, limit),
				"added_connections":   hist.connectionList(diff.AddedConnections, limit),
				"removed_connections": hist.connectionList(diff.RemovedConnections, limit),
			}

			var warnings []string
			if bounds, ok, err := env.Topology.GetHistoryBounds(ctx); err == nil {
				if !ok {
					warnings = append(warnings, "no topology history has been recorded yet, so the diff is necessarily empty")
				} else if from.Before(bounds.Earliest) {
					warnings = append(warnings, "from is earlier than the oldest recorded history ("+formatTime(bounds.Earliest)+"); anything that already existed then is reported as added")
				}
			}
			if len(warnings) > 0 {
				out["warnings"] = warnings
			}
			return out, nil
		}),
	}
}

func (s *snapshot) serviceList(list []storage.Service, limit int) map[string]any {
	ptrs := make([]*storage.Service, 0, len(list))
	for i := range list {
		ptrs = append(ptrs, &list[i])
	}
	sortServices(ptrs)
	if len(ptrs) > limit {
		ptrs = ptrs[:limit]
	}
	items := make([]map[string]any, 0, len(ptrs))
	for _, svc := range ptrs {
		items = append(items, briefOf(toMap(svc), true))
	}
	return map[string]any{"total": len(list), "returned": len(items), "items": items}
}

func (s *snapshot) connectionList(list []storage.Connection, limit int) map[string]any {
	n := len(list)
	if n > limit {
		list = list[:limit]
	}
	items := make([]map[string]any, 0, len(list))
	for i := range list {
		items = append(items, s.edgeItem(&list[i]))
	}
	return map[string]any{"total": n, "returned": len(items), "items": items}
}

// ---------------------------------------------------------------------------
// find_stale_services
// ---------------------------------------------------------------------------

type findStaleServicesArgs struct {
	OlderThan string `json:"older_than"`
	Limit     int    `json:"limit"`
}

func findStaleServicesTool() Tool {
	return Tool{
		Name:  "find_stale_services",
		Title: "Decommission candidates",
		Description: "List services that used to be observed but have not been seen for a while - candidates for decommissioning or cleanup - oldest first, with when each was last seen. " +
			"Requires topology history. For services that are present but unconnected, use find_orphans.",
		InputSchema: obj(nil, map[string]Schema{
			"older_than": str(`Only services unseen for at least this long, e.g. "7d", "48h", "2w". Defaults to the server's threshold (up to 7 days, bounded by history retention).`),
			"limit":      limitArg(50, 200),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in findStaleServicesArgs) (any, error) {
			var olderThan time.Duration
			if strings.TrimSpace(in.OlderThan) != "" {
				d, err := parseFlexibleDuration(in.OlderThan)
				if err != nil || d <= 0 {
					return nil, invalidArgs(`older_than %q is not a positive duration such as "7d", "48h" or "2w"`, sanitizeString(in.OlderThan))
				}
				olderThan = d
			}
			limit := clamp(in.Limit, 50, 1, 200)

			// One extra row tells us whether there are more than limit.
			stale, err := env.Topology.GetStaleServices(ctx, olderThan, limit+1)
			if err != nil {
				return nil, err
			}
			hasMore := len(stale) > limit
			if hasMore {
				stale = stale[:limit]
			}
			items := make([]map[string]any, 0, len(stale))
			for i := range stale {
				si := &stale[i]
				items = append(items, briefOf(toMap(storage.Service{
					ID: si.ServiceID, Name: si.Name, Type: si.Type, Tech: si.Tech,
					Namespace: si.Namespace, Node: si.Node, LastSeen: si.LastSeen,
				}), true))
				items[len(items)-1]["last_seen"] = formatTime(si.LastSeen)
			}
			out := map[string]any{"services": items, "returned": len(items), "has_more": hasMore}
			if olderThan > 0 {
				out["older_than"] = olderThan.String()
				out["cutoff"] = formatTime(time.Now().Add(-olderThan))
			} else {
				out["older_than"] = "server default"
			}
			return out, nil
		}),
	}
}
