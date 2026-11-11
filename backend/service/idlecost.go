package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/storage"
)

// Idle spend (beta): candidates for cleanup - services nothing talks to - with
// the network evidence for each and an approximate share of what their node
// costs. These are candidates to verify with their owners, never
// recommendations to delete.
//
// What InfraLens can and cannot know, which shapes everything below:
//
//   - Idleness is observed, within limits. Inbound connections are eBPF
//     accept/connect events and topology history records when each edge
//     existed. But silence only means something while InfraLens was actually
//     watching: an agent or backend outage looks exactly like silence in the
//     raw data. Quiet periods are therefore measured against observation
//     coverage (see idlecost_coverage.go), never against wall-clock time.
//   - Cost is NOT observed. Agents report node-wide CPU%/RAM only - no
//     per-process usage, no pod requests, no instance type. A service's figure
//     is always an attribution of a user-supplied node price, with the method
//     and a confidence attached. When agents group nodes under CLUSTER_NAME,
//     not even the node is known and only a cluster-wide split is possible.
//
// Invariants the tests hold this to (idlecost_invariants_test.go):
//   - per node (or cluster group), the attributed figures never sum to more
//     than its price;
//   - nothing that receives traffic from a service that is not itself flagged
//     is flagged, and nothing that is an endpoint of a live connection is ever
//     reported as "not seen";
//   - time InfraLens was not observing never counts as silence;
//   - a cluster-wide group never yields a per-node figure.
//
// When evidence is weak the report prefers "held back, and why" (Excluded) to
// a priced line.

// Idle-spend defaults. See IdleCostConfig for what each one trades off.
const (
	DefaultIdleWindow     = 30 * 24 * time.Hour
	DefaultIdleQuietAfter = 72 * time.Hour
	DefaultIdleWarmup     = 24 * time.Hour

	// idleHighConfidenceQuiet: observed silence needed for a "high" idle
	// verdict. Longer than any calendar month, so a monthly job cannot reach
	// high; with the default 30-day retention the best possible is medium.
	idleHighConfidenceQuiet = 31 * 24 * time.Hour

	// idleNodeAliveWindow: host metrics older than this mean the node is not
	// reporting.
	idleNodeAliveWindow = 15 * time.Minute

	// idleReportTTL is how long a computed report is reused. The endpoint is
	// a public read and the computation scans history, so this bounds its
	// cost no matter how often it is called.
	idleReportTTL = 60 * time.Second

	// maxIdleCacheEntries bounds the cache: the key includes the what-if
	// price, which is caller-controlled.
	maxIdleCacheEntries = 16

	maxIdleExcluded        = 50
	maxIdleStaleCandidates = 500
	defaultIdleLimit       = 100

	// agentGroupTTL: an agent node not heard from for this long is no longer
	// counted as a member of its cluster group.
	agentGroupTTL = 15 * time.Minute
)

// Idle categories, strongest signal first.
const (
	IdleCategoryOrphan          = "orphan"            // no connections at all
	IdleCategoryNoInbound       = "no_inbound"        // alive (talks outbound) but nothing connected in the observed window
	IdleCategoryQuiet           = "quiet"             // used to receive traffic; nothing for at least QuietAfter (observed)
	IdleCategoryIdleCallersOnly = "idle_callers_only" // only reached by other flagged services
	IdleCategoryStale           = "stale"             // not observed at all recently (decommission candidate)
)

// Confidence levels. "none" is only used for cost_confidence when unpriced.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
	ConfidenceNone   = "none"
)

// Price sources, in resolution order.
const (
	PriceSourceServiceRule = "service_rule"
	PriceSourceNodeRule    = "node_rule"
	PriceSourceDefault     = "default"
	PriceSourceAssumed     = "assumed"
	PriceSourceCluster     = "cluster" // sum of the member nodes' prices
)

// Share methods: how a node price is attributed to one service.
const (
	ShareDirect       = "direct"        // service rule on a node-less endpoint, or on an unpriced node
	ShareCarveOut     = "carve_out"     // service rule carved out of its node's price
	ShareWholeNode    = "whole_node"    // the only service on the node, or every service on it is flagged
	ShareEvenSplit    = "even_split"    // what is left of the node price / services observed on the node
	ShareClusterSplit = "cluster_split" // CLUSTER_NAME grouping: the cluster's price / services in it
)

