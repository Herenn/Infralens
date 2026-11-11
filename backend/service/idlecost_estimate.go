package service

import (
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// idleInputs is everything the estimator reads, gathered up front so the
// policy itself is a pure function that tests can drive directly.
type idleInputs struct {
	now             time.Time
	services        []storage.Service
	connections     []storage.Connection
	metrics         map[string]storage.NodeMetrics
	historyEnabled  bool
	maxGap          time.Duration
	coverageStart   time.Time // earliest instant the evidence window covers
	window          time.Duration
	historyEdges    []storage.ConnectionInterval // newest interval per connection in the window
	historyServices []storage.ServiceInterval    // every service interval in the window
	historyNodes    []storage.NodeInterval       // agent heartbeat intervals (observation coverage)
	retireAfter     time.Duration                // silence after which a node counts as retired (coverage.go)
	stale           []storage.ServiceInterval
	listenPorts     func(id string) []int // TCP listening ports from inspection; may be nil
	groups          map[string][]string   // CLUSTER_NAME group -> agent node names
}

// serverLikeTypes are fingerprint types whose purpose is to be connected to.
var serverLikeTypes = map[string]bool{
	"web_server":    true,
	"database":      true,
	"cache":         true,
	"message_queue": true,
	"proxy":         true,
}

var dataStoreTypes = map[string]bool{"database": true, "cache": true, "message_queue": true}

type idleCandidate struct {
	svc       storage.Service
	inGraph   bool
	category  string
	firstSeen time.Time
	lastSeen  time.Time

	lastInbound     *time.Time
	quiet           time.Duration // observed
	quietWall       time.Duration
	quietLowerBound bool

	liveIn, liveOut int
	outPeers        []string
	outBytes        uint64
	inBytes         uint64
	ignoredCallers  []string
	callers         []string // counted inbound sources (IDs) in the window
	idleCallers     []string
	viaAddress      string
	udp             bool

	idleConf         string
	inTotal          bool
	notInTotalReason string // when !inTotal
	silentNote       string // an agent in its group was out
}

type edgeObs struct {
	source, target string
	port           uint16
	udp            bool
	last           time.Time
	live           bool
	bytes          uint64
}

func estimateIdleSpend(in idleInputs, cfg IdleCostConfig, q IdleSpendQuery) *IdleSpendReport {
	now := in.now
	report := &IdleSpendReport{
		GeneratedAt:    now,
		Beta:           true,
		Disclaimer:     IdleDisclaimer,
		Currency:       cfg.Currency,
		CurrencySymbol: currencySymbol(cfg.Currency),
		Items:          []IdleServiceItem{},
		Nodes:          []IdleNodeSummary{},
		Excluded:       []IdleExclusion{},
		Notes:          []string{},
		SilentAgents:   []IdleSilentAgent{},
		Totals:         IdleSpendTotals{MonthlyByConfidence: map[string]float64{ConfidenceHigh: 0, ConfidenceMedium: 0, ConfidenceLow: 0}},
	}
	report.Pricing = pricingSummary(cfg, q)
	listenPorts := in.listenPorts
	if listenPorts == nil {
		listenPorts = func(string) []int { return nil }
	}

	// --- Index ---------------------------------------------------------------

	known := make(map[string]storage.Service, len(in.services))
	for _, s := range in.services {
		known[s.ID] = s
	}
	meta := make(map[string]storage.Service, len(known))
	firstSeen := map[string]time.Time{}
	for _, si := range in.historyServices {
		if _, ok := meta[si.ServiceID]; !ok {
			meta[si.ServiceID] = storage.Service{ID: si.ServiceID, Name: si.Name, Type: si.Type, Tech: si.Tech, Node: si.Node, Namespace: si.Namespace}
		}
		if fs, ok := firstSeen[si.ServiceID]; !ok || si.FirstSeen.Before(fs) {
			firstSeen[si.ServiceID] = si.FirstSeen
		}
	}
	for id, s := range known {
		meta[id] = s
	}
	nodeOf := func(id string) string { return meta[id].Node }

	var edges []edgeObs
	for _, c := range in.connections {
		edges = append(edges, edgeObs{source: c.SourceID, target: c.TargetID, port: c.Port, udp: c.Protocol == "udp", last: c.LastSeen, live: true, bytes: c.BytesSent + c.BytesRecv})
	}
	for _, c := range in.historyEdges {
		edges = append(edges, edgeObs{source: c.SourceID, target: c.TargetID, port: c.Port, udp: c.Protocol == "udp", last: c.LastSeen})
	}
	inbound := map[string][]edgeObs{}
	outbound := map[string][]edgeObs{}
	// recentEndpoint: either end of a live connection, or of one last seen
	// within QuietAfter. Such a service is alive and in use, whatever its own
	// service row or history says (long-lived pools make no new connects).
	recentEndpoint := map[string]time.Time{}
	for _, e := range edges {
		if e.source == e.target {
			continue
		}
		inbound[e.target] = append(inbound[e.target], e)
		outbound[e.source] = append(outbound[e.source], e)
		if e.live || now.Sub(e.last) < cfg.QuietAfter {
			for _, id := range []string{e.source, e.target} {
				if t, ok := recentEndpoint[id]; !ok || e.last.After(t) {
					recentEndpoint[id] = e.last
				}
			}
		}
	}

	addrInbound := map[string][]edgeObs{}
	for target, es := range inbound {
		if s, ok := meta[target]; ok && s.Node != "" {
			continue
		}
		host, _, err := net.SplitHostPort(target)
		if err != nil {
			continue
		}
		addrInbound[host] = append(addrInbound[host], es...)
	}

	excluded := []IdleExclusion{}
	exclude := func(s storage.Service, reason, detail string) {
		excluded = append(excluded, IdleExclusion{ID: s.ID, Name: displayName(s), Node: s.Node, Reason: reason, Detail: detail})
	}

	// --- Evidence window -----------------------------------------------------

	coverageStart := in.coverageStart
	if !in.historyEnabled || coverageStart.IsZero() {
		coverageStart = now
	}
	in.coverageStart = coverageStart
	obs := buildIdleCoverage(in, nodeOf)
	// Observed: the global coverage - and, with CLUSTER_NAME groups, only
	// while every group carrying services was observed too: one live member
	// does not make a group with a silent one observed (coverage.go).
	observedCov := obs.globalCoverage()
	groupsInUse := map[string]bool{}
	for _, s := range in.services {
		if s.Node != "" && obs.isGroup(s.Node) && !groupsInUse[s.Node] {
			groupsInUse[s.Node] = true
			observedCov = observedCov.intersect(obs.nodeCoverage(s.Node))
		}
	}
	observedTotal := observedCov.covered(coverageStart, now)
	report.Evidence = IdleEvidenceWindow{
		HistoryEnabled:    in.historyEnabled,
		From:              coverageStart,
		To:                now,
		ObservedSeconds:   int64(observedTotal.Seconds()),
		UnobservedSeconds: int64((now.Sub(coverageStart) - observedTotal).Seconds()),
		WindowSeconds:     int64(in.window.Seconds()),
		QuietAfterSeconds: int64(cfg.QuietAfter.Seconds()),
		WarmupSeconds:     int64(cfg.Warmup.Seconds()),
	}
	if in.historyEnabled && observedTotal < cfg.Warmup {
		report.Evidence.WarmingUp = true
		report.Notes = append(report.Notes, fmt.Sprintf(
			"InfraLens has only observed %s of history; idle detection starts once it covers %s (IDLE_COST_WARMUP).",
			humanDuration(observedTotal), humanDuration(cfg.Warmup)))
		finishIdleReport(report, nil, excluded, q.Limit)
		return report
	}
	if gap := now.Sub(coverageStart) - observedTotal; in.historyEnabled && gap > time.Hour {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"InfraLens was not observing for %s of the last %s (backend or agent downtime). That time is never counted as silence.",
			humanDuration(gap), humanDuration(now.Sub(coverageStart))))
	}
	if !in.historyEnabled {
		report.Notes = append(report.Notes,
			"Topology history is disabled (HISTORY_ENABLED=false): quiet periods cannot be measured, so only services with no connections at all are listed, at low confidence, and none of them count towards the total.")
	}

	quietOf := func(node string, start time.Time) (observed, wall time.Duration) {
		wall = now.Sub(start)
		if !in.historyEnabled {
			return 0, wall
		}
		var nodes []string
		if node != "" {
			nodes = []string{node}
		}
		return obs.observed(nodes, start, now), wall
	}

	// --- First pass: direct evidence -----------------------------------------

	idle := map[string]*idleCandidate{}
	var order []string
	pending := map[string]*idleCandidate{}

	for _, s := range in.services {
		if s.Node == "" || isExternalService(s) {
			continue
		}
		if isInfraLensService(s) {
			exclude(s, IdleExcludedInfraLens, "InfraLens's own component")
			continue
		}
		if isBareIPService(s) {
			continue // host-level daemons (sshd, systemd) and unlabelled peers
		}
		if pat, ok := matchGlobs(cfg.Exclude, s.Name, s.ID, s.DisplayName); ok {
			exclude(s, IdleExcludedUser, fmt.Sprintf("matches IDLE_COST_EXCLUDE pattern %q", pat))
			continue
		}

		c := &idleCandidate{svc: s, inGraph: true, lastSeen: s.LastSeen}
		c.firstSeen = s.CreatedAt
		if fs, ok := firstSeen[s.ID]; ok && (c.firstSeen.IsZero() || fs.Before(c.firstSeen)) {
			c.firstSeen = fs
		}

		udp := false
		peers := map[string]bool{}
		replicaPeer := ""
		for _, e := range outbound[s.ID] {
			udp = udp || e.udp
			if e.live {
				if _, ok := known[e.target]; ok {
					c.liveOut++
				}
				c.outBytes += e.bytes
			}
			peer := metaOr(meta, e.target)
			peers[displayName(peer)] = true
			if replicaPeer == "" && looksLikeReplicaPeer(s, peer, e.port, listenPorts) {
				replicaPeer = displayName(peer)
			}
		}
		for p := range peers {
			c.outPeers = append(c.outPeers, p)
		}
		sort.Strings(c.outPeers)

		var lastUse *time.Time
		ignored := map[string]bool{}
		callers := map[string]bool{}
		for _, e := range inbound[s.ID] {
			udp = udp || e.udp
			src := metaOr(meta, e.source)
			if isIgnoredCaller(src, s) {
				ignored[displayName(src)] = true
				continue
			}
			if e.live {
				if _, ok := known[e.source]; ok {
					c.liveIn++
				}
				c.inBytes += e.bytes
			}
			callers[e.source] = true
			t := e.last
			if lastUse == nil || t.After(*lastUse) {
				lastUse = &t
			}
		}
		c.ignoredCallers = sortedKeys(ignored)
		c.callers = sortedKeys(callers)
		c.udp = udp

		// Reached by address (or, for a k8s workload, via its Service).
		var lastAddr *time.Time
		var addrTarget string
		consider := func(e edgeObs, target string) {
			if isIgnoredCaller(metaOr(meta, e.source), s) {
				return
			}
			t := e.last
			if lastAddr == nil || t.After(*lastAddr) {
				lastAddr, addrTarget = &t, target
			}
		}
		if svcID := k8sServiceAlias(s.ID); svcID != "" {
			for _, e := range inbound[svcID] {
				consider(e, svcID)
			}
		}
		if ip := serviceIP(s); ip != "" {
			var ports []int
			for _, e := range addrInbound[ip] {
				// Inspection only lists TCP listeners, so it can narrow TCP
				// edges but says nothing about UDP ones.
				if !e.udp {
					if ports == nil {
						ports = append(listenPorts(s.ID), []int{}...)
					}
					if len(ports) > 0 && !containsInt(ports, int(e.port)) {
						continue
					}
				}
				consider(e, e.target)
			}
		}

		if cfg.Warmup > 0 && !c.firstSeen.IsZero() && now.Sub(c.firstSeen) < cfg.Warmup {
			if lastUse == nil && c.liveIn == 0 {
				exclude(s, IdleExcludedWarmup, fmt.Sprintf("first seen %s ago (warmup %s)", humanDuration(now.Sub(c.firstSeen)), humanDuration(cfg.Warmup)))
			}
			continue
		}

		if lastAddr != nil && (lastUse == nil || lastAddr.After(*lastUse)) {
			if now.Sub(*lastAddr) < cfg.QuietAfter || !in.historyEnabled {
				exclude(s, IdleExcludedReachedByAddr, fmt.Sprintf(
					"no inbound under its own ID, but %s was reached %s ago - probably the same process reached by address",
					addrTarget, humanDuration(now.Sub(*lastAddr))))
				continue
			}
			lastUse = lastAddr
			c.viaAddress = addrTarget
		}

		c.lastInbound = lastUse
		if c.liveIn > 0 || (len(callers) > 0 && lastUse != nil && now.Sub(*lastUse) < cfg.QuietAfter) {
			pending[s.ID] = c // in use - unless every caller turns out to be idle
			continue
		}

		start := coverageStart
		if lastUse != nil {
			start = *lastUse
		} else {
			c.quietLowerBound = true
			if c.firstSeen.After(start) {
				start = c.firstSeen
			}
		}
		c.quiet, c.quietWall = quietOf(s.Node, start)

		hasLiveEdges := c.liveIn > 0 || c.liveOut > 0 || liveInboundFromKnown(inbound[s.ID], known)
		everOutbound := len(outbound[s.ID]) > 0
		switch {
		case udp:
			exclude(s, IdleExcludedUDP, "has UDP traffic - InfraLens cannot see a receive-only UDP server being used, so silence proves nothing")
			continue
		case !hasLiveEdges && !everOutbound:
			c.category = IdleCategoryOrphan
		case everOutbound && s.Type == "monitoring":
			exclude(s, IdleExcludedMonitoring, "monitoring/scraper - talks outbound by design; being queried rarely is normal")
			continue
		case everOutbound && dataStoreTypes[s.Type] && replicaPeer != "":
			exclude(s, IdleExcludedReplica, fmt.Sprintf("a %s whose only traffic is outbound to %s, a peer of the same kind - possibly a replica or standby, which is idle by design", s.Type, replicaPeer))
			continue
		case everOutbound && !serverLikeTypes[s.Type] && lastUse == nil:
			exclude(s, IdleExcludedOutboundOnly, "only makes outbound connections and has never been called - looks like a worker, cron job or client (a metrics or health port does not make it a server)")
			continue
		case !hasLiveEdges:
			c.category = IdleCategoryOrphan
		case lastUse == nil:
			c.category = IdleCategoryNoInbound
		default:
			c.category = IdleCategoryQuiet
		}

		if !in.historyEnabled {
			if c.category != IdleCategoryOrphan {
				exclude(s, IdleExcludedNoHistory, "no inbound in the current window, but with history disabled InfraLens cannot tell how long it has been quiet")
				continue
			}
			c.idleConf = ConfidenceLow
			idle[s.ID] = c
			order = append(order, s.ID)
			continue
		}
		if c.quiet < cfg.QuietAfter {
			exclude(s, IdleExcludedNotQuietEnough, notQuietDetail(c, cfg))
			continue
		}
		c.idleConf = quietConfidence(c.quiet, cfg.QuietAfter)
		if c.viaAddress != "" {
			c.idleConf = downgrade(c.idleConf)
		}
		c.inTotal = true
		idle[s.ID] = c
		order = append(order, s.ID)
	}

	// --- Decommission candidates (history only) ------------------------------

	alive := map[string]storage.Service{} // history-only services that still carry traffic
	for _, si := range in.stale {
		if _, live := known[si.ServiceID]; live {
			continue
		}
		s := storage.Service{ID: si.ServiceID, Name: si.Name, Type: si.Type, Tech: si.Tech, Namespace: si.Namespace, Node: si.Node, LastSeen: si.LastSeen}
		if isExternalService(s) || isBareIPService(s) {
			continue
		}
		if t, ok := recentEndpoint[s.ID]; ok {
			if s.Node != "" && !isInfraLensService(s) {
				alive[s.ID] = s
			}
			exclude(s, IdleExcludedLiveTraffic, fmt.Sprintf(
				"no new connections since %s, but a connection it is part of carried traffic %s ago - a long-lived connection (pool, channel, consumer); it is in use",
				si.LastSeen.UTC().Format("2006-01-02"), humanDuration(now.Sub(t))))
			continue
		}
		_, servicePriced := matchPriceRule(cfg.ServicePrices, s.Name, s.ID)
		if s.Node == "" && !servicePriced {
			continue
		}
		if isInfraLensService(s) {
			exclude(s, IdleExcludedInfraLens, "InfraLens's own component")
			continue
		}
		if pat, ok := matchGlobs(cfg.Exclude, s.Name, s.ID); ok {
			exclude(s, IdleExcludedUser, fmt.Sprintf("matches IDLE_COST_EXCLUDE pattern %q", pat))
			continue
		}
		if s.Node != "" && !nodeReporting(in, s.Node) {
			exclude(s, IdleExcludedNodeGone, fmt.Sprintf("node %s no longer reports metrics or services - it may already be gone, so there is nothing left to save", s.Node))
			continue
		}
		c := &idleCandidate{svc: s, category: IdleCategoryStale, firstSeen: firstSeen[s.ID], lastSeen: si.LastSeen, quietLowerBound: true}
		c.quiet, c.quietWall = quietOf(s.Node, si.LastSeen)
		if c.quiet < cfg.QuietAfter {
			exclude(s, IdleExcludedNotQuietEnough, notQuietDetail(c, cfg))
			continue
		}
		c.idleConf = quietConfidence(c.quiet, cfg.QuietAfter)
		c.inTotal = true
		idle[s.ID] = c
		order = append(order, s.ID)
	}

	// --- Second pass: reached only by flagged services -----------------------

	for changed := true; changed; {
		changed = false
		for _, id := range sortedCandidateKeys(pending) {
			c := pending[id]
			if len(c.callers) == 0 || c.udp {
				continue
			}
			conf := ConfidenceMedium
			var minQuiet, minWall time.Duration = -1, -1
			var names []string
			all := true
			for _, caller := range c.callers {
				ic, ok := idle[caller]
				if !ok {
					all = false
					break
				}
				names = append(names, displayName(ic.svc))
				conf = minConfidence(conf, ic.idleConf)
				if minQuiet < 0 || ic.quiet < minQuiet {
					minQuiet, minWall = ic.quiet, ic.quietWall
				}
			}
			if !all {
				continue
			}
			sort.Strings(names)
			c.category = IdleCategoryIdleCallersOnly
			c.idleCallers = names
			c.lastInbound = nil
			// It has been unused for as long as its most recently active
			// caller has been idle - no longer.
			c.quiet, c.quietWall, c.quietLowerBound = minQuiet, minWall, false
			c.idleConf = conf
			c.inTotal = in.historyEnabled
			idle[id] = c
			order = append(order, id)
			delete(pending, id)
			changed = true
		}
	}

	// --- Group member outages -----------------------------------------------
	//
	// A member silent for longer than the member timeout (or flapping) no
	// longer holds its group's coverage back (coverage.go), but what only its
	// agent could see went unseen meanwhile: its services may look quiet. If
	// any member of a group was out like that anywhere in the evidence window
	// - still out, or back by now - that group's candidates are low
	// confidence and left out of the total.
	if in.historyEnabled {
		outages := obs.memberOutages(coverageStart, now)
		byGroup := map[string][]memberOutage{}
		var groupOrder []string
		for _, o := range outages {
			if byGroup[o.Group] == nil {
				groupOrder = append(groupOrder, o.Group)
			}
			byGroup[o.Group] = append(byGroup[o.Group], o)
			report.SilentAgentsTotal++
			if o.To.IsZero() {
				report.SilentAgentsStillOut++
			}
			if len(report.SilentAgents) < maxIdleSilentAgents {
				a := IdleSilentAgent{Group: o.Group, Node: o.Node, LastSeen: o.LastSeen, SilentSince: o.From, Intermittent: o.Intermittent}
				if !o.To.IsZero() {
					to := o.To
					a.ReturnedAt = &to
				}
				report.SilentAgents = append(report.SilentAgents, a)
			}
		}
		sort.Strings(groupOrder)
		var silentNotes []string
		for _, g := range groupOrder {
			agents := byGroup[g]
			affected := 0
			for _, id := range order {
				c := idle[id]
				if c.svc.Node != g {
					continue
				}
				affected++
				c.idleConf = ConfidenceLow
				c.inTotal = false
				c.notInTotalReason = NotInTotalSilentAgent
				c.silentNote = fmt.Sprintf("Low confidence, not in the total: in CLUSTER_NAME group %q, %s - what only it could see went unseen, so this may be in use.",
					g, outagePhrase(agents[0], now))
			}
			silentNotes = append(silentNotes, outageGroupNote(g, agents, affected, now, coverageStart))
		}
		report.Notes = append(silentNotes, report.Notes...)
		report.SilentNotes = len(silentNotes)
	}

	// --- Cost attribution ----------------------------------------------------

	members := map[string][]string{} // node/group -> member IDs
	memberSvc := map[string]storage.Service{}
	addMember := func(s storage.Service) {
		if s.Node == "" || isExternalService(s) || isInfraLensService(s) || isBareIPService(s) {
			return
		}
		if _, dup := memberSvc[s.ID]; dup {
			return
		}
		memberSvc[s.ID] = s
		members[s.Node] = append(members[s.Node], s.ID)
	}
	for _, s := range in.services {
		addMember(s)
	}
	for _, s := range alive {
		addMember(s)
	}
	for _, id := range order {
		addMember(idle[id].svc)
	}

	costs := map[string]*IdleCostEstimate{}
	costConf := map[string]string{}
	costNotes := map[string]string{}
	var summaries []IdleNodeSummary
	sym := currencySymbol(cfg.Currency)

	nodeNames := make([]string, 0, len(members))
	for n := range members {
		nodeNames = append(nodeNames, n)
	}
	sort.Strings(nodeNames)
	clustered := false
	for _, node := range nodeNames {
		ids := members[node]
		sort.Strings(ids)
		agents := in.groups[node]
		isCluster := len(agents) > 0
		clustered = clustered || isCluster

		price, source, rule, priced := groupPrice(node, agents, cfg, q)

		var ruleIDs, otherIDs []string
		flagged := 0
		for _, id := range ids {
			if _, ok := idle[id]; ok {
				flagged++
			}
			s := memberSvc[id]
			if _, ok := matchPriceRule(cfg.ServicePrices, s.Name, s.ID, s.DisplayName); ok {
				ruleIDs = append(ruleIDs, id)
			} else {
				otherIDs = append(otherIDs, id)
			}
		}
		fullyIdle := flagged == len(ids) && len(ids) > 0

		remaining := price
		for _, id := range ruleIDs {
			s := memberSvc[id]
			r, _ := matchPriceRule(cfg.ServicePrices, s.Name, s.ID, s.DisplayName)
			amount := r.Monthly
			method := ShareDirect
			expl := fmt.Sprintf("%s%s/mo set for this service (IDLE_COST_SERVICE_PRICES %q)", sym, formatMoney(r.Monthly), r.Pattern)
			if priced {
				method = ShareCarveOut
				if amount > remaining {
					amount = math.Max(remaining, 0)
					report.Pricing.Warnings = append(report.Pricing.Warnings, fmt.Sprintf(
						"service prices on %s add up to more than its price (%s%s/mo); %s capped at %s%s so nothing is counted twice",
						node, sym, formatMoney(price), displayName(s), sym, formatMoney(amount)))
				}
				remaining -= amount
				expl += fmt.Sprintf(", carved out of the %s%s/mo for %s", sym, formatMoney(price), node)
			}
			if _, ok := idle[id]; ok {
				m := floorCents(amount)
				costs[id] = &IdleCostEstimate{Monthly: m, Annual: round2(m * 12), NodeMonthly: round2(price), Share: 1, SharedWith: 1,
					PriceSource: PriceSourceServiceRule, PriceRule: r.Pattern, ShareMethod: method, Explanation: expl}
				costConf[id] = ConfidenceHigh
			}
		}

		var idleMonthly float64
		if priced && len(otherIDs) > 0 {
			per := floorCents(math.Max(remaining, 0) / float64(len(otherIDs)))
			method := ShareEvenSplit
			switch {
			case isCluster:
				method = ShareClusterSplit
			case len(otherIDs) == 1, fullyIdle:
				method = ShareWholeNode
			}
			for _, id := range otherIDs {
				if _, ok := idle[id]; !ok {
					continue
				}
				expl := costExplanation(sym, node, price, remaining, len(otherIDs), len(ruleIDs) > 0, source, rule, method, agents)
				costs[id] = &IdleCostEstimate{Monthly: per, Annual: round2(per * 12), NodeMonthly: round2(price),
					Share: math.Round(10000/float64(len(otherIDs))) / 10000, SharedWith: len(otherIDs),
					PriceSource: source, PriceRule: rule, ShareMethod: method, Explanation: expl}
				switch {
				case isCluster || source == PriceSourceAssumed || method == ShareEvenSplit:
					costConf[id] = ConfidenceLow
				case source == PriceSourceNodeRule:
					costConf[id] = ConfidenceHigh
				default:
					costConf[id] = ConfidenceMedium
				}
			}
		}
		var idleNotInTotal float64
		for _, id := range ids {
			if c, ok := costs[id]; ok {
				if idle[id].inTotal {
					idleMonthly += c.Monthly
				} else {
					idleNotInTotal += c.Monthly
				}
			}
		}

		sum := IdleNodeSummary{Node: node, Cluster: isCluster, AgentNodes: agents, Services: len(ids), IdleServices: flagged,
			IdleMonthly: round2(idleMonthly), IdleMonthlyNotInTotal: round2(idleNotInTotal), FullyIdle: fullyIdle && !isCluster}
		if priced {
			p := round2(price)
			sum.Monthly, sum.PriceSource, sum.PriceRule = &p, source, rule
		}
		if m, ok := in.metrics[node]; ok {
			sum.Reporting = now.Sub(m.LastSeen) < idleNodeAliveWindow
			cpu, mem := m.CPUPercent, m.MemPercent
			sum.CPUPercent, sum.MemPercent = &cpu, &mem
		}
		summaries = append(summaries, sum)

		if isCluster {
			note := fmt.Sprintf("Agents report under CLUSTER_NAME=%q, which groups %d node%s (%s): InfraLens cannot tell which node a service runs on, so there is no per-node figure.",
				node, len(agents), idlePlural(len(agents)), strings.Join(firstN(agents, 5), ", "))
			if priced {
				note += fmt.Sprintf(" Costs shown are the whole cluster's %s%s/mo split across its %d services (low confidence).", sym, formatMoney(price), len(ids))
			} else {
				note += " No cost is shown because not every node in it has a price."
			}
			note += " Leave the agents' cluster name unset (Helm agent.clusterName: \"\") for per-node attribution."
			report.Notes = append(report.Notes, note)
		} else if fullyIdle && priced {
			report.Notes = append(report.Notes, fmt.Sprintf(
				"Every service observed on %s is flagged - the whole node (~%s%s/mo) is a candidate to verify.", node, sym, formatMoney(price)))
		}
	}
	report.Evidence.ClusterGrouped = clustered

	// Node-less endpoints priced by a service rule (managed databases).
	for _, id := range order {
		c := idle[id]
		if c.svc.Node != "" {
			continue
		}
		if r, ok := matchPriceRule(cfg.ServicePrices, c.svc.Name, c.svc.ID, c.svc.DisplayName); ok {
			m := floorCents(r.Monthly)
			costs[id] = &IdleCostEstimate{Monthly: m, Annual: round2(m * 12), Share: 1, SharedWith: 1, PriceSource: PriceSourceServiceRule,
				PriceRule: r.Pattern, ShareMethod: ShareDirect,
				Explanation: fmt.Sprintf("%s%s/mo set for this service (IDLE_COST_SERVICE_PRICES %q)", sym, formatMoney(r.Monthly), r.Pattern)}
			costConf[id] = ConfidenceHigh
		}
	}

	report.Pricing.Warnings = append(report.Pricing.Warnings, unmatchedNodeRuleWarnings(cfg, in, members)...)

	// --- Items ---------------------------------------------------------------

	items := make([]IdleServiceItem, 0, len(order))
	for _, id := range order {
		c := idle[id]
		item := IdleServiceItem{
			ID: c.svc.ID, Name: displayName(c.svc), Type: c.svc.Type, Tech: c.svc.Tech, Namespace: c.svc.Namespace, Node: c.svc.Node,
			InGraph: c.inGraph, Category: c.category, IdleConfidence: c.idleConf, InTotal: c.inTotal,
		}
		if !c.inTotal {
			item.NotInTotalReason = c.notInTotalReason
			if item.NotInTotalReason == "" {
				item.NotInTotalReason = NotInTotalCurrentWindow // the only other cause: history disabled
			}
		}
		item.Evidence = buildEvidence(c, in)
		if c.silentNote != "" {
			item.Evidence.Notes = append([]string{c.silentNote}, item.Evidence.Notes...)
		}
		est := costs[id]
		cc := costConf[id]
		if est != nil && c.category == IdleCategoryStale {
			cc = ConfidenceLow
			costNotes[id] = "Cost confidence is low: it may already have been stopped, in which case nothing is left to save."
		}
		if note := costNotes[id]; note != "" {
			item.Evidence.Notes = append(item.Evidence.Notes, note)
		}
		item.Cost = est
		if est == nil {
			item.CostConfidence = ConfidenceNone
			item.Confidence = item.IdleConfidence
		} else {
			item.CostConfidence = cc
			item.Confidence = minConfidence(item.IdleConfidence, cc)
			item.Score = round2(est.Monthly * confidenceWeight(item.Confidence))
		}
		items = append(items, item)
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].IdleMonthly != summaries[j].IdleMonthly {
			return summaries[i].IdleMonthly > summaries[j].IdleMonthly
		}
		if summaries[i].IdleMonthlyNotInTotal != summaries[j].IdleMonthlyNotInTotal {
			return summaries[i].IdleMonthlyNotInTotal > summaries[j].IdleMonthlyNotInTotal
		}
		return summaries[i].Node < summaries[j].Node
	})
	if summaries != nil {
		report.Nodes = summaries
	}
	if !report.Pricing.Configured && report.Pricing.AssumedNodeMonthly == nil && len(items) > 0 {
		report.Notes = append(report.Notes,
			"No prices configured, so no costs are shown. Set IDLE_COST_NODE_MONTHLY (and optionally IDLE_COST_NODE_PRICES / IDLE_COST_SERVICE_PRICES), or pass ?assume_node_monthly= for a labelled what-if.")
	}

	finishIdleReport(report, items, excluded, q.Limit)
	return report
}

