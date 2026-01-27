package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// TopologyService manages the service topology graph.
// It coordinates between the storage layer and event bus for real-time updates.
type TopologyService struct {
	store    storage.Store
	eventBus *EventBus
}

// NewTopologyService creates a new topology service.
func NewTopologyService(store storage.Store, eventBus *EventBus) *TopologyService {
	return &TopologyService{
		store:    store,
		eventBus: eventBus,
	}
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