// Exclusion reasons: services that look idle but were held back.
const (
	IdleExcludedWarmup         = "warmup"
	IdleExcludedOutboundOnly   = "outbound_only"
	IdleExcludedMonitoring     = "monitoring"
	IdleExcludedReachedByAddr  = "reached_by_address"
	IdleExcludedInfraLens      = "infralens"
	IdleExcludedUser           = "user_excluded"
	IdleExcludedNodeGone       = "node_gone"
	IdleExcludedNotQuietEnough = "not_quiet_long_enough"
	IdleExcludedLiveTraffic    = "live_traffic"
	IdleExcludedReplica        = "possible_replica"
	IdleExcludedUDP            = "udp_service"
	IdleExcludedNoHistory      = "history_disabled"
)

// PriceRule maps a glob pattern ("*" matches any run of characters,
// including "/"; "?" one character; case-insensitive) to a monthly price.
type PriceRule struct {
	Pattern string  `json:"pattern"`
	Monthly float64 `json:"monthly"`
}

// IdleCostConfig configures idle-spend estimation. The zero value is valid:
// no prices (estimates are null), default window and quiet-after. Warmup
// follows DriftConfig's convention, where zero disables it; pass -1 for the
// default. A TopologyService that was never configured uses the default.
type IdleCostConfig struct {
	// Currency is an ISO 4217 code used as a display label only. Anything
	// that is not three letters falls back to USD with a warning.
	Currency string

	// DefaultNodeMonthly is the assumed monthly cost of any node no rule
	// matches. Non-positive means unset.
	DefaultNodeMonthly float64

	// NodePrices are matched against node names (the agent node names when
	// agents report a CLUSTER_NAME, or the group name itself to price a
	// whole cluster); first match wins.
	NodePrices []PriceRule

	// ServicePrices are matched against service name, ID and display name.
	// On a priced node the price is carved out of the node's price (never
	// added on top of it); on a node-less endpoint (a managed database) or
	// an unpriced node it stands alone.
	ServicePrices []PriceRule

	// Exclude lists glob patterns never flagged.
	Exclude []string

	// Window is how far back history is consulted. Clamped to retention.
	Window time.Duration

	// QuietAfter is how much *observed* silence flags a service.
	QuietAfter time.Duration

	// Warmup: services first observed less than this long ago are never
	// flagged, and nothing is flagged until history covers this long.
	Warmup time.Duration

	// Source describes where prices came from ("env", "demo").
	Source string

	// Warnings are configuration problems found while parsing.
	Warnings []string
}

