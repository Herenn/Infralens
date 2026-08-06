package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// ErrHistoryDisabled is returned by history-dependent queries when
// EnableHistory has not been called, so callers can tell "nothing recorded
// yet" apart from "recording was never turned on" and respond accordingly.
var ErrHistoryDisabled = errors.New("topology history is not enabled")

// TopologyService manages the service topology graph.
// It coordinates between the storage layer and event bus for real-time updates.
type TopologyService struct {
	store    storage.Store
	eventBus *EventBus

	// historyEnabled records when services and edges existed, in addition to
	// their current state. Off by default so existing behaviour is unchanged
	// until a caller opts in.
	historyEnabled bool

	// historyMaxGap is how long an entity may go unobserved before its
	// interval is treated as ended.
	historyMaxGap time.Duration
}

// NewTopologyService creates a new topology service.
func NewTopologyService(store storage.Store, eventBus *EventBus) *TopologyService {
	return &TopologyService{
		store:    store,
		eventBus: eventBus,
	}
}

// EnableHistory turns on topology history recording. A non-positive maxGap
// falls back to the storage default.
func (ts *TopologyService) EnableHistory(maxGap time.Duration) {
	if maxGap <= 0 {
		maxGap = storage.DefaultHistoryMaxGap
	}
	ts.historyEnabled = true
	ts.historyMaxGap = maxGap
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
		log.WithError(err).WithField("service_id", svc.ID).Warn("Failed to record service history")
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
		log.WithError(err).WithField("conn_id", conn.ID).Warn("Failed to record connection history")
	}
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
	// Check if connection exists
	existing, err := ts.store.Connections().Get(ctx, conn.ID)
	if err != nil {
		return fmt.Errorf("checking existing connection: %w", err)
	}

	isNew := existing == nil

	if existing != nil {
		// Increment count for existing connection
		conn.Count = existing.Count + 1
	} else {
		conn.Count = 1
	}

	if err := ts.store.Connections().Upsert(ctx, conn); err != nil {
		return fmt.Errorf("upserting connection: %w", err)
	}

	ts.recordConnectionHistory(ctx, conn, conn.LastSeen)

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
	if err := ts.store.Metrics().Upsert(ctx, metrics); err != nil {
		return fmt.Errorf("upserting metrics: %w", err)
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
	return ts.store.Services().Get(ctx, id)
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
	return ts.store.Services().List(ctx, filter)
}

// ListConnections returns all connections matching the given filter.
func (ts *TopologyService) ListConnections(ctx context.Context, filter storage.ConnectionFilter) ([]storage.Connection, error) {
	return ts.store.Connections().List(ctx, filter)
}

// GetTopology returns a snapshot of the current topology.
func (ts *TopologyService) GetTopology(ctx context.Context) (*storage.Topology, error) {
	return ts.store.GetTopology(ctx)
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
	maxImpactDepth = 20
)

// GetImpact returns the subgraph reachable from serviceID by BFS over the
// current topology, in the given direction. Returns (nil, nil) if serviceID
// does not exist in the current topology, matching GetService's
// not-found convention.
func (ts *TopologyService) GetImpact(ctx context.Context, serviceID string, direction ImpactDirection, maxDepth int) (*storage.Topology, error) {
	topo, err := ts.store.GetTopology(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting topology: %w", err)
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
		// A connection can reference an endpoint that has aged out of the
		// current services table (e.g. one side just went stale) while the
		// edge itself hasn't been pruned yet - skip rather than emit a
		// zero-value Service for it.
		if svc, ok := svcByID[id]; ok {
			services = append(services, svc)
		}
	}
	connections := make([]storage.Connection, 0, len(reachedConns))
	for _, conn := range reachedConns {
		connections = append(connections, conn)
	}

	return &storage.Topology{
		Services:    services,
		Connections: connections,
		UpdatedAt:   topo.UpdatedAt,
	}, nil
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

// GetCriticality ranks every service in the current topology by upstream
// blast radius (excluding itself), descending, capped to limit results. A
// non-positive limit falls back to defaultCriticalityLimit.
//
// Unlike GetImpact's default depth (tuned for a single readable lookup),
// this always traverses to maxImpactDepth - a criticality score computed
// from a truncated blast radius would understate exactly the services it's
// meant to flag.
func (ts *TopologyService) GetCriticality(ctx context.Context, limit int) ([]ServiceCriticality, error) {
	topo, err := ts.store.GetTopology(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting topology: %w", err)
	}
	if limit <= 0 {
		limit = defaultCriticalityLimit
	}

	byEndpoint := buildImpactIndex(topo.Connections, ImpactUpstream)

	ranked := make([]ServiceCriticality, 0, len(topo.Services))
	for _, svc := range topo.Services {
		visited, _ := impactBFS(svc.ID, byEndpoint, ImpactUpstream, maxImpactDepth)
		ranked = append(ranked, ServiceCriticality{
			Service:     svc,
			BlastRadius: len(visited) - 1, // exclude the seed itself
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].BlastRadius != ranked[j].BlastRadius {
			return ranked[i].BlastRadius > ranked[j].BlastRadius
		}
		return ranked[i].Service.ID < ranked[j].Service.ID // stable tie-break
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
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

	connected := make(map[string]bool, len(topo.Connections)*2)
	for _, conn := range topo.Connections {
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

	svcIntervals, err := ts.store.History().ServicesAt(ctx, at)
	if err != nil {
		return nil, fmt.Errorf("querying services at %s: %w", at, err)
	}
	connIntervals, err := ts.store.History().ConnectionsAt(ctx, at)
	if err != nil {
		return nil, fmt.Errorf("querying connections at %s: %w", at, err)
	}

	return &storage.Topology{
		Services:    servicesFromIntervals(svcIntervals),
		Connections: connectionsFromIntervals(connIntervals),
		UpdatedAt:   at,
	}, nil
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
// recent observation is older than olderThan. A non-positive olderThan
// falls back to the storage default retention window.
func (ts *TopologyService) GetStaleServices(ctx context.Context, olderThan time.Duration) ([]storage.ServiceInterval, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}
	if olderThan <= 0 {
		olderThan = storage.DefaultHistoryRetention
	}
	return ts.store.History().StaleServices(ctx, time.Now().Add(-olderThan))
}

// TopologyDiff is the set difference between the topology at two instants:
// what appeared and what disappeared between From and To.
type TopologyDiff struct {
	From                time.Time
	To                  time.Time
	AddedServices       []storage.Service
	RemovedServices     []storage.Service
	AddedConnections    []storage.Connection
	RemovedConnections  []storage.Connection
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

	fromSvc, err := ts.store.History().ServicesAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("querying services at %s: %w", from, err)
	}
	toSvc, err := ts.store.History().ServicesAt(ctx, to)
	if err != nil {
		return nil, fmt.Errorf("querying services at %s: %w", to, err)
	}
	fromConn, err := ts.store.History().ConnectionsAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("querying connections at %s: %w", from, err)
	}
	toConn, err := ts.store.History().ConnectionsAt(ctx, to)
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

	return &TopologyDiff{
		From:               from,
		To:                 to,
		AddedServices:      servicesFromIntervals(addedSvc),
		RemovedServices:    servicesFromIntervals(removedSvc),
		AddedConnections:   connectionsFromIntervals(addedConn),
		RemovedConnections: connectionsFromIntervals(removedConn),
	}, nil
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
