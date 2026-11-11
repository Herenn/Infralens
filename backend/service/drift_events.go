package service

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/storage"
)

// Drift feed listing limits.
const (
	DefaultDriftEventLimit = 50
	MaxDriftEventLimit     = 500
)

// driftNameBatchThreshold is how many distinct endpoints a pass may name
// one lookup at a time before driftNames reads current state in one go.
const driftNameBatchThreshold = 20

// DriftEventPage is one page of the drift feed, newest first.
type DriftEventPage struct {
	Events []storage.DriftEvent
	// Next continues the listing (pass it as the filter's Before); nil when
	// there is nothing older.
	Next *storage.DriftEventCursor
}

// DriftStatus describes the running drift detector, so a UI can explain an
// empty feed ("still learning your architecture until ...").
type DriftStatus struct {
	// Running is false until the detector has loaded its baseline, and
	// stays false if detection is not running at all.
	Running bool
	// WarmupUntil: edges first seen before this are not reported as new.
	WarmupUntil time.Time
	// LastCheck is the end of the last completed pass.
	LastCheck time.Time
	Interval  time.Duration
	GoneAfter time.Duration
	// SilentAgents are agents that stopped reporting more than an hour
	// (the group member timeout) before the last pass and less than the
	// retirement threshold ago, newest silence first, at most
	// MaxDriftSilentAgents; SilentAgentsTotal counts them all. Edges only
	// such an agent could see are not reported gone while it is merely
	// silent - except in a CLUSTER_NAME group, where a member silent for
	// longer than the timeout no longer holds the group back (see
	// coverage.go): what only it could see may then be reported gone.
	SilentAgents      []SilentAgent
	SilentAgentsTotal int
}

// SilentAgent is an agent that stopped reporting.
type SilentAgent struct {
	Node string
	// Group is the agent's CLUSTER_NAME, "" when it reports ungrouped.
	Group    string
	LastSeen time.Time
}

// MaxDriftSilentAgents caps DriftStatus.SilentAgents.
const MaxDriftSilentAgents = 20

// ListDriftEvents returns one page of the persisted drift feed, newest
// first. A non-positive filter.Limit means DefaultDriftEventLimit; it is
// capped at MaxDriftEventLimit.
//
// This is the service-layer entry point for anything that reads the feed -
// the HTTP API, and integrations such as an MCP tool.
func (ts *TopologyService) ListDriftEvents(ctx context.Context, filter storage.DriftEventFilter) (*DriftEventPage, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}
	if filter.Kind != "" && !filter.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown drift event kind %q", storage.ErrInvalidInput, filter.Kind)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultDriftEventLimit
	}
	if limit > MaxDriftEventLimit {
		limit = MaxDriftEventLimit
	}

	// One extra row tells whether there is a next page without a COUNT.
	filter.Limit = limit + 1
	events, err := ts.store.Drift().ListEvents(ctx, filter)
	if err != nil {
		return nil, err
	}

	page := &DriftEventPage{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		last := page.Events[limit-1]
		page.Next = &storage.DriftEventCursor{DetectedAt: last.DetectedAt, ID: last.ID}
	}
	if page.Events == nil {
		page.Events = []storage.DriftEvent{}
	}
	return page, nil
}

// GetDriftSummary counts the feed's events in the filter's time window
// (Since, After, Until; zero bounds are unbounded) - cheap enough to poll for
// an unread badge.
func (ts *TopologyService) GetDriftSummary(ctx context.Context, window storage.DriftEventFilter) (storage.DriftSummary, error) {
	if !ts.historyEnabled {
		return storage.DriftSummary{}, ErrHistoryDisabled
	}
	return ts.store.Drift().Summarize(ctx, window)
}

// GetDriftStatus reports on the drift detector, if one is running.
func (ts *TopologyService) GetDriftStatus() DriftStatus {
	if st := ts.driftStatus.Load(); st != nil {
		return *st
	}
	return DriftStatus{}
}

// ImportDriftEvents records events that were not produced by this process's
// detector - the demo simulator's seeded history, or a future importer.
// Fingerprints must be set; events already present are skipped. Nothing is
// published: these are past events, not news.
func (ts *TopologyService) ImportDriftEvents(ctx context.Context, events []storage.DriftEvent) ([]storage.DriftEvent, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}
	return ts.store.Drift().InsertEvents(ctx, events)
}

// publishDriftEvents pushes newly recorded events to live subscribers.
func (ts *TopologyService) publishDriftEvents(events []storage.DriftEvent) {
	if ts.eventBus == nil || len(events) == 0 {
		return
	}
	ts.eventBus.PublishDriftDetected(events)
}

// publishStatus makes the detector's current state visible to
// GetDriftStatus.
func (d *DriftDetector) publishStatus() {
	if d.ts == nil {
		return
	}
	silent := make([]SilentAgent, 0, len(d.silent))
	for _, a := range d.silent {
		if len(silent) == MaxDriftSilentAgents {
			break
		}
		silent = append(silent, SilentAgent{Node: a.Node, Group: a.Group, LastSeen: a.LastSeen})
	}
	d.ts.driftStatus.Store(&DriftStatus{
		Running:           true,
		WarmupUntil:       d.baselineUntil,
		LastCheck:         d.lastCheck,
		Interval:          d.cfg.Interval,
		GoneAfter:         d.cfg.GoneAfter,
		SilentAgents:      silent,
		SilentAgentsTotal: len(d.silent),
	})
}

