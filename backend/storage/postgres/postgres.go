// Package postgres provides a PostgreSQL implementation of the storage interfaces.
// It uses lib/pq for PostgreSQL connectivity.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"

	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/postgres/pgmigrations"
	log "github.com/sirupsen/logrus"
)

// Store implements storage.Store using PostgreSQL.
type Store struct {
	db          *sql.DB
	config      storage.Config
	services    *ServiceRepo
	connections *ConnectionRepo
	metrics     *MetricsRepo
	history     *HistoryRepo
	pruneStop   chan struct{}
	pruneWg     sync.WaitGroup
}

// New creates a new PostgreSQL store.
func New(cfg storage.Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("PostgreSQL DSN is required")
	}

	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Configure connection pool
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	} else {
		db.SetMaxOpenConns(25) // Default for PostgreSQL
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	} else {
		db.SetMaxIdleConns(5)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	} else {
		db.SetConnMaxLifetime(5 * time.Minute)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	store := &Store{
		db:        db,
		config:    cfg,
		pruneStop: make(chan struct{}),
	}

	// Initialize repositories
	store.services = &ServiceRepo{db: db}
	store.connections = &ConnectionRepo{db: db}
	store.metrics = &MetricsRepo{db: db}
	store.history = &HistoryRepo{db: db}

	// Run migrations if enabled
	if cfg.AutoMigrate {
		if err := store.migrate(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("running migrations: %w", err)
		}
	}

	// Start pruning goroutine if enabled
	if cfg.PruneInterval > 0 && cfg.PruneMaxAge > 0 {
		store.startPruning()
	}

	log.WithFields(log.Fields{
		"driver": "postgres",
	}).Info("PostgreSQL store initialized")

	return store, nil
}

// migrate runs database migrations using golang-migrate with embedded SQL files.
func (s *Store) migrate(ctx context.Context) error {
	// Create migration source from embedded files
	source, err := iofs.New(pgmigrations.FS, ".")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}

	// Create migration driver for PostgreSQL
	driver, err := postgres.WithInstance(s.db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("creating migration driver: %w", err)
	}

	// Create migrator
	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}

	// Run migrations
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("running migrations: %w", err)
	}

	// Get current version
	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		log.WithError(err).Warn("Could not get migration version")
	} else {
		log.WithFields(log.Fields{
			"version": version,
			"dirty":   dirty,
		}).Info("PostgreSQL migrations completed")
	}

	return nil
}

// startPruning starts the background pruning goroutine.
func (s *Store) startPruning() {
	s.pruneWg.Add(1)
	go func() {
		defer s.pruneWg.Done()
		ticker := time.NewTicker(s.config.PruneInterval)
		defer ticker.Stop()

		log.WithFields(log.Fields{
			"interval": s.config.PruneInterval,
			"max_age":  s.config.PruneMaxAge,
		}).Info("Started automatic pruning")

		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				pruned, err := s.Prune(ctx, s.config.PruneMaxAge)
				cancel()
				if err != nil {
					log.WithError(err).Error("Pruning failed")
				} else if pruned > 0 {
					log.WithField("pruned", pruned).Debug("Pruned stale data")
				}
			case <-s.pruneStop:
				return
			}
		}
	}()
}

// Services returns the service repository.
func (s *Store) Services() storage.ServiceRepository {
	return s.services
}

// Connections returns the connection repository.
func (s *Store) Connections() storage.ConnectionRepository {
	return s.connections
}

// Metrics returns the metrics repository.
func (s *Store) Metrics() storage.MetricsRepository {
	return s.metrics
}

// History returns the topology history repository.
func (s *Store) History() storage.HistoryRepository {
	return s.history
}

// GetTopology returns the complete topology snapshot.
func (s *Store) GetTopology(ctx context.Context) (*storage.Topology, error) {
	services, err := s.services.List(ctx, storage.ServiceFilter{})
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}

	connections, err := s.connections.List(ctx, storage.ConnectionFilter{})
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}

	metricsList, err := s.metrics.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing metrics: %w", err)
	}

	metricsMap := make(map[string]storage.NodeMetrics)
	for _, m := range metricsList {
		metricsMap[m.NodeName] = m
	}

	return &storage.Topology{
		Services:    services,
		Connections: connections,
		NodeMetrics: metricsMap,
		UpdatedAt:   time.Now(),
	}, nil
}

