// Package storage provides the persistence layer interfaces and implementations
// for InfraLens. It follows the Repository pattern to abstract data access,
// allowing easy swapping between different backends (SQLite, PostgreSQL, etc.)
package storage

import (
	"context"
	"errors"
	"time"
)

// Common errors
var (
	// ErrNotFound is returned when a requested item is not found.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists is returned when attempting to create a duplicate.
	ErrAlreadyExists = errors.New("already exists")

	// ErrInvalidInput is returned when input validation fails.
	ErrInvalidInput = errors.New("invalid input")
)

// Service represents a service (process) in the topology.
// This is the storage model, mapped from/to the domain model.
type Service struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`
	ResolvedName string            `json:"resolved_name,omitempty"`
	Type         string            `json:"type,omitempty"`
	Tech         string            `json:"tech,omitempty"`
	Icon         string            `json:"icon,omitempty"`
	Namespace    string            `json:"namespace,omitempty"`
	Node         string            `json:"node,omitempty"`
	PodIP        string            `json:"pod_ip,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	LastSeen     time.Time         `json:"last_seen"`
	Healthy      bool              `json:"healthy"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// ServiceInspection contains deep inspection data for a service.
type ServiceInspection struct {
	ServiceID    string    `json:"service_id"`
	PID          int32     `json:"pid,omitempty"`
	ProcessName  string    `json:"process_name,omitempty"`
	CommandLine  string    `json:"command_line,omitempty"` // JSON array stored as string
	WorkingDir   string    `json:"working_dir,omitempty"`
	EnvVarNames  string    `json:"env_var_names,omitempty"` // JSON array
	ListenPorts  string    `json:"listen_ports,omitempty"`  // JSON array
	ConfigFiles  string    `json:"config_files,omitempty"`  // JSON array
	Dependencies string    `json:"dependencies,omitempty"`  // JSON array
	HTTPInfo     string    `json:"http_info,omitempty"`     // JSON object
	DBInfo       string    `json:"db_info,omitempty"`       // JSON object
	K8sMetadata  string    `json:"k8s_metadata,omitempty"`  // JSON object
	CodeContext  string    `json:"code_context,omitempty"`  // JSON object
	InspectedAt  time.Time `json:"inspected_at"`
}

// Connection represents a connection between two services.
type Connection struct {
	ID            string    `json:"id"`
	SourceID      string    `json:"source_id"`
	TargetID      string    `json:"target_id"`
	Port          uint16    `json:"port"`
	Protocol      string    `json:"protocol,omitempty"` // "tcp" or "udp"
	Count         int64     `json:"count"`
	BytesSent     uint64    `json:"bytes_sent,omitempty"`
	BytesRecv     uint64    `json:"bytes_recv,omitempty"`
	BytesSentRate float64   `json:"bytes_sent_rate,omitempty"`
	BytesRecvRate float64   `json:"bytes_recv_rate,omitempty"`
	PacketsSent   uint64    `json:"packets_sent,omitempty"`
	PacketsRecv   uint64    `json:"packets_recv,omitempty"`
	LastSeen      time.Time `json:"last_seen"`
	Latency       float64   `json:"latency_ms,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// NodeMetrics represents resource usage for a physical/virtual node.
