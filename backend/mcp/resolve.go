package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Herenn/Infralens/backend/storage"
)

// snapshot is one consistent topology - current state, or the reconstruction
// at a past instant - with the lookups tools need over it. Every tool reads a
// single snapshot so that a service and its edges always come from the same
// moment.
type snapshot struct {
	at   time.Time // zero = current state
	topo *storage.Topology
	byID map[string]*storage.Service
	// raw holds each service's full JSON form, including fields this package
	// does not know about (see serviceView).
	raw map[string]map[string]any
}

func (s *snapshot) historical() bool { return !s.at.IsZero() }

// snapshot loads the topology at `at` (zero = now).
func (e *Env) snapshot(ctx context.Context, at time.Time) (*snapshot, error) {
	var (
		topo *storage.Topology
		err  error
	)
	if at.IsZero() {
		topo, err = e.Topology.GetTopology(ctx)
	} else {
		topo, err = e.Topology.GetTopologyAt(ctx, at)
	}
	if err != nil {
		return nil, err
	}
	s := &snapshot{
		at:   at,
		topo: topo,
		byID: make(map[string]*storage.Service, len(topo.Services)),
		raw:  make(map[string]map[string]any, len(topo.Services)),
	}
	for i := range topo.Services {
		svc := &topo.Services[i]
		s.byID[svc.ID] = svc
		s.raw[svc.ID] = toMap(svc)
	}
	return s, nil
}

// sortedServices returns the snapshot's services ordered by name then ID, the
// stable order every list in this package uses.
func (s *snapshot) sortedServices() []*storage.Service {
	out := make([]*storage.Service, 0, len(s.topo.Services))
	for i := range s.topo.Services {
		out = append(out, &s.topo.Services[i])
	}
	sortServices(out)
	return out
}

func sortServices(list []*storage.Service) {
	sort.Slice(list, func(i, j int) bool {
		a, b := strings.ToLower(displayName(list[i])), strings.ToLower(displayName(list[j]))
		if a != b {
			return a < b
		}
		return list[i].ID < list[j].ID
	})
}

func displayName(svc *storage.Service) string {
	if svc.DisplayName != "" {
		return svc.DisplayName
	}
	if svc.Name != "" {
		return svc.Name
	}
	return svc.ID
}

// ---------------------------------------------------------------------------
// Service resolution
// ---------------------------------------------------------------------------

// maxCandidates bounds the disambiguation list returned for an ambiguous or
// unknown service reference.
const maxCandidates = 10

// resolved is a service reference resolved against a snapshot.
type resolved struct {
	svc *storage.Service
	// matchedBy says how the reference matched: "id", "name" (exact,
	// case-insensitive, on a name-like field), or "partial" (unique
	// substring), so the model can tell a guess from an exact hit.
	matchedBy string
}

// resolveService turns a model-supplied reference - an exact ID like
// "10.0.1.10/nginx", or a name, display name, resolved name, IP or hostname -
// into one service. Exact ID wins; then a unique case-insensitive exact match
// on a name-like field; then a unique substring match. Anything ambiguous is
// returned as an "ambiguous_service" tool error listing the candidates rather
// than picked arbitrarily.
func (s *snapshot) resolveService(arg, ref string) (*resolved, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, invalidArgs("%s is required: a service ID or name (use search_services to find one)", arg)
	}
	if svc, ok := s.byID[ref]; ok {
		return &resolved{svc: svc, matchedBy: "id"}, nil
	}

	needle := strings.ToLower(ref)
	var exact, partial []*storage.Service
	for _, svc := range s.sortedServices() {
		isExact, isPartial := false, strings.Contains(strings.ToLower(svc.ID), needle)
		for _, v := range nameValues(s.raw[svc.ID]) {
			lv := strings.ToLower(v)
			if lv == needle {
				isExact = true
				break
			}
			if strings.Contains(lv, needle) {
				isPartial = true
			}
		}
		switch {
		case isExact:
			exact = append(exact, svc)
		case isPartial:
			partial = append(partial, svc)
		}
	}

	switch {
	case len(exact) == 1:
		return &resolved{svc: exact[0], matchedBy: "name"}, nil
	case len(exact) > 1:
		return nil, s.ambiguous(arg, ref, exact)
	case len(partial) == 1:
		return &resolved{svc: partial[0], matchedBy: "partial"}, nil
	case len(partial) > 1:
		return nil, s.ambiguous(arg, ref, partial)
	}

	where := "in the current topology"
	if s.historical() {
		where = "in the topology at " + formatTime(s.at)
	}
	return nil, &ToolError{
		Code:    "service_not_found",
		Message: "no service matches the " + arg + " argument " + where + "; use search_services to look it up",
		Details: map[string]any{"query": sanitizeString(ref)},
	}
}

func (s *snapshot) ambiguous(arg, ref string, matches []*storage.Service) error {
	candidates := make([]map[string]any, 0, maxCandidates)
	for i, svc := range matches {
		if i == maxCandidates {
			break
		}
		candidates = append(candidates, s.brief(svc.ID))
	}
	return &ToolError{
		Code:    "ambiguous_service",
		Message: "the " + arg + " argument matches more than one service; call again with one of the candidate IDs",
		Details: map[string]any{
			"query":            sanitizeString(ref),
			"total_matches":    len(matches),
			"candidates":       candidates,
			"candidates_shown": len(candidates),
		},
	}
}

