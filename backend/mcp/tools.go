package mcp

// BuiltinTools is the tool set served by NewHandler, in the order tools/list
// returns them (the spec asks for a deterministic order: it lets clients and
// LLM prompt caches reuse the list).
//
// Adding a tool:
//
//  1. Write a constructor returning a Tool in a new file, e.g.
//     tool_drift.go with `func driftEventsTool() Tool`, using
//     Typed[yourArgs](...) for decoded arguments and the obj/str/integer
//     helpers for the schema. Look at findOrphansTool for the smallest example.
//  2. Append it below. If it needs a dependency that is not on
//     *service.TopologyService, add a field to Deps, set it where the handler
//     is built in api/mcp.go (newMCPEndpoint), and register the tool only when it is non-nil.
//  3. Add a case to TestToolsAgainstSeededStore (tools_test.go).
func BuiltinTools(deps Deps) []Tool {
	tools := []Tool{
		topologyOverviewTool(),
		searchServicesTool(),
		getServiceTool(),
		getDependenciesTool(),
		getBlastRadiusTool(),
		listConnectionsTool(),
		diffTopologyTool(),
		rankCriticalityTool(),
		findOrphansTool(),
		findStaleServicesTool(),
		exportMermaidTool(),
	}
	// Optional, dependency-gated tools go here, e.g.:
	//
	//	if deps.IdleCost != nil {
	//		tools = append(tools, idleCostTool())
	//	}
	_ = deps
	return tools
}

// Shared argument schemas, so every tool describes the same concept the same
// way.
var (
	serviceArg = str(`Service to look up: an exact service ID (e.g. "10.0.1.10/nginx") or a name, display name, IP or hostname (case-insensitive). If several services match, the tool returns the candidates instead of guessing.`)
	atArg      = str("Optional point in time to answer for instead of now. Requires topology history. Accepts " + timeArgHelp + ".")
)

func limitArg(def, max int) Schema {
	return integer("Maximum number of items to return.", 1, max, def)
}

var offsetArg = integer("Number of items to skip, for paging; pass the previous response's next_offset.", 0, 1_000_000, 0)

// clamp bounds an integer argument, substituting def when it is unset (0).
func clamp(v, def, min, max int) int {
	if v == 0 {
		v = def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// pageOf returns the [offset, offset+limit) window of n items plus paging
// fields for the response.
func pageOf(n, offset, limit int) (lo, hi int, info map[string]any) {
	if offset < 0 {
		offset = 0
	}
	lo = offset
	if lo > n {
		lo = n
	}
	hi = lo + limit
	if hi > n {
		hi = n
	}
	info = map[string]any{"total": n, "offset": offset, "returned": hi - lo}
	if hi < n {
		info["next_offset"] = hi
	}
	return lo, hi, info
}
