package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// ErrHistoryDisabled is returned by history-dependent queries when
// EnableHistory has not been called, so callers can tell "nothing recorded
// yet" apart from "recording was never turned on" and respond accordingly.
var ErrHistoryDisabled = errors.New("topology history is not enabled")

// ErrDiffRangeTooLarge is returned by GetTopologyDiff when the requested
// span exceeds the configured retention window. See GetTopologyDiff.
var ErrDiffRangeTooLarge = errors.New("requested range exceeds configured history retention")

// TopologyService manages the service topology graph.
// It coordinates between the storage layer and event bus for real-time updates.
type TopologyService struct {
	store    storage.Store
	eventBus *EventBus

	// historyEnabled records when services and edges existed, in addition to
	// their current state. A TopologyService does not record history until
	// EnableHistory is called - but note the shipped default is ON
	// (HISTORY_ENABLED=true in config), so real deployments do record it
	// unless they opt out. Recording costs roughly 30% more time per ingested
	// event when writes are serial, and ~40% under concurrent agent load -
	// every service and connection write does an extra UPDATE, and under
	// concurrency usually a second (no-op) guarded INSERT as well, because
	// racing writes routinely fail the monotonicity guard. Measure before
	// quoting either number again; both have moved as this write path changed.
	//
	// These fields, like historyMaxGap and historyRetention below, are written
	// once by EnableHistory during startup wiring and only read afterwards, so
	// they carry no lock. Anything that starts calling EnableHistory after the
	// server is serving needs to revisit that.
	historyEnabled bool

	// historyMaxGap is how long an entity may go unobserved before its
	// interval is treated as ended.
	historyMaxGap time.Duration

	// historyRetention mirrors the window the store prunes history on, so
	// query defaults derived from it stay inside what actually still exists.
	historyRetention time.Duration

	// criticality memoizes the blast-radius ranking. See GetCriticality.
	//
	// The guard is a 1-buffered channel rather than a sync.Mutex so waiters
	// can honour their request context: Mutex.Lock is uninterruptible, which
	// would let one slow store query pin every queued request well past its
	// deadline with no way to give up.
	criticalityLock   chan struct{}
	criticalityRanked []ServiceCriticality
	criticalityAt     time.Time

	// Throttles the "failed to record history" warning. See logHistoryFailure.
	historyLogMu         sync.Mutex
	historyLogLast       time.Time
	historyLogSuppressed int

	// Throttles history refreshes driven by throughput reports. See
	// refreshHistoryFromStats.
	historyRefreshMu    sync.Mutex
	historyRefreshed    map[string]time.Time
	historyRefreshSwept time.Time

	// driftStatus is published by the running drift detector. See
	// GetDriftStatus.
	driftStatus atomic.Pointer[DriftStatus]

	// Throttles DNS-name checks driven by throughput reports. See
	// AddConnectionHostnames.
	hostnames hostnameMemo

	// Serializes read-merge-write of a connection's names. See connLocks.
	connLocks connLocks

	// idle holds the idle-spend feature's state (idlecost.go, liveness.go).
	idle idleState
}

// NewTopologyService creates a new topology service.
func NewTopologyService(store storage.Store, eventBus *EventBus) *TopologyService {
	return &TopologyService{
		store:           store,
		eventBus:        eventBus,
		criticalityLock: make(chan struct{}, 1),
	}
}

// EnableHistory turns on topology history recording. Non-positive values fall
// back to the storage defaults.
//
// retention is the same window the store prunes history on. It is needed here,
// not just at the storage layer, because the decommission-candidate threshold
// is only meaningful relative to it - see staleThreshold.
func (ts *TopologyService) EnableHistory(maxGap, retention time.Duration) {
	if maxGap <= 0 {
		maxGap = storage.DefaultHistoryMaxGap
	}
	if retention <= 0 {
		retention = storage.DefaultHistoryRetention
	}
	ts.historyEnabled = true
	ts.historyMaxGap = maxGap
	ts.historyRetention = retention
}

// staleThreshold is how long a service must go unobserved before it is
// reported as a decommission candidate.
//
// Derived from the configured retention rather than used as a bare constant,
// and bounded at both ends. Asking for services unseen for longer than
// retention returns nothing by construction - the retention prune has already
// deleted exactly those intervals - so a fixed default silently produces an
// empty list for any deployment that shortens HISTORY_RETENTION below it.
// Deriving it keeps the two in the right relationship however retention is
// configured, instead of relying on a documented invariant nothing enforces.
func (ts *TopologyService) staleThreshold() time.Duration {
	retention := ts.historyRetention
	if retention <= 0 {
		retention = storage.DefaultHistoryRetention
	}
	// Cap: never ask for anything the retention prune has already deleted.
	half := retention / 2
	threshold := storage.DefaultStaleThreshold
	if half < threshold {
		threshold = half
	}
	// Floor: without one this swings the other way - at a 10m retention the cap
	// alone yields 5m, and every service quiet for five minutes is offered as a
	// decommission candidate.
	//
	// The floor is applied ONLY when it still fits inside the window. Raising
	// the threshold past retention would recreate the very bug the cap exists
	// to prevent (the prune deletes exactly what the query then asks for, so the
	// list is permanently empty), and an empty list is a worse failure than a
	// noisy one: noise is visible and arguable, emptiness looks like "nothing to
	// decommission". Containment wins; below a ~2h retention the threshold stays
	// at retention/2 and the results are correspondingly twitchy.
	if threshold < storage.MinStaleThreshold && storage.MinStaleThreshold <= half {
		threshold = storage.MinStaleThreshold
	}
	return threshold
}

// historyLogInterval is how often a history-recording failure may be logged.
//
// These failures are per-entity and therefore per-event: processing one TCP
// event records two services and a connection, so a persistent fault emits
// three lines per event. The realistic cause is a schema behind the binary -
// DB_AUTO_MIGRATE=false against a database that never got the history
// migration, with HISTORY_ENABLED defaulting to true - which floods logs
// indefinitely at whatever rate agents report.
//
// Throttled rather than latched off after the first failure: a transient
// fault (lock timeout, disk full) must not silently disable history for the
// life of the process. The suppressed count keeps the log honest about how
// often it is really happening.
const historyLogInterval = time.Minute