// maxIdleSilentAgents caps IdleSpendReport.SilentAgents; notes name at most
// maxIdleSilentNamed agents per group.
const (
	maxIdleSilentAgents = 20
	maxIdleSilentNamed  = 3
)

// outagePhrase says what happened to one member's agent, e.g. "the agent on
// node-a stopped reporting 3d ago", "the agent on node-a was not reporting
// from 2026-11-01 10:00 UTC to 2026-11-05 08:00 UTC (3d 22h)".
func outagePhrase(o memberOutage, now time.Time) string {
	const layout = "2006-01-02 15:04 MST"
	from := o.From.UTC().Format(layout)
	switch {
	case o.To.IsZero() && !o.Intermittent:
		return fmt.Sprintf("the agent on %s stopped reporting %s ago", o.Node, humanDuration(now.Sub(o.From)))
	case o.To.IsZero():
		return fmt.Sprintf("the agent on %s has reported only intermittently since %s (%s)", o.Node, from, humanDuration(now.Sub(o.From)))
	case !o.Intermittent:
		return fmt.Sprintf("the agent on %s was not reporting from %s to %s (%s)", o.Node, from, o.To.UTC().Format(layout), humanDuration(o.To.Sub(o.From)))
	default:
		return fmt.Sprintf("the agent on %s reported only intermittently from %s to %s (%s)", o.Node, from, o.To.UTC().Format(layout), humanDuration(o.To.Sub(o.From)))
	}
}

