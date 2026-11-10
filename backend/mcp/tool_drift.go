package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// ---------------------------------------------------------------------------
// list_drift_events
// ---------------------------------------------------------------------------

type listDriftEventsArgs struct {
	Since        string `json:"since"`
	Until        string `json:"until"`
	Kind         string `json:"kind"`
	ExternalOnly bool   `json:"external_only"`
	Service      string `json:"service"`
	Limit        int    `json:"limit"`
	Cursor       string `json:"cursor"`
}

const (
	driftDefaultLimit = 25
	driftMaxLimit     = 100
	// driftScanCap bounds how many stored events one call reads while
	// filtering by service (a filter the store does not index), so a rare
	// service on a busy feed costs a bounded amount of work; the response
	// then says where to continue.
	driftScanCap = 2000
)

func listDriftEventsTool() Tool {
	return Tool{
		Name:  "list_drift_events",
		Title: "Architecture changes (drift feed)",
		Description: "List recorded architecture drift, newest first: dependencies (connections) that appeared for the first time, or that disappeared after being established. " +
			"Each event names the source and target (external targets by their DNS name when one was observed), the port, whether the target is on the public internet, and when the edge was first/last seen. " +
			"Use it for \"what changed recently\", \"what new internet dependencies appeared this week\", or \"what happened to service X\". For a full before/after comparison of two moments use diff_topology.",
		InputSchema: obj(nil, map[string]Schema{
			"since":         str("Only events detected at or after this time. Accepts " + timeArgHelp + ". Default: no lower bound."),
			"until":         str("Only events detected before this time. Accepts " + timeArgHelp + ". Default: now."),
			"kind":          enum("Which changes to return.", "any", "any", "appeared", "disappeared"),
			"external_only": Schema{"type": "boolean", "description": "Only changes whose target is on the public internet.", "default": false},
			"service":       str("Only changes where this service is the source or the target: an exact service ID, or a name (also matches services that no longer exist, by the name recorded in the event)."),
			"limit":         limitArg(driftDefaultLimit, driftMaxLimit),
			"cursor":        str("Continue a listing: pass the previous response's next_cursor."),
		}),
		Run: Typed(func(ctx context.Context, env *Env, in listDriftEventsArgs) (any, error) {
			now := time.Now()
			filter := storage.DriftEventFilter{}
			var err error
			if strings.TrimSpace(in.Since) != "" {
				if filter.Since, err = parseTimeArg("since", in.Since, now); err != nil {
					return nil, err
				}
			}
			if strings.TrimSpace(in.Until) != "" && !strings.EqualFold(strings.TrimSpace(in.Until), "now") {
				if filter.Until, err = parseTimeArg("until", in.Until, now); err != nil {
					return nil, err
				}
			}
			if !filter.Since.IsZero() && !filter.Until.IsZero() && filter.Until.Before(filter.Since) {
				return nil, invalidArgs("until must not be before since")
			}
			switch strings.ToLower(strings.TrimSpace(in.Kind)) {
			case "", "any", "all":
			case "appeared", "new":
				filter.Kind = storage.DriftAppeared
			case "disappeared", "gone":
				filter.Kind = storage.DriftDisappeared
			default:
				return nil, invalidArgs("kind must be one of any, appeared, disappeared")
			}
			if in.ExternalOnly {
				t := true
				filter.External = &t
			}
			if in.Cursor != "" {
				c, err := decodeDriftCursor(in.Cursor)
				if err != nil {
					return nil, invalidArgs("cursor is not a next_cursor value from a previous list_drift_events response")
				}
				filter.Before = c
			}
			limit := clamp(in.Limit, driftDefaultLimit, 1, driftMaxLimit)

			match, err := driftServiceMatcher(ctx, env, in.Service)
			if err != nil {
				return nil, err
			}

			var (
				events  []storage.DriftEvent
				scanned int
				next    *storage.DriftEventCursor
			)
			for {
				pageSize := limit
				if match != nil {
					pageSize = service.MaxDriftEventLimit
				}
				filter.Limit = pageSize
				page, err := env.Topology.ListDriftEvents(ctx, filter)
				if err != nil {
					return nil, err
				}
				for i, ev := range page.Events {
					scanned++
					if match == nil || match(ev) {
						events = append(events, ev)
					}
					if len(events) == limit {
						if i < len(page.Events)-1 || page.Next != nil {
							next = &storage.DriftEventCursor{DetectedAt: ev.DetectedAt, ID: ev.ID}
						}
						break
					}
				}
				if len(events) == limit || page.Next == nil {
					break
				}
				if scanned >= driftScanCap {
					next = page.Next
					break
				}
				filter.Before = page.Next
			}

			items := make([]map[string]any, 0, len(events))
			for _, ev := range events {
				items = append(items, driftEventView(ev))
			}
			out := map[string]any{"events": items, "returned": len(items)}
			if next != nil {
				out["next_cursor"] = encodeDriftCursor(*next)
			}
			if kept := fitList(out, "events", items); kept < len(items) {
				if kept == 0 {
					return nil, &ToolError{Code: "result_too_large", Message: "a single drift event is over the result size cap"}
				}
				last := events[kept-1]
				out["returned"] = kept
				out["next_cursor"] = encodeDriftCursor(storage.DriftEventCursor{DetectedAt: last.DetectedAt, ID: last.ID})
				out["note"] = "fewer events than the limit were returned to stay within the result size cap; pass next_cursor for more"
			}
			if match != nil && next != nil && len(items) < limit {
				out["note"] = fmt.Sprintf("stopped after scanning %d events for the service filter; pass next_cursor to continue further back", scanned)
			}
			if st := env.Topology.GetDriftStatus(); st.Running {
				det := map[string]any{"last_check": formatTime(st.LastCheck)}
				if !st.WarmupUntil.IsZero() && st.WarmupUntil.After(now) {
					det["warmup_until"] = formatTime(st.WarmupUntil)
				}
				out["detector"] = det
			} else {
				out["detector"] = map[string]any{"running": false}
			}
			return out, nil
		}),
	}
}