// RunInTransaction executes a function within a database transaction.
func (s *Store) RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	// Create transaction-aware context
	txCtx := context.WithValue(ctx, txKey{}, tx)

	if err := fn(txCtx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.WithError(rbErr).Error("Failed to rollback transaction")
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// txKey is the context key for transaction.
type txKey struct{}

// getTx returns the transaction from context, or nil if not in a transaction.
func getTx(ctx context.Context) *sql.Tx {
	tx, _ := ctx.Value(txKey{}).(*sql.Tx)
	return tx
}

// Prune removes all stale data older than maxAge.
func (s *Store) Prune(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge)
	var total int64

	// Prune services
	svcPruned, err := s.services.DeleteStale(ctx, cutoff)
	if err != nil {
		return total, fmt.Errorf("pruning services: %w", err)
	}
	total += svcPruned

	// Prune connections
	connPruned, err := s.connections.DeleteStale(ctx, cutoff)
	if err != nil {
		return total, fmt.Errorf("pruning connections: %w", err)
	}
	total += connPruned

	// Prune metrics
	metricsPruned, err := s.metrics.DeleteStale(ctx, cutoff)
	if err != nil {
		return total, fmt.Errorf("pruning metrics: %w", err)
	}
	total += metricsPruned

	// History is pruned on its own, much longer clock. maxAge governs current
	// state and is measured in minutes; applying it to history would discard
	// the record almost as fast as it is written.
	if s.config.HistoryEnabled {
		retention := s.config.HistoryRetention
		if retention <= 0 {
			retention = storage.DefaultHistoryRetention
		}
		historyPruned, err := s.history.DeleteStale(ctx, time.Now().Add(-retention))
		if err != nil {
			return total, fmt.Errorf("pruning history: %w", err)
		}
		total += historyPruned
	}

	return total, nil
}

// Close closes the store and releases resources.
func (s *Store) Close() error {
	// Stop pruning goroutine
	close(s.pruneStop)
	s.pruneWg.Wait()

	return s.db.Close()
}

// Ping verifies the connection is alive.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ============================================================================
// ServiceRepo Implementation
// ============================================================================

// ServiceRepo implements storage.ServiceRepository.
type ServiceRepo struct {
	db *sql.DB
}