func outageGroupNote(group string, outages []memberOutage, affected int, now, windowStart time.Time) string {
	var parts []string
	ongoing := 0
	for i, o := range outages {
		if o.To.IsZero() {
			ongoing++
		}
		if i < maxIdleSilentNamed {
			parts = append(parts, outagePhrase(o, now))
		}
	}
	list := strings.Join(parts, "; ")
	if more := len(outages) - len(parts); more > 0 {
		list += fmt.Sprintf("; and %d more", more)
	}
	pron := "it"
	if len(outages) > 1 {
		pron = "they"
	}
	note := fmt.Sprintf("In CLUSTER_NAME group %q, %s - services only %s could see may be listed wrongly. ", group, list, pron)
	switch affected {
	case 0:
		note += "None of this group's services is flagged now; any that are while this lasts are low confidence and not in the total."
	case 1:
		note += "The 1 candidate in this group is low confidence and not in the total."
	default:
		note += fmt.Sprintf("The %d candidates in this group are low confidence and not in the total.", affected)
	}
	window := humanDuration(now.Sub(windowStart))
	switch {
	case ongoing == 1 && len(outages) == 1:
		note += fmt.Sprintf(" Restore the agent or remove the node; for a node that is gone this clears once its last report is older than the %s evidence window.", window)
	case ongoing > 0:
		note += fmt.Sprintf(" Restore the agents or remove the nodes; this clears once every outage listed is older than the %s evidence window.", window)
	case len(outages) == 1:
		note += fmt.Sprintf(" The agent is reporting again; this clears once the outage is older than the %s evidence window.", window)
	default:
		note += fmt.Sprintf(" The agents are reporting again; this clears once the outages are older than the %s evidence window.", window)
	}
	return note
}