// logHistoryFailure reports a history write failure at most once per
// historyLogInterval, carrying how many were suppressed since the last report.
func (ts *TopologyService) logHistoryFailure(err error, field, id string) {
	ts.historyLogMu.Lock()
	if time.Since(ts.historyLogLast) < historyLogInterval {
		ts.historyLogSuppressed++
		ts.historyLogMu.Unlock()
		return
	}
	suppressed := ts.historyLogSuppressed
	ts.historyLogSuppressed = 0
	ts.historyLogLast = time.Now()
	ts.historyLogMu.Unlock()

	entry := log.WithError(err).WithField(field, id)
	if suppressed > 0 {
		entry = entry.WithField("suppressed_since_last", suppressed)
	}
	entry.Warn("Failed to record topology history (is the history migration applied?)")
}

// recordServiceHistory records a service sighting.
//
// History is strictly additive to the live topology: a failure to record it
// must not fail the write that keeps the current view correct, so errors are
// logged rather than returned. Losing a slice of history is a degraded
// feature; losing the live topology is a broken product.
func (ts *TopologyService) recordServiceHistory(ctx context.Context, svc *storage.Service, at time.Time) {
	if !ts.historyEnabled {
		return
	}
	if err := ts.store.History().RecordService(ctx, svc, observationTime(at), ts.historyMaxGap); err != nil {
		ts.logHistoryFailure(err, "service_id", svc.ID)
	}
}

// recordNodeHistory records an agent heartbeat (a host-metrics report) as
// node observation coverage - see storage.NodeInterval. node is the real
// agent node, group its CLUSTER_NAME group ("" if none). Throttled like the
// throughput-driven refreshes: agents report every few seconds.
func (ts *TopologyService) recordNodeHistory(ctx context.Context, node, group string, at time.Time) {
	if !ts.historyEnabled || node == "" {
		return
	}
	at = observationTime(at)
	// Keyed by (group, node), like the intervals: same-named nodes in two
	// clusters are two agents.
	if !ts.claimHistoryRefresh("n:"+group+"\x00"+node, at) {
		return
	}
	if err := ts.store.History().RecordNode(ctx, node, group, at, ts.historyMaxGap); err != nil {
		ts.logHistoryFailure(err, "node", node)
	}
}

// observationTime resolves when a sighting happened.
//
// Callers pass the LastSeen they built the entity with. Note that the current
// state tables do NOT use that value - ServiceRepo.Upsert stamps time.Now()
// itself and ignores the caller's LastSeen entirely - so history is the only
// place an explicitly supplied observation time is honoured. In normal
// operation the processor sets LastSeen to now and the two agree; honouring it
// here keeps backfill and replay meaningful rather than silently rewriting
// every observation to the moment it happened to be processed.
//
// A zero time means the caller did not supply one, so fall back to now rather
// than recording an interval in the year zero.
func observationTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now()
	}
	return at
}

// recordConnectionHistory records an edge sighting. See recordServiceHistory
// for why errors are logged rather than propagated.
func (ts *TopologyService) recordConnectionHistory(ctx context.Context, conn *storage.Connection, at time.Time) {
	if !ts.historyEnabled {
		return
	}
	if err := ts.store.History().RecordConnection(ctx, conn, observationTime(at), ts.historyMaxGap); err != nil {
		ts.logHistoryFailure(err, "conn_id", conn.ID)
	}
}

// refreshHistoryFromStats extends the history intervals of a connection that
// is carrying traffic, and of both of its endpoint services.
//
// Without this, history is only ever extended by connect/accept events, so a
// long-lived connection - a database pool, a gRPC channel, a Kafka consumer,
// opened once and then reused for days - stops being extended maxGap after it
// was opened. History then reports it (and often both its endpoints) as gone
// while traffic is still flowing over it: the timeline shows a dependency
// vanishing that never did, and drift detection announces a disappearance
// that didn't happen. Throughput reports are the only ongoing proof such a
// connection is alive, so they have to count as sightings.
//
// Throughput arrives far more often than connect events (every agent reports
// every socket each second by default), so writes are throttled to one per
// entity per maxGap/2 - often enough that an interval is always extended
// before its maxGap grace runs out even if a refresh is missed, rare enough
// that history write volume still tracks the number of live entities rather
// than the report rate.
//
// Endpoint services are refreshed from current state rather than synthesized
// from the connection: a service that has already been pruned from current
// state is the phantom-endpoint case (see GetImpact) and is deliberately not
// resurrected in history either.
func (ts *TopologyService) refreshHistoryFromStats(ctx context.Context, conn *storage.Connection) {
	if !ts.historyEnabled || conn == nil {
		return
	}
	now := time.Now()

	if ts.claimHistoryRefresh("c:"+conn.ID, now) {
		ts.recordConnectionHistory(ctx, conn, now)
	}
	for _, id := range []string{conn.SourceID, conn.TargetID} {
		if !ts.claimHistoryRefresh("s:"+id, now) {
			continue
		}
		svc, err := ts.store.Services().Get(ctx, id)
		if err != nil || svc == nil {
			continue
		}
		ts.recordServiceHistory(ctx, svc, now)
	}
}

// claimHistoryRefresh reports whether key is due a throughput-driven history
// refresh at now, and if so records that it has had one. Entries older than
// the refresh interval are swept at most once per maxGap so the map stays
// proportional to the set of entities currently carrying traffic.
func (ts *TopologyService) claimHistoryRefresh(key string, now time.Time) bool {
	interval := ts.historyMaxGap / 2

	ts.historyRefreshMu.Lock()
	defer ts.historyRefreshMu.Unlock()

	if ts.historyRefreshed == nil {
		ts.historyRefreshed = make(map[string]time.Time)
	}
	if last, ok := ts.historyRefreshed[key]; ok && now.Sub(last) < interval {
		return false
	}
	ts.historyRefreshed[key] = now

	if now.Sub(ts.historyRefreshSwept) > ts.historyMaxGap {
		for k, last := range ts.historyRefreshed {
			if now.Sub(last) >= interval {
				delete(ts.historyRefreshed, k)
			}
		}
		ts.historyRefreshSwept = now
	}
	return true
}