// driftServiceMatcher builds the service filter, or nil for none. A service
// that still exists is matched by ID (an exact ID or exact name, so a guess
// never narrows the feed silently); any reference is also matched against
// the names recorded in events, which is how retired services are found.
func driftServiceMatcher(ctx context.Context, env *Env, ref string) (func(storage.DriftEvent) bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	ids := map[string]bool{ref: true}
	snap, err := env.snapshot(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	r, rerr := snap.resolveService("service", ref)
	var te *ToolError
	switch {
	case rerr == nil:
		if r.matchedBy != "partial" {
			ids[r.svc.ID] = true
		}
	case errors.As(rerr, &te) && te.Code == "ambiguous_service":
		return nil, rerr
	}
	needle := strings.ToLower(ref)
	return func(ev storage.DriftEvent) bool {
		return ids[ev.SourceID] || ids[ev.TargetID] ||
			strings.ToLower(ev.SourceName) == needle || strings.ToLower(ev.TargetName) == needle
	}, nil
}

// driftEventView is an event's model-facing form. Names are the ones
// recorded when the change was detected (endpoints of a dependency that
// disappeared have often been pruned since); IDs are emitted unaltered.
func driftEventView(ev storage.DriftEvent) map[string]any {
	end := func(id, name string) map[string]any {
		m := map[string]any{"id": id}
		if name != "" && name != id {
			m["name"] = sanitizeString(name)
		}
		return m
	}
	out := map[string]any{
		"kind":        string(ev.Kind),
		"detected_at": formatTime(ev.DetectedAt),
		"source":      end(ev.SourceID, ev.SourceName),
		"target":      end(ev.TargetID, ev.TargetName),
		"port":        ev.Port,
		"protocol":    orDefault(ev.Protocol, "tcp"),
		"external":    ev.External,
	}
	if ev.Kind == storage.DriftAppeared {
		out["first_seen"] = formatTime(ev.FirstSeen)
	} else {
		out["last_seen"] = formatTime(ev.LastSeen)
	}
	return out
}

// The cursor is opaque to the model: "<detected_at unix µs>.<id>".
func encodeDriftCursor(c storage.DriftEventCursor) string {
	return strconv.FormatInt(c.DetectedAt.UnixMicro(), 10) + "." + strconv.FormatInt(c.ID, 10)
}

func decodeDriftCursor(s string) (*storage.DriftEventCursor, error) {
	at, id, ok := strings.Cut(strings.TrimSpace(s), ".")
	if !ok {
		return nil, errors.New("malformed cursor")
	}
	us, err1 := strconv.ParseInt(at, 10, 64)
	n, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil || us <= 0 || n <= 0 {
		return nil, errors.New("malformed cursor")
	}
	return &storage.DriftEventCursor{DetectedAt: time.UnixMicro(us).UTC(), ID: n}, nil
}