func (r *ServiceRepo) executor(ctx context.Context) interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
} {
	if tx := getTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

func (r *ServiceRepo) Upsert(ctx context.Context, svc *storage.Service) error {
	now := time.Now()
	labelsJSON, _ := json.Marshal(svc.Labels)

	query := `
		INSERT INTO services (id, name, display_name, resolved_name, type, tech, icon, 
			namespace, node, pod_ip, labels, last_seen, healthy, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT(id) DO UPDATE SET
			name = COALESCE(NULLIF(EXCLUDED.name, ''), services.name),
			display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), services.display_name),
			resolved_name = COALESCE(NULLIF(EXCLUDED.resolved_name, ''), services.resolved_name),
			type = COALESCE(NULLIF(EXCLUDED.type, ''), services.type),
			tech = COALESCE(NULLIF(EXCLUDED.tech, ''), services.tech),
			icon = COALESCE(NULLIF(EXCLUDED.icon, ''), services.icon),
			namespace = COALESCE(NULLIF(EXCLUDED.namespace, ''), services.namespace),
			node = COALESCE(NULLIF(EXCLUDED.node, ''), services.node),
			pod_ip = COALESCE(NULLIF(EXCLUDED.pod_ip, ''), services.pod_ip),
			labels = CASE WHEN EXCLUDED.labels != '{}' AND EXCLUDED.labels != 'null' 
				THEN EXCLUDED.labels ELSE services.labels END,
			last_seen = EXCLUDED.last_seen,
			healthy = EXCLUDED.healthy,
			updated_at = EXCLUDED.updated_at
	`

	_, err := r.executor(ctx).ExecContext(ctx, query,
		svc.ID, svc.Name, svc.DisplayName, svc.ResolvedName, svc.Type, svc.Tech, svc.Icon,
		svc.Namespace, svc.Node, svc.PodIP, string(labelsJSON), now, svc.Healthy, now, now)
	return err
}

func (r *ServiceRepo) Get(ctx context.Context, id string) (*storage.Service, error) {
	query := `SELECT id, name, display_name, resolved_name, type, tech, icon, 
		namespace, node, pod_ip, labels, last_seen, healthy, created_at, updated_at
		FROM services WHERE id = $1`

	var svc storage.Service
	var labelsJSON string
	err := r.executor(ctx).QueryRowContext(ctx, query, id).Scan(
		&svc.ID, &svc.Name, &svc.DisplayName, &svc.ResolvedName, &svc.Type, &svc.Tech, &svc.Icon,
		&svc.Namespace, &svc.Node, &svc.PodIP, &labelsJSON, &svc.LastSeen, &svc.Healthy,
		&svc.CreatedAt, &svc.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if labelsJSON != "" {
		json.Unmarshal([]byte(labelsJSON), &svc.Labels)
	}
	return &svc, nil
}

func (r *ServiceRepo) List(ctx context.Context, filter storage.ServiceFilter) ([]storage.Service, error) {
	query := `SELECT id, name, display_name, resolved_name, type, tech, icon, 
		namespace, node, pod_ip, labels, last_seen, healthy, created_at, updated_at
		FROM services WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if filter.Node != "" {
		query += fmt.Sprintf(" AND node = $%d", argIdx)
		args = append(args, filter.Node)
		argIdx++
	}
	if filter.Namespace != "" {
		query += fmt.Sprintf(" AND namespace = $%d", argIdx)
		args = append(args, filter.Namespace)
		argIdx++
	}
	if filter.Type != "" {
		query += fmt.Sprintf(" AND type = $%d", argIdx)
		args = append(args, filter.Type)
		argIdx++
	}
	if filter.LastSeenAfter != nil {
		query += fmt.Sprintf(" AND last_seen > $%d", argIdx)
		args = append(args, *filter.LastSeenAfter)
		argIdx++
	}

	query += " ORDER BY name"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", filter.Offset)
		}
	}

	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var services []storage.Service
	for rows.Next() {
		var svc storage.Service
		var labelsJSON string
		err := rows.Scan(&svc.ID, &svc.Name, &svc.DisplayName, &svc.ResolvedName, &svc.Type, &svc.Tech, &svc.Icon,
			&svc.Namespace, &svc.Node, &svc.PodIP, &labelsJSON, &svc.LastSeen, &svc.Healthy,
			&svc.CreatedAt, &svc.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if labelsJSON != "" {
			json.Unmarshal([]byte(labelsJSON), &svc.Labels)
		}
		services = append(services, svc)
	}
	return services, rows.Err()
}

func (r *ServiceRepo) Delete(ctx context.Context, id string) error {
	_, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM services WHERE id = $1", id)
	return err
}

func (r *ServiceRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM services WHERE last_seen < $1", before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *ServiceRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.executor(ctx).QueryRowContext(ctx, "SELECT COUNT(*) FROM services").Scan(&count)
	return count, err
}

func (r *ServiceRepo) UpsertInspection(ctx context.Context, insp *storage.ServiceInspection) error {
	query := `
		INSERT INTO service_inspections (service_id, pid, process_name, command_line, working_dir,
			env_var_names, listen_ports, config_files, dependencies, http_info, db_info, 
			k8s_metadata, code_context, inspected_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT(service_id) DO UPDATE SET
			pid = EXCLUDED.pid,
			process_name = EXCLUDED.process_name,
			command_line = EXCLUDED.command_line,
			working_dir = EXCLUDED.working_dir,
			env_var_names = EXCLUDED.env_var_names,
			listen_ports = EXCLUDED.listen_ports,
			config_files = EXCLUDED.config_files,
			dependencies = EXCLUDED.dependencies,
			http_info = EXCLUDED.http_info,
			db_info = EXCLUDED.db_info,
			k8s_metadata = EXCLUDED.k8s_metadata,
			code_context = EXCLUDED.code_context,
			inspected_at = EXCLUDED.inspected_at
	`
	_, err := r.executor(ctx).ExecContext(ctx, query,
		insp.ServiceID, insp.PID, insp.ProcessName, insp.CommandLine, insp.WorkingDir,
		insp.EnvVarNames, insp.ListenPorts, insp.ConfigFiles, insp.Dependencies,
		insp.HTTPInfo, insp.DBInfo, insp.K8sMetadata, insp.CodeContext, insp.InspectedAt)
	return err
}

func (r *ServiceRepo) GetInspection(ctx context.Context, serviceID string) (*storage.ServiceInspection, error) {
	query := `SELECT service_id, pid, process_name, command_line, working_dir,
		env_var_names, listen_ports, config_files, dependencies, http_info, db_info,
		k8s_metadata, code_context, inspected_at
		FROM service_inspections WHERE service_id = $1`

	var insp storage.ServiceInspection
	err := r.executor(ctx).QueryRowContext(ctx, query, serviceID).Scan(
		&insp.ServiceID, &insp.PID, &insp.ProcessName, &insp.CommandLine, &insp.WorkingDir,
		&insp.EnvVarNames, &insp.ListenPorts, &insp.ConfigFiles, &insp.Dependencies,
		&insp.HTTPInfo, &insp.DBInfo, &insp.K8sMetadata, &insp.CodeContext, &insp.InspectedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &insp, err
}

// ============================================================================
// ConnectionRepo Implementation
// ============================================================================

// ConnectionRepo implements storage.ConnectionRepository.
type ConnectionRepo struct {
	db *sql.DB
}

func (r *ConnectionRepo) executor(ctx context.Context) interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
} {
	if tx := getTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

func (r *ConnectionRepo) Upsert(ctx context.Context, conn *storage.Connection) error {
	now := time.Now()
	if conn.Protocol == "" {
		conn.Protocol = "tcp"
	}
	query := `
		INSERT INTO connections (id, source_id, target_id, port, protocol, count, bytes_sent, bytes_recv,
			bytes_sent_rate, bytes_recv_rate, packets_sent, packets_recv, last_seen, latency_ms,
			created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT(id) DO UPDATE SET
			count = connections.count + 1,
			protocol = EXCLUDED.protocol,
			bytes_sent = CASE WHEN EXCLUDED.bytes_sent > 0 THEN EXCLUDED.bytes_sent ELSE connections.bytes_sent END,
			bytes_recv = CASE WHEN EXCLUDED.bytes_recv > 0 THEN EXCLUDED.bytes_recv ELSE connections.bytes_recv END,
			bytes_sent_rate = EXCLUDED.bytes_sent_rate,
			bytes_recv_rate = EXCLUDED.bytes_recv_rate,
			packets_sent = CASE WHEN EXCLUDED.packets_sent > 0 THEN EXCLUDED.packets_sent ELSE connections.packets_sent END,
			packets_recv = CASE WHEN EXCLUDED.packets_recv > 0 THEN EXCLUDED.packets_recv ELSE connections.packets_recv END,
			last_seen = EXCLUDED.last_seen,
			latency_ms = CASE WHEN EXCLUDED.latency_ms > 0 THEN EXCLUDED.latency_ms ELSE connections.latency_ms END,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.executor(ctx).ExecContext(ctx, query,
		conn.ID, conn.SourceID, conn.TargetID, conn.Port, conn.Protocol, conn.Count,
		conn.BytesSent, conn.BytesRecv, conn.BytesSentRate, conn.BytesRecvRate,
		conn.PacketsSent, conn.PacketsRecv, now, conn.Latency, now, now)
	return err
}

func (r *ConnectionRepo) Get(ctx context.Context, id string) (*storage.Connection, error) {
	query := `SELECT id, source_id, target_id, port, protocol, count, bytes_sent, bytes_recv,
		bytes_sent_rate, bytes_recv_rate, packets_sent, packets_recv, last_seen, latency_ms,
		created_at, updated_at FROM connections WHERE id = $1`

	var conn storage.Connection
	err := r.executor(ctx).QueryRowContext(ctx, query, id).Scan(
		&conn.ID, &conn.SourceID, &conn.TargetID, &conn.Port, &conn.Protocol, &conn.Count,
		&conn.BytesSent, &conn.BytesRecv, &conn.BytesSentRate, &conn.BytesRecvRate,
		&conn.PacketsSent, &conn.PacketsRecv, &conn.LastSeen, &conn.Latency,
		&conn.CreatedAt, &conn.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &conn, err
}

func (r *ConnectionRepo) List(ctx context.Context, filter storage.ConnectionFilter) ([]storage.Connection, error) {
	query := `SELECT id, source_id, target_id, port, protocol, count, bytes_sent, bytes_recv,
		bytes_sent_rate, bytes_recv_rate, packets_sent, packets_recv, last_seen, latency_ms,
		created_at, updated_at FROM connections WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if filter.SourceID != "" {
		query += fmt.Sprintf(" AND source_id = $%d", argIdx)
		args = append(args, filter.SourceID)
		argIdx++
	}
	if filter.TargetID != "" {
		query += fmt.Sprintf(" AND target_id = $%d", argIdx)
		args = append(args, filter.TargetID)
		argIdx++
	}
	if filter.Port > 0 {
		query += fmt.Sprintf(" AND port = $%d", argIdx)
		args = append(args, filter.Port)
		argIdx++
	}
	if filter.LastSeenAfter != nil {
		query += fmt.Sprintf(" AND last_seen > $%d", argIdx)
		args = append(args, *filter.LastSeenAfter)
		argIdx++
	}

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", filter.Offset)
		}
	}

	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connections []storage.Connection
	for rows.Next() {
		var conn storage.Connection
		err := rows.Scan(&conn.ID, &conn.SourceID, &conn.TargetID, &conn.Port, &conn.Protocol, &conn.Count,
			&conn.BytesSent, &conn.BytesRecv, &conn.BytesSentRate, &conn.BytesRecvRate,
			&conn.PacketsSent, &conn.PacketsRecv, &conn.LastSeen, &conn.Latency,
			&conn.CreatedAt, &conn.UpdatedAt)
		if err != nil {
			return nil, err
		}
		connections = append(connections, conn)
	}
	return connections, rows.Err()
}

func (r *ConnectionRepo) Delete(ctx context.Context, id string) error {
	_, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM connections WHERE id = $1", id)
	return err
}

func (r *ConnectionRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM connections WHERE last_seen < $1", before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *ConnectionRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.executor(ctx).QueryRowContext(ctx, "SELECT COUNT(*) FROM connections").Scan(&count)
	return count, err
}

func (r *ConnectionRepo) IncrementCount(ctx context.Context, id string) error {
	_, err := r.executor(ctx).ExecContext(ctx,
		"UPDATE connections SET count = count + 1, last_seen = $1, updated_at = $2 WHERE id = $3",
		time.Now(), time.Now(), id)
	return err
}

func (r *ConnectionRepo) UpdateStats(ctx context.Context, id string, bytesSent, bytesRecv uint64,
	bytesSentRate, bytesRecvRate float64, packetsSent, packetsRecv uint64) error {
	now := time.Now()
	_, err := r.executor(ctx).ExecContext(ctx,
		`UPDATE connections SET 
			bytes_sent = $1, bytes_recv = $2, 
			bytes_sent_rate = $3, bytes_recv_rate = $4,
			packets_sent = $5, packets_recv = $6,
			last_seen = $7, updated_at = $8
		WHERE id = $9`,
		bytesSent, bytesRecv, bytesSentRate, bytesRecvRate, packetsSent, packetsRecv, now, now, id)
	return err
}

// ============================================================================
// MetricsRepo Implementation
// ============================================================================

// MetricsRepo implements storage.MetricsRepository.
type MetricsRepo struct {
	db *sql.DB
}

func (r *MetricsRepo) executor(ctx context.Context) interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
} {
	if tx := getTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

func (r *MetricsRepo) Upsert(ctx context.Context, metrics *storage.NodeMetrics) error {
	now := time.Now()
	query := `
		INSERT INTO node_metrics (node_name, cpu_percent, mem_percent, mem_used, mem_total, last_seen, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(node_name) DO UPDATE SET
			cpu_percent = EXCLUDED.cpu_percent,
			mem_percent = EXCLUDED.mem_percent,
			mem_used = EXCLUDED.mem_used,
			mem_total = EXCLUDED.mem_total,
			last_seen = EXCLUDED.last_seen,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.executor(ctx).ExecContext(ctx, query,
		metrics.NodeName, metrics.CPUPercent, metrics.MemPercent, metrics.MemUsed, metrics.MemTotal, now, now)
	return err
}

func (r *MetricsRepo) Get(ctx context.Context, nodeName string) (*storage.NodeMetrics, error) {
	query := `SELECT node_name, cpu_percent, mem_percent, mem_used, mem_total, last_seen, updated_at
		FROM node_metrics WHERE node_name = $1`

	var m storage.NodeMetrics
	err := r.executor(ctx).QueryRowContext(ctx, query, nodeName).Scan(
		&m.NodeName, &m.CPUPercent, &m.MemPercent, &m.MemUsed, &m.MemTotal, &m.LastSeen, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &m, err
}

func (r *MetricsRepo) List(ctx context.Context) ([]storage.NodeMetrics, error) {
	rows, err := r.executor(ctx).QueryContext(ctx,
		`SELECT node_name, cpu_percent, mem_percent, mem_used, mem_total, last_seen, updated_at FROM node_metrics`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []storage.NodeMetrics
	for rows.Next() {
		var m storage.NodeMetrics
		err := rows.Scan(&m.NodeName, &m.CPUPercent, &m.MemPercent, &m.MemUsed, &m.MemTotal, &m.LastSeen, &m.UpdatedAt)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}

func (r *MetricsRepo) Delete(ctx context.Context, nodeName string) error {
	_, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM node_metrics WHERE node_name = $1", nodeName)
	return err
}

func (r *MetricsRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM node_metrics WHERE last_seen < $1", before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