// HistoryEnabled reports whether topology history is being recorded.
func (ts *TopologyService) HistoryEnabled() bool {
	return ts.historyEnabled
}

// AddOrUpdateService adds or updates a service in the topology.
func (ts *TopologyService) AddOrUpdateService(ctx context.Context, svc *storage.Service) error {
	// Check if service exists to determine if this is create or update
	existing, err := ts.store.Services().Get(ctx, svc.ID)
	if err != nil {
		return fmt.Errorf("checking existing service: %w", err)
	}

	isNew := existing == nil
	svc.Healthy = true

	if err := ts.store.Services().Upsert(ctx, svc); err != nil {
		return fmt.Errorf("upserting service: %w", err)
	}

	ts.recordServiceHistory(ctx, svc, svc.LastSeen)

	// Publish event
	event := ServiceEvent{
		ServiceID: svc.ID,
		Name:      svc.Name,
		Node:      svc.Node,
		Type:      svc.Type,
		Healthy:   svc.Healthy,
	}

	if isNew {
		ts.eventBus.PublishServiceCreated(event)
	} else {
		ts.eventBus.PublishServiceUpdated(event)
	}

	return nil
}

// UpdateServiceInspection updates inspection data for a service.
func (ts *TopologyService) UpdateServiceInspection(ctx context.Context, serviceID string, insp *ServiceInspection) error {
	if insp == nil {
		return fmt.Errorf("inspection is nil for service %q", serviceID)
	}

	// Convert service layer inspection to storage model
	storageInsp := &storage.ServiceInspection{
		ServiceID:   serviceID,
		PID:         insp.PID,
		ProcessName: insp.ProcessName,
		WorkingDir:  insp.WorkingDir,
		InspectedAt: time.Now(),
	}

	// Serialize arrays and objects to JSON
	if len(insp.CommandLine) > 0 {
		data, _ := json.Marshal(insp.CommandLine)
		storageInsp.CommandLine = string(data)
	}
	if len(insp.EnvVarNames) > 0 {
		data, _ := json.Marshal(insp.EnvVarNames)
		storageInsp.EnvVarNames = string(data)
	}
	if len(insp.ListenPorts) > 0 {
		data, _ := json.Marshal(insp.ListenPorts)
		storageInsp.ListenPorts = string(data)
	}
	if len(insp.ConfigFiles) > 0 {
		data, _ := json.Marshal(insp.ConfigFiles)
		storageInsp.ConfigFiles = string(data)
	}
	if len(insp.Dependencies) > 0 {
		data, _ := json.Marshal(insp.Dependencies)
		storageInsp.Dependencies = string(data)
	}
	if insp.HTTPInfo != nil {
		data, _ := json.Marshal(insp.HTTPInfo)
		storageInsp.HTTPInfo = string(data)
	}
	if insp.DBInfo != nil {
		data, _ := json.Marshal(insp.DBInfo)
		storageInsp.DBInfo = string(data)
	}
	if insp.K8sMetadata != nil {
		data, _ := json.Marshal(insp.K8sMetadata)
		storageInsp.K8sMetadata = string(data)
	}
	if insp.CodeContext != nil {
		data, _ := json.Marshal(insp.CodeContext)
		storageInsp.CodeContext = string(data)
	}

	if err := ts.store.Services().UpsertInspection(ctx, storageInsp); err != nil {
		return fmt.Errorf("upserting inspection: %w", err)
	}

	// Optionally update service tech/type based on inspection
	if insp.HTTPInfo != nil && insp.HTTPInfo.ServerHeader != "" {
		svc, _ := ts.store.Services().Get(ctx, serviceID)
		if svc != nil && svc.Tech == "" {
			svc.Tech = insp.HTTPInfo.ServerHeader
			ts.store.Services().Upsert(ctx, svc)
		}
	}
	if insp.DBInfo != nil && insp.DBInfo.Type != "" {
		svc, _ := ts.store.Services().Get(ctx, serviceID)
		if svc != nil && svc.Tech == "" {
			svc.Tech = insp.DBInfo.Type
			if insp.DBInfo.Version != "" {
				svc.Tech = fmt.Sprintf("%s %s", insp.DBInfo.Type, insp.DBInfo.Version)
			}
			ts.store.Services().Upsert(ctx, svc)
		}
	}

	return nil
}

// AddConnection adds or updates a connection in the topology.
func (ts *TopologyService) AddConnection(ctx context.Context, conn *storage.Connection) error {
	// Held across read-merge-write so concurrent reports for one edge don't
	// lose each other's DNS names. See connLocks.
	unlock := ts.connLocks.lock(conn.ID)

	// Check if connection exists
	existing, err := ts.store.Connections().Get(ctx, conn.ID)
	if err != nil {
		unlock()
		return fmt.Errorf("checking existing connection: %w", err)
	}

	isNew := existing == nil

	var previousNames []storage.Hostname
	if existing != nil {
		// Increment count for existing connection
		conn.Count = existing.Count + 1
		previousNames = existing.Hostnames
	} else {
		conn.Count = 1
	}
	// Names accumulate (each at its latest sighting) rather than being replaced.
	conn.Hostnames = storage.MergeHostnames(conn.Hostnames, previousNames, time.Now())
	namesChanged := !storage.EqualHostnames(conn.Hostnames, previousNames)

	if err := ts.store.Connections().Upsert(ctx, conn); err != nil {
		unlock()
		return fmt.Errorf("upserting connection: %w", err)
	}

	ts.recordConnectionHistory(ctx, conn, conn.LastSeen)
	unlock()

	// Publish event
	event := ConnectionEvent{
		ConnectionID:  conn.ID,
		SourceID:      conn.SourceID,
		TargetID:      conn.TargetID,
		Port:          conn.Port,
		BytesSentRate: conn.BytesSentRate,
		BytesRecvRate: conn.BytesRecvRate,
	}

	if isNew {
		ts.eventBus.PublishConnectionCreated(event)
	} else {
		ts.eventBus.PublishConnectionUpdated(event)
	}
	// The target's derived names changed, after its own update was already
	// published (the processor stores the service before the connection).
	if namesChanged {
		ts.publishTargetUpdated(ctx, conn.TargetID)
	}

	return nil
}