// driftFingerprint identifies the occurrence behind a change - "this edge
// first appeared at T", "this edge was last seen at T" - independent of when,
// or by which process, it was detected. Re-detecting the same change (after a
// restart resumes from an earlier checkpoint, or on another replica) yields
// the same fingerprint, so the feed stores it once.
//
// Microsecond precision: Postgres stores no finer, and the timestamps here
// are always ones read back from history, so truncation makes both backends
// agree.
func driftFingerprint(kind storage.DriftEventKind, e DriftEdge) string {
	at := e.FirstSeen
	if kind == storage.DriftDisappeared {
		at = e.LastSeen
	}
	return fmt.Sprintf("%s|%s|%d", kind, e.ConnectionID, at.UnixMicro())
}

// eventsFor turns a report into feed events, resolving display names for
// the endpoints.
func (d *DriftDetector) eventsFor(ctx context.Context, report *DriftReport) []storage.DriftEvent {
	if report.Empty() {
		return nil
	}

	ids := make([]string, 0, 2*(len(report.Appeared)+len(report.Disappeared)))
	since := report.DetectedAt
	for _, group := range [][]DriftEdge{report.Appeared, report.Disappeared} {
		for _, e := range group {
			ids = append(ids, e.SourceID, e.TargetID)
			if e.LastSeen.Before(since) {
				since = e.LastSeen
			}
		}
	}
	names := d.ts.driftNames(ctx, ids, since.Add(-d.maxGap), report.DetectedAt)

	events := make([]storage.DriftEvent, 0, len(report.Appeared)+len(report.Disappeared))
	add := func(kind storage.DriftEventKind, e DriftEdge) {
		events = append(events, storage.DriftEvent{
			DetectedAt:   report.DetectedAt,
			Kind:         kind,
			ConnectionID: e.ConnectionID,
			SourceID:     e.SourceID,
			SourceName:   names[e.SourceID],
			TargetID:     e.TargetID,
			TargetName:   driftTargetName(e, names),
			Port:         e.Port,
			Protocol:     e.Protocol,
			External:     e.External,
			FirstSeen:    e.FirstSeen,
			LastSeen:     e.LastSeen,
			Fingerprint:  driftFingerprint(kind, e),
		})
	}
	for _, e := range report.Appeared {
		add(storage.DriftAppeared, e)
	}
	for _, e := range report.Disappeared {
		add(storage.DriftDisappeared, e)
	}
	return events
}

// IsExternalServiceID reports whether a service ID identifies a public
// internet address - the test drift detection uses to flag internet-bound
// dependencies.
func IsExternalServiceID(id string) bool {
	return isPublicServiceID(id)
}

// DriftDisplayName is the name the drift feed records for a service: the
// most human-readable thing known about it, or "" if nothing beats its ID.
// An external endpoint reached by a DNS name is named by it (ServiceHostname)
// rather than by its bare address; svc.Hostnames must be annotated for that,
// which is why eventsFor also passes the edge's own name (driftTargetName).
func DriftDisplayName(svc *storage.Service) string {
	if svc == nil {
		return ""
	}
	if h := ServiceHostname(svc); h != "" {
		return h
	}
	for _, name := range []string{svc.DisplayName, svc.Name} {
		if name != "" && name != svc.ID {
			return name
		}
	}
	return ""
}

// driftTargetName is the recorded name of an event's target: the DNS name
// the source resolved it from when one was observed on the edge (the same
// name the webhook text and the topology show), else the service's name.
func driftTargetName(e DriftEdge, names map[string]string) string {
	if e.TargetHostname != "" {
		return e.TargetHostname
	}
	return names[e.TargetID]
}

// driftNames resolves display names for service IDs: from current state
// first, then - for services already pruned from it, which is the usual case
// for the far end of a dependency that disappeared a day ago - from the
// newest history interval in [from, to]. Lookup failures only cost a name;
// the feed falls back to IDs.
func (ts *TopologyService) driftNames(ctx context.Context, ids []string, from, to time.Time) map[string]string {
	names := make(map[string]string, len(ids))
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}

	// A handful of changes: look each up. A rollout's worth: one read of
	// current state beats thousands of point queries.
	var current func(id string) *storage.Service
	if len(unique) > driftNameBatchThreshold {
		all, err := ts.store.Services().List(ctx, storage.ServiceFilter{})
		byID := make(map[string]*storage.Service, len(all))
		if err == nil {
			for i := range all {
				byID[all[i].ID] = &all[i]
			}
		}
		current = func(id string) *storage.Service { return byID[id] }
	} else {
		current = func(id string) *storage.Service {
			svc, err := ts.store.Services().Get(ctx, id)
			if err != nil {
				return nil
			}
			return svc
		}
	}

	var missing []string
	for _, id := range unique {
		if name := DriftDisplayName(current(id)); name != "" {
			names[id] = name
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 || !ts.historyEnabled {
		return names
	}

	intervals, err := ts.store.History().ServicesBetween(ctx, from, to)
	if err != nil {
		log.WithError(err).Debug("Drift detection: could not resolve names from history")
		return names
	}
	want := make(map[string]bool, len(missing))
	for _, id := range missing {
		want[id] = true
	}
	newest := make(map[string]time.Time, len(missing))
	for _, si := range intervals {
		if !want[si.ServiceID] || si.Name == "" || si.Name == si.ServiceID {
			continue
		}
		if at, ok := newest[si.ServiceID]; ok && !si.LastSeen.After(at) {
			continue
		}
		newest[si.ServiceID] = si.LastSeen
		names[si.ServiceID] = si.Name
	}
	return names
}