func notQuietDetail(c *idleCandidate, cfg IdleCostConfig) string {
	d := fmt.Sprintf("no inbound for %s of observed time so far; flagged after %s (IDLE_COST_QUIET_AFTER)", humanDuration(c.quiet), humanDuration(cfg.QuietAfter))
	if gap := c.quietWall - c.quiet; gap > time.Hour {
		d += fmt.Sprintf(" - InfraLens was not observing for %s of the last %s, which is not counted", humanDuration(gap), humanDuration(c.quietWall))
	}
	return d
}

func costExplanation(sym, node string, price, remaining float64, n int, carved bool, source, rule, method string, agents []string) string {
	desc := map[string]string{
		PriceSourceNodeRule: fmt.Sprintf("node rule %q", rule),
		PriceSourceDefault:  "IDLE_COST_NODE_MONTHLY",
		PriceSourceAssumed:  "assumed for this view",
		PriceSourceCluster:  fmt.Sprintf("sum of %d node prices", len(agents)),
	}[source]
	base := fmt.Sprintf("%s%s/mo", sym, formatMoney(price))
	if carved {
		base = fmt.Sprintf("%s%s/mo left of %s%s/mo after service prices", sym, formatMoney(remaining), sym, formatMoney(price))
	}
	switch method {
	case ShareClusterSplit:
		return fmt.Sprintf("%s for cluster group %s (%s) ÷ %d services observed in it - which node it runs on is unknown", base, node, desc, n)
	case ShareWholeNode:
		if n == 1 {
			return fmt.Sprintf("%s node (%s); the only service observed on %s", base, desc, node)
		}
		return fmt.Sprintf("%s node (%s) ÷ %d services observed on %s - all of them flagged", base, desc, n, node)
	default:
		return fmt.Sprintf("%s node (%s) ÷ %d services observed on %s", base, desc, n, node)
	}
}