// UpdateConnectionStats updates throughput statistics for a connection.
func (ts *TopologyService) UpdateConnectionStats(ctx context.Context, connID string,
	bytesSent, bytesRecv uint64, bytesSentRate, bytesRecvRate float64,
	packetsSent, packetsRecv uint64) error {

	err := ts.store.Connections().UpdateStats(ctx, connID,
		bytesSent, bytesRecv, bytesSentRate, bytesRecvRate, packetsSent, packetsRecv)
	if err != nil {
		return fmt.Errorf("updating connection stats: %w", err)
	}

	// Get updated connection for event
	conn, _ := ts.store.Connections().Get(ctx, connID)
	if conn != nil {
		ts.refreshEndpointsFromStats(ctx, conn)
		ts.refreshHistoryFromStats(ctx, conn)
		ts.eventBus.PublishConnectionUpdated(ConnectionEvent{
			ConnectionID:  conn.ID,
			SourceID:      conn.SourceID,
			TargetID:      conn.TargetID,
			Port:          conn.Port,
			BytesSentRate: bytesSentRate,
			BytesRecvRate: bytesRecvRate,
		})
	}

	return nil
}

// UpdateNodeMetrics updates CPU/RAM metrics for a node.
func (ts *TopologyService) UpdateNodeMetrics(ctx context.Context, metrics *storage.NodeMetrics) error {
	return ts.UpdateAgentMetrics(ctx, metrics, "")
}

// UpdateAgentMetrics is UpdateNodeMetrics for an agent that reports under a
// CLUSTER_NAME group: metrics.NodeName is the group (what the UI shows and
// what the group's services carry), agentNode the real node, whose own
// heartbeat is recorded - a group-level heartbeat would let one live agent
// keep every node in the group "observed". agentNode "" means ungrouped.
func (ts *TopologyService) UpdateAgentMetrics(ctx context.Context, metrics *storage.NodeMetrics, agentNode string) error {
	if err := ts.store.Metrics().Upsert(ctx, metrics); err != nil {
		return fmt.Errorf("upserting metrics: %w", err)
	}
	if agentNode == "" || agentNode == metrics.NodeName {
		ts.recordNodeHistory(ctx, metrics.NodeName, "", metrics.LastSeen)
	} else {
		ts.recordNodeHistory(ctx, agentNode, metrics.NodeName, metrics.LastSeen)
	}

	ts.eventBus.PublishMetricsUpdated(MetricsEvent{
		NodeName:   metrics.NodeName,
		CPUPercent: metrics.CPUPercent,
		MemPercent: metrics.MemPercent,
	})

	return nil
}

// GetService retrieves a service by ID.
func (ts *TopologyService) GetService(ctx context.Context, id string) (*storage.Service, error) {
	svc, err := ts.store.Services().Get(ctx, id)
	if err == nil {
		ts.annotateService(ctx, svc)
	}
	return svc, err
}

// GetConnection retrieves a connection by ID.
func (ts *TopologyService) GetConnection(ctx context.Context, id string) (*storage.Connection, error) {
	return ts.store.Connections().Get(ctx, id)
}

// GetNodeMetrics retrieves metrics for a node.
func (ts *TopologyService) GetNodeMetrics(ctx context.Context, nodeName string) (*storage.NodeMetrics, error) {
	return ts.store.Metrics().Get(ctx, nodeName)
}

// ListServices returns all services matching the given filter.
func (ts *TopologyService) ListServices(ctx context.Context, filter storage.ServiceFilter) ([]storage.Service, error) {
	services, err := ts.store.Services().List(ctx, filter)
	if err == nil && len(services) > 0 {
		if conns, cerr := ts.store.Connections().List(ctx, storage.ConnectionFilter{}); cerr == nil {
			AnnotateHostnames(services, conns)
		}
	}
	return services, err
}

// ListConnections returns all connections matching the given filter.
func (ts *TopologyService) ListConnections(ctx context.Context, filter storage.ConnectionFilter) ([]storage.Connection, error) {
	return ts.store.Connections().List(ctx, filter)
}

// GetTopology returns a snapshot of the current topology.
func (ts *TopologyService) GetTopology(ctx context.Context) (*storage.Topology, error) {
	topo, err := ts.store.GetTopology(ctx)
	annotateTopology(topo)
	return topo, err
}

// ImpactDirection controls which way GetImpact traverses the graph.
type ImpactDirection string

const (
	// ImpactUpstream follows edges backwards - what calls this service, and
	// what calls those callers. This is what "what breaks if I take this
	// down" actually means: the transitive callers, not the dependencies.
	ImpactUpstream ImpactDirection = "upstream"

	// ImpactDownstream follows edges forwards - what this service depends on.
	ImpactDownstream ImpactDirection = "downstream"
)

const (
	defaultImpactDepth = 5
	// maxImpactDepth bounds traversal so a densely connected graph can't
	// make this walk arbitrarily long; it's a safety cap, not a tuned value.
	//
	// GetCriticality always ranks at this depth, and the UI's blast-radius view
	// requests it explicitly (IMPACT_DEPTH in App.tsx) so the number the
	// "Most critical" panel reports matches the set the impact view
	// highlights. Changing it here without changing that is a visible
	// contradiction between two views of the same question.
	maxImpactDepth = 20
)