// nameValues returns the values of a service's name-like fields. It reads the
// generic JSON form so fields added to storage.Service later (Dev D's
// hostnames for external IPs, for instance) are searchable without a change
// here: any field whose key contains "name" or "host", or the IP, counts.
func nameValues(raw map[string]any) []string {
	var out []string
	for k, v := range raw {
		// Namespace is excluded: "default" would otherwise match half the
		// cluster.
		if k == "id" || k == "namespace" || !(strings.Contains(k, "name") || strings.Contains(k, "host") || k == "pod_ip") {
			continue
		}
		switch tv := v.(type) {
		case string:
			if tv != "" {
				out = append(out, tv)
			}
		case []any:
			for _, e := range tv {
				if s, ok := e.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Output shaping
// ---------------------------------------------------------------------------

// Fields dropped from service output: UI-only, or bookkeeping that costs
// tokens without helping answer an architecture question.
var droppedServiceFields = map[string]bool{"icon": true, "created_at": true, "updated_at": true}

// Fields additionally dropped from the brief form used for list entries and
// edge peers.
var briefDroppedFields = map[string]bool{"labels": true, "last_seen": true, "healthy": true}

// maxLabels caps how many labels a service view carries.
const maxLabels = 12

// maxFieldRunes caps any single observed string in output.
const maxFieldRunes = 200

// serviceView is a service's full model-facing form. It starts from the
// service's own JSON encoding rather than a hand-picked struct so that new
// fields added to storage.Service by other features flow through unchanged;
// it only removes known noise and bounds observed text.
func (s *snapshot) serviceView(id string) map[string]any {
	raw, ok := s.raw[id]
	if !ok {
		return map[string]any{"id": id, "known": false}
	}
	return viewOf(raw, s.historical())
}

// viewOf shapes a service's generic JSON form; see serviceView.
func viewOf(raw map[string]any, historical bool) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if droppedServiceFields[k] || isEmptyValue(v) {
			continue
		}
		out[k] = v
	}
	if dn, ok := out["display_name"].(string); ok && dn == out["name"] {
		delete(out, "display_name") // the common case; saves tokens per entry
	}
	if historical {
		// History intervals record no health and no decorative fields;
		// servicesFromIntervals fills Healthy=true, which would be invented.
		delete(out, "healthy")
	}
	// Timestamps: one format (UTC, whole seconds) everywhere, and a zero
	// time means "not recorded" rather than the year 1.
	for k, v := range out {
		if s, ok := v.(string); ok && (strings.HasSuffix(k, "_seen") || strings.HasSuffix(k, "_at")) {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				if t.IsZero() {
					delete(out, k)
				} else {
					out[k] = formatTime(t)
				}
			}
		}
	}
	if labels, ok := out["labels"].(map[string]any); ok && len(labels) > maxLabels {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		trimmed := make(map[string]any, maxLabels)
		for _, k := range keys[:maxLabels] {
			trimmed[k] = labels[k]
		}
		out["labels"] = trimmed
		out["labels_truncated"] = len(labels) - maxLabels
	}
	clean := sanitizeValue(out).(map[string]any)
	// IDs are handles the model passes back, so they are never truncated or
	// altered: a lossy ID would not resolve, and two long IDs sharing a prefix
	// would look identical. They stay safe anyway - they are only ever JSON
	// string values, and escapeInvisible renders every control, bidi and
	// zero-width character in them as a \u escape on the wire.
	if id, ok := raw["id"].(string); ok {
		clean["id"] = id
	}
	return clean
}

// brief is the compact form for list entries and edge peers.
func (s *snapshot) brief(id string) map[string]any {
	raw, ok := s.raw[id]
	if !ok {
		return map[string]any{"id": id, "known": false}
	}
	return briefOf(raw, s.historical())
}

// briefOf is viewOf reduced to the compact form.
func briefOf(raw map[string]any, historical bool) map[string]any {
	v := viewOf(raw, historical)
	for k := range briefDroppedFields {
		delete(v, k)
	}
	delete(v, "labels_truncated")
	return v
}

// mini is the smallest reference to a service: its ID plus a readable name
// when that differs from the ID.
func (s *snapshot) mini(id string) map[string]any {
	out := map[string]any{"id": id}
	if svc, ok := s.byID[id]; ok {
		if n := displayName(svc); n != svc.ID {
			out["name"] = sanitizeString(n)
		}
	} else {
		out["known"] = false
	}
	return out
}

func toMap(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

func isEmptyValue(v any) bool {
	switch tv := v.(type) {
	case nil:
		return true
	case string:
		return tv == ""
	case map[string]any:
		return len(tv) == 0
	case []any:
		return len(tv) == 0
	}
	return false
}

// sanitizeValue bounds and cleans every string in a decoded JSON value, map
// keys included. Service names, labels and the rest are observed from the
// infrastructure, so anyone who can name a process or label a pod controls
// them; they reach the model only as JSON string values, stripped of control
// characters (no smuggled newlines or terminal escapes) and length-capped.
func sanitizeValue(v any) any {
	switch tv := v.(type) {
	case string:
		return sanitizeString(tv)
	case map[string]any:
		out := make(map[string]any, len(tv))
		for k, e := range tv {
			out[sanitizeString(k)] = sanitizeValue(e)
		}
		return out
	case []any:
		out := make([]any, len(tv))
		for i, e := range tv {
			out[i] = sanitizeValue(e)
		}
		return out
	}
	return v
}

func sanitizeString(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	clean := true
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r) {
			clean = false
			break
		}
	}
	if !clean {
		s = strings.Map(func(r rune) rune {
			switch {
			case unicode.IsControl(r), r == '\u2028', r == '\u2029':
				return ' '
			case unicode.Is(unicode.Cf, r): // zero-width and bidi controls
				return -1
			}
			return r
		}, s)
	}
	if utf8.RuneCountInString(s) > maxFieldRunes {
		runes := []rune(s)
		s = string(runes[:maxFieldRunes]) + "…"
	}
	return s
}