// groupPrice prices a node, or a CLUSTER_NAME group: a rule naming the group
// prices it directly, otherwise it is the sum of its agent nodes' prices,
// and unpriced if any of them is.
func groupPrice(node string, agents []string, cfg IdleCostConfig, q IdleSpendQuery) (float64, string, string, bool) {
	if len(agents) == 0 {
		p, src, rule := resolveNodePrice(node, cfg, q)
		return p, src, rule, src != ""
	}
	if r, ok := matchPriceRule(cfg.NodePrices, node); ok {
		return r.Monthly, PriceSourceNodeRule, r.Pattern, true
	}
	total := 0.0
	assumed := false
	for _, a := range agents {
		p, src, _ := resolveNodePrice(a, cfg, q)
		if src == "" {
			return 0, "", "", false
		}
		assumed = assumed || src == PriceSourceAssumed
		total += p
	}
	if assumed {
		return total, PriceSourceAssumed, "", true
	}
	return total, PriceSourceCluster, "", true
}

func resolveNodePrice(node string, cfg IdleCostConfig, q IdleSpendQuery) (price float64, source, rule string) {
	if r, ok := matchPriceRule(cfg.NodePrices, node); ok {
		return r.Monthly, PriceSourceNodeRule, r.Pattern
	}
	if cfg.DefaultNodeMonthly > 0 {
		return cfg.DefaultNodeMonthly, PriceSourceDefault, ""
	}
	if q.AssumeNodeMonthly > 0 {
		return q.AssumeNodeMonthly, PriceSourceAssumed, ""
	}
	return 0, "", ""
}