// resolveTopology returns current state, or the reconstruction at `at` when a
// non-zero instant is supplied.
func (ts *TopologyService) resolveTopology(ctx context.Context, at time.Time) (*storage.Topology, error) {
	if at.IsZero() {
		return ts.GetTopology(ctx)
	}
	return ts.GetTopologyAt(ctx, at)
}

// GetImpact returns the subgraph reachable from serviceID by BFS over the
// topology, in the given direction. A non-zero `at` traverses the topology as
// it existed at that instant instead of current state - without it, asking
// "what breaks if this goes down" while viewing a past moment answered from
// today's graph and highlighted the result onto the historical one. Returns (nil, nil) if serviceID
// does not exist in the current topology, matching GetService's
// not-found convention.
func (ts *TopologyService) GetImpact(ctx context.Context, serviceID string, direction ImpactDirection, maxDepth int, at time.Time) (*storage.Topology, error) {
	topo, err := ts.resolveTopology(ctx, at)
	if err != nil {
		return nil, err
	}

	svcByID := make(map[string]storage.Service, len(topo.Services))
	for _, svc := range topo.Services {
		svcByID[svc.ID] = svc
	}
	if _, ok := svcByID[serviceID]; !ok {
		return nil, nil
	}

	if maxDepth <= 0 {
		maxDepth = defaultImpactDepth
	}
	if maxDepth > maxImpactDepth {
		maxDepth = maxImpactDepth
	}

	byEndpoint := buildImpactIndex(topo.Connections, direction)
	visited, reachedConns := impactBFS(serviceID, byEndpoint, direction, maxDepth)

	services := make([]storage.Service, 0, len(visited))
	for id := range visited {
		// A connection can reference an endpoint that is not in the services
		// table - see realReachedCount for when that still happens. Skip
		// rather than emit a zero-value Service for it.
		if svc, ok := svcByID[id]; ok {
			services = append(services, svc)
		}
	}
	// Edges are filtered to match. Emitting an edge whose endpoint was just
	// dropped above would hand back a graph referencing nodes it doesn't
	// contain, which anything that renders the response has to special-case.
	connections := make([]storage.Connection, 0, len(reachedConns))
	for _, conn := range reachedConns {
		_, haveSource := svcByID[conn.SourceID]
		_, haveTarget := svcByID[conn.TargetID]
		if !haveSource || !haveTarget {
			continue
		}
		connections = append(connections, conn)
	}

	return &storage.Topology{
		Services:    services,
		Connections: connections,
		UpdatedAt:   topo.UpdatedAt,
	}, nil
}

// realReachedCount is how many *existing* services the traversal reached,
// excluding the seed. It is the blast radius: "how many services break if this
// one goes down."
//
// Counting the raw visited set instead would count endpoints that no longer
// exist. Up to v3.0 that was routine: throughput reports refreshed only the
// connection's last_seen, so the endpoints of a long-lived connection (DB
// pools, gRPC channels) aged out at PruneMaxAge while the edge stayed. Since
// v3.1 UpdateConnectionStats also refreshes both endpoints
// (refreshEndpointsFromStats, liveness.go), so that no longer happens while
// traffic flows - but an edge can still outlive an endpoint: a service
// deleted (RemoveService, demo retirements) or pruned while its connection is
// still inside its own prune window, or a connection reported for a peer that
// was never upserted. Counting those phantoms would inflate the ranking, and
// could report a blast radius larger than the total number of services.
func realReachedCount(visited map[string]bool, known map[string]bool, seed string) int {
	n := 0
	for id := range visited {
		if id != seed && known[id] {
			n++
		}
	}
	return n
}

// buildImpactIndex indexes connections by the endpoint impactBFS expands
// from: for upstream that's the target (find who calls this ID), for
// downstream the source (find what this ID calls). Built once and reused
// across many BFS runs - GetCriticality runs one per service in the graph
// rather than rebuilding this per seed.
func buildImpactIndex(connections []storage.Connection, direction ImpactDirection) map[string][]storage.Connection {
	byEndpoint := make(map[string][]storage.Connection, len(connections))
	for _, conn := range connections {
		if direction == ImpactDownstream {
			byEndpoint[conn.SourceID] = append(byEndpoint[conn.SourceID], conn)
		} else {
			byEndpoint[conn.TargetID] = append(byEndpoint[conn.TargetID], conn)
		}
	}
	return byEndpoint
}

// impactBFS is the traversal core shared by GetImpact and GetCriticality:
// breadth-first from seed over byEndpoint, capped at maxDepth hops. Returns
// every reached node (including seed) and every edge walked to reach them.
func impactBFS(seed string, byEndpoint map[string][]storage.Connection, direction ImpactDirection, maxDepth int) (visited map[string]bool, reachedConns map[string]storage.Connection) {
	visited = map[string]bool{seed: true}
	reachedConns = make(map[string]storage.Connection)
	frontier := []string{seed}

	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			for _, conn := range byEndpoint[id] {
				reachedConns[conn.ID] = conn
				neighbor := conn.SourceID
				if direction == ImpactDownstream {
					neighbor = conn.TargetID
				}
				if !visited[neighbor] {
					visited[neighbor] = true
					next = append(next, neighbor)
				}
			}
		}
		frontier = next
	}
	return visited, reachedConns
}

// ServiceCriticality is a service's blast radius size: how many other
// services (transitively) depend on it, upstream. Higher means more breaks
// if this one goes down.
type ServiceCriticality struct {
	Service     storage.Service
	BlastRadius int
}

// defaultCriticalityLimit caps the ranking to a reviewable list by default -
// "your riskiest handful," not a full sort of every service in the graph.
const defaultCriticalityLimit = 20

// criticalityTTL is how long a computed ranking is reused.
//
// The ranking costs one BFS per service - O(N*(N+E)) - and is served from a
// public, unauthenticated endpoint, so without this the cost per request is
// unbounded and attacker-controlled. Blast radius is a property of the graph's
// shape, which changes on the timescale of deploys rather than of requests, so
// a few seconds of staleness buys a hard ceiling of one recomputation per TTL
// no matter how often the endpoint is hit.
const criticalityTTL = 30 * time.Second