type NodeMetrics struct {
	NodeName   string    `json:"node_name"`
	CPUPercent float64   `json:"cpu_percent"`
	MemPercent float64   `json:"mem_percent"`
	MemUsed    uint64    `json:"mem_used"`
	MemTotal   uint64    `json:"mem_total"`
	LastSeen   time.Time `json:"last_seen"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Topology represents the complete service topology graph.
type Topology struct {
	Services    []Service              `json:"services"`
	Connections []Connection           `json:"connections"`
	NodeMetrics map[string]NodeMetrics `json:"node_metrics,omitempty"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

// ServiceFilter provides filtering options for querying services.
type ServiceFilter struct {
	Node          string     // Filter by node name
	Namespace     string     // Filter by namespace
	Type          string     // Filter by service type
	LastSeenAfter *time.Time // Only services seen after this time
	Limit         int        // Max results (0 = no limit)
	Offset        int        // Pagination offset
}

// ConnectionFilter provides filtering options for querying connections.
type ConnectionFilter struct {
	SourceID      string
	TargetID      string
	Port          uint16
	LastSeenAfter *time.Time
	Limit         int
	Offset        int
}

// ServiceRepository defines the interface for service persistence operations.
type ServiceRepository interface {
	// Upsert creates or updates a service.
	Upsert(ctx context.Context, svc *Service) error

	// Get retrieves a service by ID.
	Get(ctx context.Context, id string) (*Service, error)

	// List retrieves services matching the filter.
	List(ctx context.Context, filter ServiceFilter) ([]Service, error)

	// Delete removes a service by ID.
	Delete(ctx context.Context, id string) error

	// DeleteStale removes services not seen since the given time.
	DeleteStale(ctx context.Context, before time.Time) (int64, error)

	// Count returns the total number of services.
	Count(ctx context.Context) (int64, error)

	// UpsertInspection creates or updates inspection data for a service.
	UpsertInspection(ctx context.Context, insp *ServiceInspection) error

	// GetInspection retrieves inspection data for a service.
	GetInspection(ctx context.Context, serviceID string) (*ServiceInspection, error)
}

// ConnectionRepository defines the interface for connection persistence operations.
type ConnectionRepository interface {
	// Upsert creates or updates a connection.
	Upsert(ctx context.Context, conn *Connection) error

	// Get retrieves a connection by ID.
	Get(ctx context.Context, id string) (*Connection, error)

	// List retrieves connections matching the filter.
	List(ctx context.Context, filter ConnectionFilter) ([]Connection, error)

	// Delete removes a connection by ID.
	Delete(ctx context.Context, id string) error

	// DeleteStale removes connections not seen since the given time.
	DeleteStale(ctx context.Context, before time.Time) (int64, error)

	// Count returns the total number of connections.
	Count(ctx context.Context) (int64, error)

	// IncrementCount atomically increments the connection count.
	IncrementCount(ctx context.Context, id string) error

	// UpdateStats updates throughput statistics for a connection.
	UpdateStats(ctx context.Context, id string, bytesSent, bytesRecv uint64,
		bytesSentRate, bytesRecvRate float64, packetsSent, packetsRecv uint64) error
}

// MetricsRepository defines the interface for node metrics persistence.
type MetricsRepository interface {
	// Upsert creates or updates node metrics.
	Upsert(ctx context.Context, metrics *NodeMetrics) error

	// Get retrieves metrics for a node.
	Get(ctx context.Context, nodeName string) (*NodeMetrics, error)

	// List retrieves all node metrics.
	List(ctx context.Context) ([]NodeMetrics, error)

	// Delete removes metrics for a node.
	Delete(ctx context.Context, nodeName string) error

	// DeleteStale removes metrics not seen since the given time.
	DeleteStale(ctx context.Context, before time.Time) (int64, error)
}

// Store combines all repositories and provides transaction support.
type Store interface {
	// Services returns the service repository.
	Services() ServiceRepository

	// Connections returns the connection repository.
	Connections() ConnectionRepository

	// Metrics returns the metrics repository.
	Metrics() MetricsRepository

	// History returns the topology history repository, which records when
	// services and edges existed rather than only their current state.
	History() HistoryRepository

	// GetTopology returns the complete topology snapshot.
	GetTopology(ctx context.Context) (*Topology, error)

	// RunInTransaction executes a function within a database transaction.
	RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error

	// Prune removes all stale data older than maxAge.
	Prune(ctx context.Context, maxAge time.Duration) (pruned int64, err error)

	// Close closes the store and releases resources.
	Close() error

	// Ping verifies the connection is alive.
	Ping(ctx context.Context) error
}

// Config holds database configuration.
type Config struct {
	Driver          string        // "sqlite" or "postgres"
	DSN             string        // Data source name / connection string
	MaxOpenConns    int           // Max open connections (0 = default)
	MaxIdleConns    int           // Max idle connections (0 = default)
	ConnMaxLifetime time.Duration // Connection max lifetime (0 = default)
	AutoMigrate     bool          // Run migrations on startup
	PruneInterval   time.Duration // Interval for pruning stale data (0 = disabled)
	PruneMaxAge     time.Duration // Max age for data before pruning

	// HistoryEnabled records topology history alongside current state.
	HistoryEnabled bool

	// HistoryRetention is how long historical intervals are kept.
	//
	// Deliberately separate from PruneMaxAge. That governs current state and
	// defaults to 30 minutes - reusing it here would delete the history within
	// half an hour and leave the feature pointless. Retention for history is
	// measured in days, and is the setting that actually decides how far back
	// a user can look.
	HistoryRetention time.Duration

	// HistoryMaxGap is how long an entity can go unobserved before its
	// interval is considered ended and the next sighting opens a new one.
	//
	// This is the knob that decides what counts as "it went away" versus "we
	// briefly stopped hearing about it". Too small and an agent restart or a
	// quiet period reads as the service disappearing and coming back; too
	// large and a real outage is swallowed into one continuous interval.
	HistoryMaxGap time.Duration
}

// History defaults. Exported so callers configuring a Store directly get the
// same behaviour as the server without duplicating the numbers.
const (
	// DefaultHistoryRetention keeps roughly a month of architecture history:
	// long enough to answer "did this change since last release" and to cover
	// a postmortem, without unbounded growth.
	DefaultHistoryRetention = 30 * 24 * time.Hour

	// DefaultHistoryMaxGap tolerates an agent restart, a rollout, or a short
	// network blip without registering a disappearance, while still noticing a
	// service that is genuinely gone within a few minutes.
	DefaultHistoryMaxGap = 5 * time.Minute

	// DefaultStaleThreshold is the upper bound on how long a service must go
	// unobserved before it is offered as a decommission candidate: long enough
	// that a service idle that long is genuinely suspicious.
	//
	// It is an upper bound rather than a flat default because the threshold is
	// only meaningful relative to the retention window. A cutoff at or beyond
	// retention asks for exactly the intervals the retention prune has already
	// deleted, so the answer is always empty. Callers must therefore clamp this
	// against the configured retention rather than using it bare - see
	// service.TopologyService.staleThreshold, which is what the API goes
	// through.
	DefaultStaleThreshold = 7 * 24 * time.Hour

	// MinStaleThreshold is the floor for that clamp. Below roughly an hour,
	// "not seen recently" stops meaning "probably decommissioned" and starts
	// flagging anything briefly quiet, which is noise rather than a signal.
	MinStaleThreshold = time.Hour
)

// DefaultConfig returns a Config with sensible defaults for SQLite.
func DefaultConfig() Config {
	return Config{
		Driver:          "sqlite",
		DSN:             "infralens.db",
		MaxOpenConns:    1, // SQLite works best with 1 connection
		MaxIdleConns:    1,
		ConnMaxLifetime: 0,
		AutoMigrate:     true,
		PruneInterval:   5 * time.Minute,
		PruneMaxAge:     30 * time.Minute,
	}
}