// unmatchedNodeRuleWarnings flags node rules that match no observed name -
// the silent failure mode of a typo, or of agents reporting under a cluster
// name.
func unmatchedNodeRuleWarnings(cfg IdleCostConfig, in idleInputs, members map[string][]string) []string {
	names := map[string]bool{}
	for n := range members {
		names[n] = true
	}
	for n := range in.metrics {
		names[n] = true
	}
	for g, agents := range in.groups {
		names[g] = true
		for _, a := range agents {
			names[a] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	list := sortedKeys(names)
	var out []string
	for _, r := range cfg.NodePrices {
		matched := false
		for _, n := range list {
			if globMatch(r.Pattern, n) {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, fmt.Sprintf("IDLE_COST_NODE_PRICES pattern %q matches no observed node (observed: %s)", r.Pattern, strings.Join(firstN(list, 8), ", ")))
		}
	}
	return out
}

func finishIdleReport(report *IdleSpendReport, items []IdleServiceItem, excluded []IdleExclusion, limit int) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if (a.Cost != nil) != (b.Cost != nil) {
			return a.Cost != nil
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Evidence.QuietSeconds != b.Evidence.QuietSeconds {
			return a.Evidence.QuietSeconds > b.Evidence.QuietSeconds
		}
		return a.ID < b.ID
	})
	for _, it := range items {
		report.Totals.Services++
		if it.Cost == nil {
			report.Totals.Unpriced++
			continue
		}
		report.Totals.Priced++
		if !it.InTotal {
			report.Totals.NotInTotalMonthly += it.Cost.Monthly
			if report.Totals.NotInTotalByReason == nil {
				report.Totals.NotInTotalByReason = map[string]float64{}
			}
			report.Totals.NotInTotalByReason[it.NotInTotalReason] += it.Cost.Monthly
			continue
		}
		report.Totals.Monthly += it.Cost.Monthly
		report.Totals.MonthlyByConfidence[it.Confidence] += it.Cost.Monthly
	}
	report.Totals.Monthly = round2(report.Totals.Monthly)
	report.Totals.Annual = round2(report.Totals.Monthly * 12)
	report.Totals.NotInTotalMonthly = round2(report.Totals.NotInTotalMonthly)
	for k, v := range report.Totals.NotInTotalByReason {
		report.Totals.NotInTotalByReason[k] = round2(v)
	}
	for k, v := range report.Totals.MonthlyByConfidence {
		report.Totals.MonthlyByConfidence[k] = round2(v)
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	if items != nil {
		report.Items = items
	}
	sort.SliceStable(excluded, func(i, j int) bool {
		if excluded[i].Reason != excluded[j].Reason {
			return excluded[i].Reason < excluded[j].Reason
		}
		return excluded[i].ID < excluded[j].ID
	})
	report.ExcludedCount = len(excluded)
	if len(excluded) > maxIdleExcluded {
		excluded = excluded[:maxIdleExcluded]
	}
	report.Excluded = excluded
}

const periodicCaveat = "Silence does not rule out something that runs less often than InfraLens has been watching (monthly, quarterly, on incident) - verify with the owner."

func buildEvidence(c *idleCandidate, in idleInputs) IdleEvidence {
	ev := IdleEvidence{
		LastInbound:       c.lastInbound,
		QuietSeconds:      int64(c.quiet.Seconds()),
		QuietWallSeconds:  int64(c.quietWall.Seconds()),
		UnobservedSeconds: int64((c.quietWall - c.quiet).Seconds()),
		QuietIsLowerBound: c.quietLowerBound,
		LastSeen:          c.lastSeen,
		LiveInboundEdges:  c.liveIn,
		LiveOutboundEdges: c.liveOut,
		OutboundPeers:     c.outPeers,
		OutboundBytes:     c.outBytes,
		InboundBytes:      c.inBytes,
		IgnoredCallers:    c.ignoredCallers,
		Notes:             []string{},
	}
	if !c.firstSeen.IsZero() {
		fs := c.firstSeen
		ev.FirstSeen = &fs
	}
	if c.category == IdleCategoryIdleCallersOnly {
		ev.IdleCallers = c.idleCallers
	}
	observed := humanDuration(c.quiet) + " of observed time"
	switch c.category {
	case IdleCategoryStale:
		ev.Notes = append(ev.Notes, fmt.Sprintf("Not observed at all since %s (%s while InfraLens was watching).", c.lastSeen.UTC().Format("2006-01-02 15:04 MST"), humanDuration(c.quiet)))
		ev.Notes = append(ev.Notes, "No network activity also means InfraLens cannot tell whether the process still runs - verify before counting the saving.")
	case IdleCategoryOrphan:
		if !in.historyEnabled {
			ev.Notes = append(ev.Notes, "No connections in or out in the current window (history disabled: how long is unknown).")
		} else if c.lastInbound == nil {
			ev.Notes = append(ev.Notes, fmt.Sprintf("No connections in or out in %s.", observed))
		} else {
			ev.Notes = append(ev.Notes, fmt.Sprintf("No live connections; nothing has connected for %s.", observed))
		}
	case IdleCategoryNoInbound:
		ev.Notes = append(ev.Notes, fmt.Sprintf("No inbound connection in %s.", observed))
	case IdleCategoryQuiet:
		ev.Notes = append(ev.Notes, fmt.Sprintf("Last inbound connection %s (%s ago); %s of silence observed since.",
			c.lastInbound.UTC().Format("2006-01-02 15:04 MST"), humanDuration(c.quietWall), humanDuration(c.quiet)))
	case IdleCategoryIdleCallersOnly:
		ev.Notes = append(ev.Notes, fmt.Sprintf("Only reached by services that are themselves flagged: %s.", strings.Join(firstN(c.idleCallers, 3), ", ")))
	}
	if gap := c.quietWall - c.quiet; gap > time.Hour && in.historyEnabled {
		ev.Notes = append(ev.Notes, fmt.Sprintf("InfraLens was not observing this node for %s of that time (collection gap); it is not counted as silence.", humanDuration(gap)))
	}
	if c.category != IdleCategoryStale && len(c.outPeers) > 0 && c.liveOut > 0 {
		ev.Notes = append(ev.Notes, fmt.Sprintf("Still opens outbound connections (%s%s) - the process is alive and holding resources.",
			strings.Join(firstN(c.outPeers, 3), ", "), bytesSuffix(c.outBytes)))
	}
	if len(c.ignoredCallers) > 0 {
		ev.Notes = append(ev.Notes, fmt.Sprintf("Inbound from %s ignored (monitoring/health checks are not use).", strings.Join(firstN(c.ignoredCallers, 3), ", ")))
	}
	if c.viaAddress != "" {
		ev.Notes = append(ev.Notes, fmt.Sprintf("Last reached by address as %s - confidence reduced.", c.viaAddress))
	}
	if c.category != IdleCategoryStale {
		ev.Notes = append(ev.Notes, periodicCaveat)
	}
	return ev
}

func pricingSummary(cfg IdleCostConfig, q IdleSpendQuery) IdlePricingSummary {
	p := IdlePricingSummary{
		Configured:   cfg.Priced(),
		Source:       "none",
		NodeRules:    append([]PriceRule{}, cfg.NodePrices...),
		ServiceRules: append([]PriceRule{}, cfg.ServicePrices...),
		Warnings:     append([]string{}, cfg.Warnings...),
	}
	if cfg.DefaultNodeMonthly > 0 {
		v := cfg.DefaultNodeMonthly
		p.DefaultNodeMonthly = &v
	}
	if p.Configured {
		p.Source = cfg.Source
		if p.Source == "" {
			p.Source = "env"
		}
	}
	if q.AssumeNodeMonthly > 0 && cfg.DefaultNodeMonthly <= 0 {
		v := q.AssumeNodeMonthly
		p.AssumedNodeMonthly = &v
		if !p.Configured {
			p.Source = "assumed"
		}
	}
	return p
}

// --- policy helpers -----------------------------------------------------------

func quietConfidence(quiet, quietAfter time.Duration) string {
	switch {
	case quiet >= idleHighConfidenceQuiet && quiet >= 2*quietAfter:
		return ConfidenceHigh
	case quiet >= quietAfter:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

var confidenceRank = map[string]int{ConfidenceNone: 0, ConfidenceLow: 1, ConfidenceMedium: 2, ConfidenceHigh: 3}

func minConfidence(a, b string) string {
	if confidenceRank[a] <= confidenceRank[b] {
		return a
	}
	return b
}

func downgrade(c string) string {
	if c == ConfidenceHigh {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

func confidenceWeight(c string) float64 {
	switch c {
	case ConfidenceHigh:
		return 1
	case ConfidenceMedium:
		return 0.6
	default:
		return 0.25
	}
}

func isExternalService(s storage.Service) bool {
	return strings.EqualFold(s.Tech, "External") || s.Node == "External Network"
}

func isInfraLensService(s storage.Service) bool {
	for _, v := range []string{s.Name, s.ID, s.DisplayName} {
		if strings.Contains(strings.ToLower(v), "infralens") {
			return true
		}
	}
	return false
}

// isBareIPService: an ID that is only an address. The processor collapses
// host daemons (sshd, systemd, init) to their IP, and inbound peers are
// recorded by IP; neither is a workload anyone would decommission, and
// counting them in a node's split would stop the whole-node rule ever firing.
func isBareIPService(s storage.Service) bool {
	return net.ParseIP(s.ID) != nil
}

// isIgnoredCaller: inbound traffic that does not mean anyone uses the
// service - metrics scrapes and InfraLens's own probes. A monitoring tool
// connecting to a data store is treated as a real client (Grafana's own
// database, an exporter reading a DB), so it still counts there.
func isIgnoredCaller(caller, target storage.Service) bool {
	if isInfraLensService(caller) {
		return true
	}
	if caller.Type != "monitoring" {
		return false
	}
	return !dataStoreTypes[target.Type]
}

// looksLikeReplicaPeer: a data store talking to another data store of the
// same kind (type, technology or port) is far more likely to be replicating
// or clustering than to be a client.
func looksLikeReplicaPeer(s, peer storage.Service, port uint16, listenPorts func(string) []int) bool {
	if !dataStoreTypes[s.Type] {
		return false
	}
	if peer.Type == s.Type && peer.Type != "" {
		return true
	}
	if techFamily(s.Tech) != "" && techFamily(s.Tech) == techFamily(peer.Tech) {
		return true
	}
	if p := idPort(s.ID); p != 0 && p == int(port) {
		return true
	}
	return containsInt(listenPorts(s.ID), int(port))
}

func techFamily(tech string) string {
	f := strings.Fields(strings.ToLower(tech))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func idPort(id string) int {
	if _, p, err := net.SplitHostPort(id); err == nil {
		var n int
		_, _ = fmt.Sscanf(p, "%d", &n)
		return n
	}
	return 0
}

// k8sServiceAlias maps a workload ID ("Deploy: shop/api") to the ID a
// Kubernetes Service of the identical namespace/name resolves to. Only
// identical names match; that is a convention, not a guarantee.
func k8sServiceAlias(id string) string {
	for _, prefix := range []string{"Deploy: ", "STS: ", "DS: ", "RS: ", "App: ", "Pod: "} {
		if strings.HasPrefix(id, prefix) {
			return "Svc: " + strings.TrimPrefix(id, prefix)
		}
	}
	return ""
}

func liveInboundFromKnown(es []edgeObs, known map[string]storage.Service) bool {
	for _, e := range es {
		if e.live {
			if _, ok := known[e.source]; ok {
				return true
			}
		}
	}
	return false
}

func nodeReporting(in idleInputs, node string) bool {
	for _, s := range in.services {
		if s.Node == node {
			return true
		}
	}
	m, ok := in.metrics[node]
	return ok && in.now.Sub(m.LastSeen) < idleNodeAliveWindow
}

func metaOr(meta map[string]storage.Service, id string) storage.Service {
	if s, ok := meta[id]; ok {
		return s
	}
	return storage.Service{ID: id}
}

func displayName(s storage.Service) string {
	if h := ServiceHostname(&s); h != "" {
		return h
	}
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

func serviceIP(s storage.Service) string {
	if s.PodIP != "" {
		return s.PodIP
	}
	id := s.ID
	if i := strings.Index(id, "/"); i > 0 {
		id = id[:i]
	}
	if host, _, err := net.SplitHostPort(id); err == nil {
		id = host
	}
	if net.ParseIP(id) != nil {
		return id
	}
	return ""
}

func matchPriceRule(rules []PriceRule, values ...string) (PriceRule, bool) {
	for _, r := range rules {
		for _, v := range values {
			if v != "" && globMatch(r.Pattern, v) {
				return r, true
			}
		}
	}
	return PriceRule{}, false
}

func matchGlobs(patterns []string, values ...string) (string, bool) {
	for _, p := range patterns {
		for _, v := range values {
			if v != "" && globMatch(p, v) {
				return p, true
			}
		}
	}
	return "", false
}

// globMatch is a case-insensitive wildcard match where "*" spans any run of
// characters including "/" and "?" matches one character.
func globMatch(pattern, s string) bool {
	p := []rune(strings.ToLower(pattern))
	v := []rune(strings.ToLower(s))
	pi, vi := 0, 0
	star, mark := -1, 0
	for vi < len(v) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == v[vi]):
			pi++
			vi++
		case pi < len(p) && p[pi] == '*':
			star = pi
			mark = vi
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			vi = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCandidateKeys(m map[string]*idleCandidate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), fmt.Sprintf("+%d more", len(xs)-n))
}

func bytesSuffix(b uint64) string {
	if b == 0 {
		return ""
	}
	return ", " + humanBytes(b)
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	switch {
	case days >= 3:
		return fmt.Sprintf("%dd", days)
	case days > 0:
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// floorCents rounds down to the cent, so per-item shares can never add up to
// more than the price they were split from.
func floorCents(v float64) float64 { return math.Floor(v*100+1e-6) / 100 }

func formatMoney(v float64) string {
	if v == math.Trunc(v) {
		return commaInt(int64(v))
	}
	whole := math.Trunc(v)
	cents := int64(math.Round((v - whole) * 100))
	if cents == 100 {
		return commaInt(int64(whole) + 1)
	}
	return fmt.Sprintf("%s.%02d", commaInt(int64(whole)), cents)
}

func commaInt(n int64) string {
	if n < 0 {
		return "-" + commaInt(-n)
	}
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func currencySymbol(code string) string {
	switch strings.ToUpper(code) {
	case "USD", "AUD", "CAD", "NZD", "SGD", "HKD":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	case "JPY", "CNY":
		return "¥"
	case "INR":
		return "₹"
	case "TRY":
		return "₺"
	default:
		return strings.ToUpper(code) + " "
	}
}