// GetCriticality ranks every service in the current topology by upstream
// blast radius (excluding itself), descending, capped to limit results. A
// non-positive limit falls back to defaultCriticalityLimit.
//
// Unlike GetImpact's default depth (tuned for a single readable lookup),
// this always traverses to maxImpactDepth - a criticality score computed
// from a truncated blast radius would understate exactly the services it's
// meant to flag.
//
// The full ranking is memoized for criticalityTTL and the limit applied to
// the cached result, so varying ?limit= doesn't defeat the cache.
func (ts *TopologyService) GetCriticality(ctx context.Context, limit int) ([]ServiceCriticality, error) {
	if limit <= 0 {
		limit = defaultCriticalityLimit
	}

	ranked, err := ts.rankedCriticality(ctx)
	if err != nil {
		return nil, err
	}

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	// Copied rather than returned as a sub-slice of the cached array, which
	// the cache outlives: a returned window would otherwise both alias entries
	// the next caller will read and keep the whole backing array alive.
	//
	// Service.Labels is a map, so the element copy alone would still hand out
	// a reference into the cache; it is cloned so a caller mutating the
	// returned labels cannot corrupt what the next TTL's worth of requests
	// see. The remaining fields are values and need no deep copy.
	out := make([]ServiceCriticality, len(ranked))
	copy(out, ranked)
	for i := range out {
		if out[i].Service.Labels == nil {
			continue
		}
		labels := make(map[string]string, len(out[i].Service.Labels))
		for k, v := range out[i].Service.Labels {
			labels[k] = v
		}
		out[i].Service.Labels = labels
	}
	return out, nil
}

// rankedCriticality returns the full blast-radius ranking, recomputing it
// only when the memoized copy has aged past criticalityTTL.
//
// The lock is held across the computation so a burst of concurrent requests
// produces one recomputation rather than one per request - the stampede this
// cache exists to prevent.
//
// The tradeoff is that the topology read happens under the lock too, so a slow
// or hung store serializes every criticality request behind it for as long as
// that query takes. Accepted deliberately: the alternative (compute outside
// the lock) lets exactly the concurrent load this guards against each start
// its own O(N*(N+E)) traversal. Waiting is bounded by the caller's context
// rather than by that query, so a cancelled or timed-out request gives up
// instead of queueing behind it.
func (ts *TopologyService) rankedCriticality(ctx context.Context) ([]ServiceCriticality, error) {
	select {
	case ts.criticalityLock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-ts.criticalityLock }()

	if ts.criticalityRanked != nil && time.Since(ts.criticalityAt) < criticalityTTL {
		return ts.criticalityRanked, nil
	}

	topo, err := ts.store.GetTopology(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting topology: %w", err)
	}
	// So the ranking names external endpoints by hostname (ServiceHostname),
	// like the map does, instead of ":443".
	annotateTopology(topo)

	byEndpoint := buildImpactIndex(topo.Connections, ImpactUpstream)

	known := make(map[string]bool, len(topo.Services))
	for _, svc := range topo.Services {
		known[svc.ID] = true
	}

	ranked := make([]ServiceCriticality, 0, len(topo.Services))
	for _, svc := range topo.Services {
		visited, _ := impactBFS(svc.ID, byEndpoint, ImpactUpstream, maxImpactDepth)
		ranked = append(ranked, ServiceCriticality{
			Service:     svc,
			BlastRadius: realReachedCount(visited, known, svc.ID),
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].BlastRadius != ranked[j].BlastRadius {
			return ranked[i].BlastRadius > ranked[j].BlastRadius
		}
		return ranked[i].Service.ID < ranked[j].Service.ID // stable tie-break
	})

	// An empty ranking is not cached. Caching it would pin "nothing to rank"
	// for a full TTL on a backend that has only just started discovering
	// services - the same first-run trap as the history range - and there is
	// nothing to protect here anyway: the traversal this cache exists to
	// throttle is O(0) when there are no services.
	if len(ranked) > 0 {
		ts.criticalityRanked = ranked
		ts.criticalityAt = time.Now()
	}
	return ranked, nil
}

// GetOrphanServices returns services with no connections at all - neither
// caller nor callee in the current topology. Often misconfigured, leftover,
// or something the agent hasn't captured traffic for yet. Unlike
// GetStaleServices this needs no history: an orphan is a fact about the
// live graph, not something that only shows up after being unobserved for a
// while.
func (ts *TopologyService) GetOrphanServices(ctx context.Context) ([]storage.Service, error) {
	topo, err := ts.store.GetTopology(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting topology: %w", err)
	}

	known := make(map[string]bool, len(topo.Services))
	for _, svc := range topo.Services {
		known[svc.ID] = true
	}

	// An edge only counts as connecting something if both of its endpoints
	// exist. A connection pointing at a service that is gone draws nothing on
	// the graph, so a service whose only edge is one of those looks isolated -
	// and is exactly the "leftover" this panel is meant to surface. Counting it
	// as connected made the panel disagree with the picture beside it.
	//
	// Rarer since v3.1 (throughput now keeps both endpoints alive), but still
	// reachable: a peer deleted or pruned while the edge is inside its own
	// prune window (see realReachedCount).
	connected := make(map[string]bool, len(topo.Connections)*2)
	for _, conn := range topo.Connections {
		if !known[conn.SourceID] || !known[conn.TargetID] {
			continue
		}
		connected[conn.SourceID] = true
		connected[conn.TargetID] = true
	}

	orphans := make([]storage.Service, 0)
	for _, svc := range topo.Services {
		if !connected[svc.ID] {
			orphans = append(orphans, svc)
		}
	}
	return orphans, nil
}