// Priced reports whether any price is configured.
func (c IdleCostConfig) Priced() bool {
	return c.DefaultNodeMonthly > 0 || len(c.NodePrices) > 0 || len(c.ServicePrices) > 0
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

func (c IdleCostConfig) withDefaults() IdleCostConfig {
	c.Warnings = append([]string{}, c.Warnings...)
	c.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	if c.Currency == "" {
		c.Currency = "USD"
	} else if !currencyCode.MatchString(c.Currency) {
		c.Warnings = append(c.Warnings, "IDLE_COST_CURRENCY must be a three-letter code such as USD or EUR; using USD")
		c.Currency = "USD"
	}
	if c.Window <= 0 {
		c.Window = DefaultIdleWindow
	}
	if c.QuietAfter <= 0 {
		c.QuietAfter = DefaultIdleQuietAfter
	}
	if c.Warmup < 0 {
		c.Warmup = DefaultIdleWarmup
	}
	return c
}

// IdleSpendQuery is the per-request part of an idle-spend estimate.
type IdleSpendQuery struct {
	// AssumeNodeMonthly, when positive, prices every node that no configured
	// rule or default covers at this amount, labelled "assumed".
	AssumeNodeMonthly float64

	// Limit caps the itemized list (totals always cover everything).
	Limit int
}

// IdleSpendReport is the full answer. Treat it as read-only: reports are
// cached and shared between callers.
type IdleSpendReport struct {
	GeneratedAt    time.Time          `json:"generated_at"`
	Beta           bool               `json:"beta"`
	Disclaimer     string             `json:"disclaimer"`
	Currency       string             `json:"currency"`
	CurrencySymbol string             `json:"currency_symbol"`
	Pricing        IdlePricingSummary `json:"pricing"`
	Evidence       IdleEvidenceWindow `json:"evidence_window"`
	Totals         IdleSpendTotals    `json:"totals"`
	Items          []IdleServiceItem  `json:"items"`
	Nodes          []IdleNodeSummary  `json:"nodes"`
	Excluded       []IdleExclusion    `json:"excluded"`
	ExcludedCount  int                `json:"excluded_count"`
	Notes          []string           `json:"notes"`
	// SilentAgents are CLUSTER_NAME members that were out within the
	// evidence window - silent for longer than the one-hour member timeout,
	// or flapping - whether still out or reporting again: one entry per
	// agent (its most recent outage), still-out first, at most
	// maxIdleSilentAgents. While a group has one, its candidates are low
	// confidence and not in the total, and Notes opens with one sentence per
	// such group saying so.
	SilentAgents []IdleSilentAgent `json:"silent_agents"`
	// SilentAgentsTotal counts all such agents (SilentAgents is capped);
	// SilentAgentsStillOut those of them not reporting yet. SilentNotes is
	// how many entries at the head of Notes are about them (one per group).
	SilentAgentsTotal    int `json:"silent_agents_total"`
	SilentAgentsStillOut int `json:"silent_agents_still_out"`
	SilentNotes          int `json:"silent_notes"`
}

// IdleSilentAgent is a grouped agent that was out within the evidence
// window.
type IdleSilentAgent struct {
	Group string `json:"group"`
	Node  string `json:"node"`
	// LastSeen is its most recent heartbeat.
	LastSeen time.Time `json:"last_seen"`
	// SilentSince is its last heartbeat before the outage.
	SilentSince time.Time `json:"silent_since"`
	// ReturnedAt is the heartbeat that ended the outage; absent while the
	// agent is still out.
	ReturnedAt *time.Time `json:"returned_at,omitempty"`
	// Intermittent: it kept reporting now and then (flapping) rather than
	// being silent throughout.
	Intermittent bool `json:"intermittent,omitempty"`
}

// IdleDisclaimer is shown with every report.
const IdleDisclaimer = "Beta. These are candidates to verify with the owning team, not recommendations to delete. " +
	"Idle means no inbound connections observed by InfraLens while it was watching; costs are attributions of configured prices, not billing data."

// IdlePricingSummary says which prices were in effect.
type IdlePricingSummary struct {
	Configured         bool        `json:"configured"`
	Source             string      `json:"source"` // "env", "demo", "assumed", "none"
	DefaultNodeMonthly *float64    `json:"default_node_monthly"`
	AssumedNodeMonthly *float64    `json:"assumed_node_monthly"`
	NodeRules          []PriceRule `json:"node_rules"`
	ServiceRules       []PriceRule `json:"service_rules"`
	Warnings           []string    `json:"warnings"`
}

// IdleEvidenceWindow describes how much observation backs the verdicts.
type IdleEvidenceWindow struct {
	HistoryEnabled    bool      `json:"history_enabled"`
	From              time.Time `json:"from"`
	To                time.Time `json:"to"`
	ObservedSeconds   int64     `json:"observed_seconds"`   // time InfraLens was actually observing, within [from, to]
	UnobservedSeconds int64     `json:"unobserved_seconds"` // collection gaps within [from, to]
	WindowSeconds     int64     `json:"window_seconds"`
	QuietAfterSeconds int64     `json:"quiet_after_seconds"`
	WarmupSeconds     int64     `json:"warmup_seconds"`
	WarmingUp         bool      `json:"warming_up"`
	ClusterGrouped    bool      `json:"cluster_grouped"`
}

// IdleSpendTotals sums the priced items counted in the headline.
type IdleSpendTotals struct {
	Monthly             float64            `json:"monthly"`
	Annual              float64            `json:"annual"`
	Services            int                `json:"services"`
	Priced              int                `json:"priced"`
	Unpriced            int                `json:"unpriced"`
	MonthlyByConfidence map[string]float64 `json:"monthly_by_confidence"`
	// NotInTotalMonthly is priced but deliberately left out of Monthly
	// (history disabled: current-window evidence only; or an agent in the
	// candidate's CLUSTER_NAME group stopped reporting).
	NotInTotalMonthly float64 `json:"not_in_total_monthly"`
	// NotInTotalByReason splits NotInTotalMonthly by the items'
	// not_in_total_reason; absent when nothing is left out.
	NotInTotalByReason map[string]float64 `json:"not_in_total_by_reason,omitempty"`
}

// IdleServiceItem is one candidate: evidence, estimate, confidence.
type IdleServiceItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Tech      string `json:"tech,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Node      string `json:"node,omitempty"`

	InGraph  bool         `json:"in_graph"`
	Category string       `json:"category"`
	Evidence IdleEvidence `json:"evidence"`

	Cost           *IdleCostEstimate `json:"cost"`
	IdleConfidence string            `json:"idle_confidence"`
	CostConfidence string            `json:"cost_confidence"`
	Confidence     string            `json:"confidence"`

	// InTotal is false when the cost is shown but kept out of the headline;
	// NotInTotalReason then says why (NotInTotal*).
	InTotal          bool    `json:"in_total"`
	NotInTotalReason string  `json:"not_in_total_reason,omitempty"`
	Score            float64 `json:"score"`
}

// Why an item is not in the total.
const (
	// NotInTotalCurrentWindow: history is disabled, so the evidence is the
	// current window only.
	NotInTotalCurrentWindow = "current_window"
	// NotInTotalSilentAgent: an agent in the item's CLUSTER_NAME group was
	// out within the evidence window (IdleSpendReport.SilentAgents).
	NotInTotalSilentAgent = "silent_agent"
)

// IdleEvidence is why a service is considered idle.
type IdleEvidence struct {
	// LastInbound is the most recent inbound connection that counts as use.
	LastInbound *time.Time `json:"last_inbound"`

	// QuietSeconds is observed silence: time since LastInbound (or since
	// observation began) during which InfraLens was actually watching the
	// node. QuietWallSeconds is the wall-clock span; the difference is time
	// not observed, which is never counted as silence.
	QuietSeconds      int64 `json:"quiet_seconds"`
	QuietWallSeconds  int64 `json:"quiet_wall_seconds"`
	UnobservedSeconds int64 `json:"unobserved_seconds"`
	QuietIsLowerBound bool  `json:"quiet_is_lower_bound"`

	FirstSeen *time.Time `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`

	LiveInboundEdges  int      `json:"live_inbound_edges"`
	LiveOutboundEdges int      `json:"live_outbound_edges"`
	OutboundPeers     []string `json:"outbound_peers,omitempty"`
	OutboundBytes     uint64   `json:"outbound_bytes"`
	InboundBytes      uint64   `json:"inbound_bytes"`
	IgnoredCallers    []string `json:"ignored_callers,omitempty"`
	IdleCallers       []string `json:"idle_callers,omitempty"`

	Notes []string `json:"notes"`
}

// IdleCostEstimate is how a service's monthly figure was derived.
type IdleCostEstimate struct {
	Monthly     float64 `json:"monthly"`
	Annual      float64 `json:"annual"`
	NodeMonthly float64 `json:"node_monthly"`
	Share       float64 `json:"share"`
	SharedWith  int     `json:"shared_with"`
	PriceSource string  `json:"price_source"`
	PriceRule   string  `json:"price_rule,omitempty"`
	ShareMethod string  `json:"share_method"`
	Explanation string  `json:"explanation"`
}

// IdleNodeSummary is the per-node (or per-cluster-group) picture.
type IdleNodeSummary struct {
	Node         string   `json:"node"`
	Cluster      bool     `json:"cluster"`
	AgentNodes   []string `json:"agent_nodes,omitempty"`
	Monthly      *float64 `json:"monthly"`
	PriceSource  string   `json:"price_source,omitempty"`
	PriceRule    string   `json:"price_rule,omitempty"`
	Services     int      `json:"services"`
	IdleServices int      `json:"idle_services"`
	// IdleMonthly is the flagged services' cost counted in the report's
	// total; IdleMonthlyNotInTotal the part shown but kept out of it. Over
	// all nodes they add up to Totals.Monthly and Totals.NotInTotalMonthly,
	// less node-less items priced by a service rule.
	IdleMonthly           float64  `json:"idle_monthly"`
	IdleMonthlyNotInTotal float64  `json:"idle_monthly_not_in_total"`
	FullyIdle             bool     `json:"fully_idle"`
	Reporting             bool     `json:"reporting"`
	CPUPercent            *float64 `json:"cpu_percent,omitempty"`
	MemPercent            *float64 `json:"mem_percent,omitempty"`
}

// IdleExclusion is a service a false-positive guard held back.
type IdleExclusion struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Node   string `json:"node,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// --- service state ----------------------------------------------------------

// idleState is everything the idle-spend feature keeps on TopologyService:
// configuration, the report cache, the agent-node registry used to detect
// CLUSTER_NAME grouping, and the throttle for keeping long-lived connection
// endpoints alive (liveness.go).
type idleState struct {
	cfg atomic.Pointer[IdleCostConfig]

	cacheMu sync.Mutex
	cache   map[string]*idleCacheEntry

	groupsMu        sync.Mutex
	groups          map[string]map[string]time.Time // group -> agent node -> last heard
	groupsSwept     time.Time
	groupsSize      int // agent nodes across all groups
	groupsCapLogged bool

	liveMu      sync.Mutex
	liveSeen    map[string]time.Time
	liveSwept   time.Time
	pruneMaxAge time.Duration // see SetPruneMaxAge

	computations atomic.Int64 // reports computed (not served from cache); for tests
}

type idleCacheEntry struct {
	done   chan struct{}
	at     time.Time
	report *IdleSpendReport
	err    error
}

// ConfigureIdleCost sets the pricing and thresholds used by GetIdleSpend.
func (ts *TopologyService) ConfigureIdleCost(cfg IdleCostConfig) {
	ts.idle.cfg.Store(&cfg)
	ts.idle.cacheMu.Lock()
	ts.idle.cache = nil
	ts.idle.cacheMu.Unlock()
}

// IdleCostConfig returns the configured idle-spend settings.
func (ts *TopologyService) IdleCostConfig() IdleCostConfig {
	cfg := ts.idle.cfg.Load()
	if cfg == nil {
		return IdleCostConfig{Warmup: -1}
	}
	return *cfg
}

// NoteAgentNode records that an agent reporting as node reports under group
// (its CLUSTER_NAME). The backend replaces the node name with the group name
// everywhere else, so this registry is the only way to know a "node" is
// really a cluster - and how many machines it stands for.
func (ts *TopologyService) NoteAgentNode(group, node string) {
	if group == "" || node == "" {
		return
	}
	now := time.Now()
	ts.idle.groupsMu.Lock()
	defer ts.idle.groupsMu.Unlock()
	if ts.idle.groups == nil {
		ts.idle.groups = map[string]map[string]time.Time{}
	}
	// Expire on write too, not only when a report is computed: node names
	// churn (autoscaling, rebuilt hosts) and nothing else bounds the map.
	if now.Sub(ts.idle.groupsSwept) > agentGroupTTL/2 {
		ts.sweepAgentGroupsLocked(now)
	}
	members := ts.idle.groups[group]
	if _, known := members[node]; !known {
		if ts.idle.groupsSize >= maxAgentNodes {
			// Pathological (agent node names come from whoever can post
			// host metrics): make room by forgetting the least recently
			// heard node, so a real new node is never ignored.
			ts.evictOldestAgentNodeLocked()
			if !ts.idle.groupsCapLogged {
				ts.idle.groupsCapLogged = true
				log.WithField("cap", maxAgentNodes).Warn("Agent-node registry is full; forgetting the least recently heard agent nodes")
			}
		}
		if members == nil {
			members = map[string]time.Time{}
			ts.idle.groups[group] = members
		}
		ts.idle.groupsSize++
	}
	members[node] = now
}

// maxAgentNodes caps the agent-node registry. Agent node names come from
// whoever can post host metrics; far beyond any real cluster.
const maxAgentNodes = 10000

// evictOldestAgentNodeLocked forgets the least recently heard agent node.
// groupsMu must be held. Linear, but only ever reached at the cap.
func (ts *TopologyService) evictOldestAgentNodeLocked() {
	var og, on string
	var oldest time.Time
	for g, nodes := range ts.idle.groups {
		for n, at := range nodes {
			if og == "" || at.Before(oldest) {
				og, on, oldest = g, n, at
			}
		}
	}
	if og == "" {
		return
	}
	delete(ts.idle.groups[og], on)
	if len(ts.idle.groups[og]) == 0 {
		delete(ts.idle.groups, og)
	}
	ts.idle.groupsSize--
}

// sweepAgentGroupsLocked drops agent nodes not heard from within
// agentGroupTTL, and groups left empty. groupsMu must be held.
func (ts *TopologyService) sweepAgentGroupsLocked(now time.Time) {
	for g, nodes := range ts.idle.groups {
		for n, at := range nodes {
			if now.Sub(at) > agentGroupTTL {
				delete(nodes, n)
				ts.idle.groupsSize--
			}
		}
		if len(nodes) == 0 {
			delete(ts.idle.groups, g)
		}
	}
	ts.idle.groupsSwept = now
}

func (ts *TopologyService) agentGroups(now time.Time) map[string][]string {
	ts.idle.groupsMu.Lock()
	defer ts.idle.groupsMu.Unlock()
	ts.sweepAgentGroupsLocked(now)
	out := map[string][]string{}
	for g, nodes := range ts.idle.groups {
		for n := range nodes {
			out[g] = append(out[g], n)
		}
		sort.Strings(out[g])
	}
	return out
}

// GetIdleSpend estimates what idle services cost, with the evidence for each.
// It is the single entry point for the HTTP endpoint and any other consumer
// (MCP tool, digests). Reports are cached for idleReportTTL per what-if price,
// and concurrent requests for the same key share one computation.
func (ts *TopologyService) GetIdleSpend(ctx context.Context, q IdleSpendQuery) (*IdleSpendReport, error) {
	key := fmt.Sprintf("%g", q.AssumeNodeMonthly)

	ts.idle.cacheMu.Lock()
	if ts.idle.cache == nil {
		ts.idle.cache = map[string]*idleCacheEntry{}
	}
	entry, ok := ts.idle.cache[key]
	if ok {
		select {
		case <-entry.done:
			if entry.err != nil || time.Since(entry.at) >= idleReportTTL {
				ok = false
			}
		default: // in flight: wait for it below
		}
	}
	if !ok {
		ts.evictIdleCacheLocked()
		entry = &idleCacheEntry{done: make(chan struct{})}
		ts.idle.cache[key] = entry
		ts.idle.cacheMu.Unlock()

		// Computed on a context detached from this caller's cancellation, so
		// one impatient client cannot fail the computation others wait on.
		entry.report, entry.err = ts.computeIdleSpend(context.WithoutCancel(ctx), q.AssumeNodeMonthly)
		entry.at = time.Now()
		close(entry.done)
	} else {
		ts.idle.cacheMu.Unlock()
		select {
		case <-entry.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if entry.err != nil {
		return nil, entry.err
	}
	return limitIdleReport(entry.report, q.Limit), nil
}

func (ts *TopologyService) evictIdleCacheLocked() {
	for k, e := range ts.idle.cache {
		select {
		case <-e.done:
			if time.Since(e.at) >= idleReportTTL {
				delete(ts.idle.cache, k)
			}
		default:
		}
	}
	for len(ts.idle.cache) >= maxIdleCacheEntries {
		var oldest string
		var oldestAt time.Time
		for k, e := range ts.idle.cache {
			select {
			case <-e.done:
				if oldest == "" || e.at.Before(oldestAt) {
					oldest, oldestAt = k, e.at
				}
			default:
			}
		}
		if oldest == "" {
			return // everything in flight; let it grow briefly
		}
		delete(ts.idle.cache, oldest)
	}
}

// limitIdleReport returns a shallow copy with at most limit items.
func limitIdleReport(r *IdleSpendReport, limit int) *IdleSpendReport {
	if limit <= 0 {
		limit = defaultIdleLimit
	}
	out := *r
	if len(out.Items) > limit {
		out.Items = out.Items[:limit:limit]
	}
	return &out
}

func (ts *TopologyService) computeIdleSpend(ctx context.Context, assume float64) (*IdleSpendReport, error) {
	ts.idle.computations.Add(1)
	now := time.Now()
	cfg := ts.IdleCostConfig().withDefaults()

	topo, err := ts.store.GetTopology(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting topology: %w", err)
	}
	// External endpoints are named in evidence ("called only by ...",
	// exclusions) by their DNS name, like everywhere else.
	annotateTopology(topo)

	in := idleInputs{
		now:            now,
		services:       topo.Services,
		connections:    topo.Connections,
		metrics:        topo.NodeMetrics,
		historyEnabled: ts.historyEnabled,
		maxGap:         ts.historyMaxGap,
		groups:         ts.agentGroups(now),
		// A node silent for as long as a service must be to be a
		// decommission candidate counts as retired: what was on it is
		// offered as stale (low confidence), as before heartbeats.
		retireAfter: ts.staleThreshold(),
	}

	if ts.historyEnabled {
		window := cfg.Window
		if ts.historyRetention > 0 && window > ts.historyRetention {
			window = ts.historyRetention
		}
		from := now.Add(-window)
		bounds, ok, err := ts.store.History().Bounds(ctx)
		if err != nil {
			return nil, fmt.Errorf("getting history bounds: %w", err)
		}
		switch {
		case !ok:
			from = now
		case bounds.Earliest.After(from):
			from = bounds.Earliest
		}
		in.coverageStart = from
		in.window = window

		if ok {
			if in.historyEdges, err = latestConnectionIntervals(ctx, ts.store.History(), from, now); err != nil {
				return nil, fmt.Errorf("querying connection history: %w", err)
			}
			if in.historyServices, err = ts.store.History().ServicesBetween(ctx, from, now); err != nil {
				return nil, fmt.Errorf("querying service history: %w", err)
			}
			// All heartbeats within retention, not just the window: the
			// first one marks where heartbeat coverage begins (see coverage.go).
			retention := ts.historyRetention
			if retention <= 0 {
				retention = storage.DefaultHistoryRetention
			}
			if in.historyNodes, err = ts.store.History().NodesBetween(ctx, now.Add(-retention), now); err != nil {
				return nil, fmt.Errorf("querying node history: %w", err)
			}
			if in.stale, err = ts.GetStaleServices(ctx, 0, maxIdleStaleCandidates); err != nil {
				return nil, fmt.Errorf("querying stale services: %w", err)
			}
		}
	}

	// Listening ports from deep inspection, fetched only for the few services
	// whose verdict depends on them.
	ports := map[string][]int{}
	in.listenPorts = func(id string) []int {
		if p, ok := ports[id]; ok {
			return p
		}
		var out []int
		if insp, err := ts.store.Services().GetInspection(ctx, id); err == nil && insp != nil && insp.ListenPorts != "" {
			_ = json.Unmarshal([]byte(insp.ListenPorts), &out)
		}
		ports[id] = out
		return out
	}

	return estimateIdleSpend(in, cfg, IdleSpendQuery{AssumeNodeMonthly: assume}), nil
}

// latestConnectionIntervals returns, per connection, its newest interval
// overlapping [from, to]. Stores that can aggregate in SQL implement
// storage.LatestConnectionIntervaler; others fall back to reading every
// interval. Idle evidence only needs "when was this edge last seen", and
// reading every interval of a busy window was the endpoint's dominant cost.
func latestConnectionIntervals(ctx context.Context, h storage.HistoryRepository, from, to time.Time) ([]storage.ConnectionInterval, error) {
	if agg, ok := h.(storage.LatestConnectionIntervaler); ok {
		return agg.LatestConnectionIntervals(ctx, from, to)
	}
	rows, err := h.ConnectionsBetween(ctx, from, to)
	if err != nil {
		return nil, err
	}
	newest := map[string]storage.ConnectionInterval{}
	for _, r := range rows {
		if cur, ok := newest[r.ConnectionID]; !ok || r.LastSeen.After(cur.LastSeen) {
			newest[r.ConnectionID] = r
		}
	}
	out := make([]storage.ConnectionInterval, 0, len(newest))
	for _, r := range newest {
		out = append(out, r)
	}
	return out, nil
}