// GetTopologyAt reconstructs the topology as it existed at a specific
// instant, from recorded history intervals rather than current state.
//
// Node metrics have no historical record - only services and connections are
// tracked as intervals - so the returned Topology always has an empty
// NodeMetrics. Per-service decorative fields not stored on the interval
// (DisplayName, Labels, PodIP, ...) are likewise absent; the interval only
// carries what's needed to redraw the graph shape.
func (ts *TopologyService) GetTopologyAt(ctx context.Context, at time.Time) (*storage.Topology, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}

	svcIntervals, err := ts.store.History().ServicesAt(ctx, at, ts.historyMaxGap)
	if err != nil {
		return nil, fmt.Errorf("querying services at %s: %w", at, err)
	}
	connIntervals, err := ts.store.History().ConnectionsAt(ctx, at, ts.historyMaxGap)
	if err != nil {
		return nil, fmt.Errorf("querying connections at %s: %w", at, err)
	}

	services := servicesFromIntervals(svcIntervals)
	topo := &storage.Topology{
		Services:    services,
		Connections: edgesWithinServices(connectionsFromIntervals(connIntervals), services),
		UpdatedAt:   at,
	}
	annotateTopologyAt(topo, at)
	return topo, nil
}

// edgesWithinServices drops connections whose endpoints are not among the
// given services, so a reconstructed topology is a coherent graph rather than
// one referencing nodes it does not contain.
//
// The two interval tables are queried independently and can legitimately
// disagree at an instant: an edge may be observed before its endpoints are
// fingerprinted, and retention prunes each table on its own rows - a service's
// newest interval can end before its connection's, because throughput reports
// refresh a connection's last_seen while only connect/accept events refresh a
// service's. Consumers cannot draw an edge with no node, and App.tsx wraps
// layout in a try/catch that would swallow the failure silently.
func edgesWithinServices(conns []storage.Connection, services []storage.Service) []storage.Connection {
	present := make(map[string]bool, len(services))
	for _, svc := range services {
		present[svc.ID] = true
	}
	out := make([]storage.Connection, 0, len(conns))
	for _, c := range conns {
		if present[c.SourceID] && present[c.TargetID] {
			out = append(out, c)
		}
	}
	return out
}

// GetHistoryBounds returns the earliest and latest instants covered by
// recorded history, for sizing a UI timeline control. ok is false when
// history is enabled but nothing has been recorded yet.
func (ts *TopologyService) GetHistoryBounds(ctx context.Context) (bounds storage.HistoryBounds, ok bool, err error) {
	if !ts.historyEnabled {
		return storage.HistoryBounds{}, false, ErrHistoryDisabled
	}
	return ts.store.History().Bounds(ctx)
}

// GetStaleServices returns decommission candidates: services whose most
// recent observation is older than olderThan, at most limit of them. A
// non-positive olderThan falls back to staleThreshold; a non-positive limit
// means no limit.
//
// An explicitly requested olderThan is honoured as given, even beyond the
// retention window - that is the caller's stated question, and answering a
// different one silently would be worse than returning the empty set they
// asked for. Only the default is derived from retention.
func (ts *TopologyService) GetStaleServices(ctx context.Context, olderThan time.Duration, limit int) ([]storage.ServiceInterval, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}
	if olderThan <= 0 {
		olderThan = ts.staleThreshold()
	}
	return ts.store.History().StaleServices(ctx, time.Now().Add(-olderThan), limit)
}

// TopologyDiff is the set difference between the topology at two instants:
// what appeared and what disappeared between From and To.
type TopologyDiff struct {
	From               time.Time
	To                 time.Time
	AddedServices      []storage.Service
	RemovedServices    []storage.Service
	AddedConnections   []storage.Connection
	RemovedConnections []storage.Connection
}

// GetTopologyDiff compares the topology at two instants and returns what
// appeared and disappeared between them. This is the on-demand, manual
// sibling of continuous change detection: no rules engine, no persisted
// alert log, just "what's different between these two points" computed from
// the same interval data GetTopologyAt already reconstructs from.
func (ts *TopologyService) GetTopologyDiff(ctx context.Context, from, to time.Time) (*TopologyDiff, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}
	// Bounded by retention, not an arbitrary constant: a span wider than what
	// the store still holds does four interval queries' worth of work to
	// answer a question the prune has already made unanswerable for the part
	// outside the window. This is also what keeps the endpoint's cost bounded
	// for an unauthenticated caller - see the public-endpoint cost analysis in
	// CODE-REVIEW-FINDINGS.md (O5).
	//
	// The cap is 2x retention, not 1x: pruning runs on its own interval
	// (PRUNE_INTERVAL), not continuously, so the oldest data actually present
	// can lag "now - retention" by up to one prune cycle. At a short
	// HISTORY_RETENTION that slop is proportionally large (a 5m default prune
	// interval is half of a 10m retention), so a 1x cap would reject the
	// legitimate "diff against the oldest data we still have" request the UI
	// makes by default. 2x keeps a hard bound while absorbing that slop at any
	// configured retention.
	if to.Sub(from) > 2*ts.historyRetention {
		return nil, ErrDiffRangeTooLarge
	}

	fromSvc, err := ts.store.History().ServicesAt(ctx, from, ts.historyMaxGap)
	if err != nil {
		return nil, fmt.Errorf("querying services at %s: %w", from, err)
	}
	toSvc, err := ts.store.History().ServicesAt(ctx, to, ts.historyMaxGap)
	if err != nil {
		return nil, fmt.Errorf("querying services at %s: %w", to, err)
	}
	fromConn, err := ts.store.History().ConnectionsAt(ctx, from, ts.historyMaxGap)
	if err != nil {
		return nil, fmt.Errorf("querying connections at %s: %w", from, err)
	}
	toConn, err := ts.store.History().ConnectionsAt(ctx, to, ts.historyMaxGap)
	if err != nil {
		return nil, fmt.Errorf("querying connections at %s: %w", to, err)
	}

	fromSvcIDs := make(map[string]bool, len(fromSvc))
	for _, si := range fromSvc {
		fromSvcIDs[si.ServiceID] = true
	}
	toSvcIDs := make(map[string]bool, len(toSvc))
	for _, si := range toSvc {
		toSvcIDs[si.ServiceID] = true
	}

	var addedSvc, removedSvc []storage.ServiceInterval
	for _, si := range toSvc {
		if !fromSvcIDs[si.ServiceID] {
			addedSvc = append(addedSvc, si)
		}
	}
	for _, si := range fromSvc {
		if !toSvcIDs[si.ServiceID] {
			removedSvc = append(removedSvc, si)
		}
	}

	fromConnIDs := make(map[string]bool, len(fromConn))
	for _, ci := range fromConn {
		fromConnIDs[ci.ConnectionID] = true
	}
	toConnIDs := make(map[string]bool, len(toConn))
	for _, ci := range toConn {
		toConnIDs[ci.ConnectionID] = true
	}

	var addedConn, removedConn []storage.ConnectionInterval
	for _, ci := range toConn {
		if !fromConnIDs[ci.ConnectionID] {
			addedConn = append(addedConn, ci)
		}
	}
	for _, ci := range fromConn {
		if !toConnIDs[ci.ConnectionID] {
			removedConn = append(removedConn, ci)
		}
	}

	diff := &TopologyDiff{
		From:               from,
		To:                 to,
		AddedServices:      servicesFromIntervals(addedSvc),
		RemovedServices:    servicesFromIntervals(removedSvc),
		AddedConnections:   connectionsFromIntervals(addedConn),
		RemovedConnections: connectionsFromIntervals(removedConn),
	}
	// Each side is named from the edges present at its own instant.
	annotateHostnamesAt(diff.AddedServices, connectionsFromIntervals(toConn), to)
	annotateHostnamesAt(diff.RemovedServices, connectionsFromIntervals(fromConn), from)
	return diff, nil
}

func servicesFromIntervals(intervals []storage.ServiceInterval) []storage.Service {
	out := make([]storage.Service, 0, len(intervals))
	for _, si := range intervals {
		out = append(out, storage.Service{
			ID:        si.ServiceID,
			Name:      si.Name,
			Type:      si.Type,
			Tech:      si.Tech,
			Namespace: si.Namespace,
			Node:      si.Node,
			LastSeen:  si.LastSeen,
			Healthy:   true,
			CreatedAt: si.FirstSeen,
			UpdatedAt: si.LastSeen,
		})
	}
	return out
}

func connectionsFromIntervals(intervals []storage.ConnectionInterval) []storage.Connection {
	out := make([]storage.Connection, 0, len(intervals))
	for _, ci := range intervals {
		out = append(out, storage.Connection{
			ID:        ci.ConnectionID,
			SourceID:  ci.SourceID,
			TargetID:  ci.TargetID,
			Port:      ci.Port,
			Protocol:  ci.Protocol,
			LastSeen:  ci.LastSeen,
			CreatedAt: ci.FirstSeen,
			UpdatedAt: ci.LastSeen,
			Hostnames: ci.Hostnames,
		})
	}
	return out
}

// GetStats returns statistics about the topology.
func (ts *TopologyService) GetStats(ctx context.Context) (map[string]int64, error) {
	svcCount, err := ts.store.Services().Count(ctx)
	if err != nil {
		return nil, err
	}

	connCount, err := ts.store.Connections().Count(ctx)
	if err != nil {
		return nil, err
	}

	return map[string]int64{
		"services":    svcCount,
		"connections": connCount,
	}, nil
}

// ConnectionExists checks if a connection exists.
func (ts *TopologyService) ConnectionExists(ctx context.Context, id string) (bool, error) {
	conn, err := ts.store.Connections().Get(ctx, id)
	if err != nil {
		return false, err
	}
	return conn != nil, nil
}

// ServiceInspection represents deep inspection data for a service.
// This is the service layer model, different from storage model.
type ServiceInspection struct {
	PID          int32
	ProcessName  string
	CommandLine  []string
	WorkingDir   string
	EnvVarNames  []string
	ListenPorts  []int
	ConfigFiles  []string
	Dependencies []Dependency
	HTTPInfo     *HTTPProbeInfo
	DBInfo       *DBProbeInfo
	K8sMetadata  *K8sMetadataInfo
	CodeContext  *CodeContext
}

// Dependency represents a package dependency.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Type    string `json:"type"`
}

// HTTPProbeInfo contains HTTP probing results.
type HTTPProbeInfo struct {
	ServerHeader    string            `json:"server_header,omitempty"`
	PoweredBy       string            `json:"x_powered_by,omitempty"`
	HealthEndpoint  string            `json:"health_endpoint,omitempty"`
	HealthStatus    int               `json:"health_status,omitempty"`
	Endpoints       []string          `json:"endpoints,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// DBProbeInfo contains database probing results.
type DBProbeInfo struct {
	Type       string   `json:"type"`
	Version    string   `json:"version,omitempty"`
	Databases  []string `json:"databases,omitempty"`
	TableCount int      `json:"table_count,omitempty"`
}

// K8sMetadataInfo contains Kubernetes metadata.
type K8sMetadataInfo struct {
	PodName        string            `json:"pod_name,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	ServiceAccount string            `json:"service_account,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
}

// CodeContext contains source code information for AI analysis.
type CodeContext struct {
	ProjectName    string `json:"project_name,omitempty"`
	Description    string `json:"description,omitempty"`
	README         string `json:"readme,omitempty"`
	EntryPointFile string `json:"entry_point_file,omitempty"`
	EntryPoint     string `json:"entry_point,omitempty"`
	Dockerfile     string `json:"dockerfile,omitempty"`
}

// BroadcastTopology sends the current topology to all WebSocket subscribers.
func (ts *TopologyService) BroadcastTopology(ctx context.Context) {
	topology, err := ts.GetTopology(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get topology for broadcast")
		return
	}
	ts.eventBus.PublishTopologySnapshot(topology)
}
