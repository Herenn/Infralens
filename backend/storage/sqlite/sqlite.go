// Package sqlite provides a SQLite implementation of the storage interfaces.
// It uses modernc.org/sqlite for pure Go SQLite support (no CGO required).
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"

	"github.com/Herenn/Infralens/backend/migrations"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// SQLite performance tuning constants.
// Adjust these values based on your workload characteristics.
const (
	// sqlitePragmas configures SQLite for optimal performance:
	// - WAL mode: Better concurrency for read-heavy workloads
	// - NORMAL sync: Good durability with improved write speed
	// - 5s busy timeout: Prevents "database is locked" under contention
	// - 20MB cache: Keeps hot data in memory (negative = KB)
	sqlitePragmas = "_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_cache_size=-20000"
)

// Store implements storage.Store using SQLite.
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

// New creates a new SQLite store.
func New(cfg storage.Config) (*Store, error) {
	if cfg.Driver == "" {
		cfg.Driver = "sqlite"
	}

	dsn := cfg.DSN
	if dsn == "" {
		dsn = "infralens.db"
	}

	// Add SQLite pragmas for better performance
	if !strings.Contains(dsn, "?") {
		dsn += "?"
	} else {
		dsn += "&"
	}
	dsn += sqlitePragmas

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Configure connection pool
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	} else {
		db.SetMaxOpenConns(1) // SQLite works best with single connection
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	// Start pruning goroutine if enabled.
	//
	// History retention is deliberately part of this condition. Requiring
	// PruneMaxAge > 0 alone would mean "do not expire my current state" also
	// silently switched off HISTORY_RETENTION - and history, unlike current
	// state, grows without bound (current-state rows are updated in place;
	// interval rows accumulate), so nothing would ever collect it.
	if cfg.PruneInterval > 0 && (cfg.PruneMaxAge > 0 || cfg.HistoryEnabled) {
		store.startPruning()
	}

	log.WithFields(log.Fields{
		"driver": cfg.Driver,
		"dsn":    storage.RedactDSN(cfg.DSN),
	}).Info("SQLite store initialized")

	return store, nil
}

// migrate runs database migrations using golang-migrate with embedded SQL files.
func (s *Store) migrate(ctx context.Context) error {
	// Create migration source from embedded files
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}

	// Create migration driver for SQLite
	driver, err := sqlite.WithInstance(s.db, &sqlite.Config{})
	if err != nil {
		return fmt.Errorf("creating migration driver: %w", err)
	}

	// Create migrator
	m, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
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
		}).Info("Database migrations completed")
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
				// Prune(maxAge) treats a non-positive maxAge as "cutoff at or
				// after now", i.e. delete everything - so a configured
				// PruneMaxAge of 0 ("do not expire current state") must not be
				// handed to it. Prune history on its own window instead: the
				// two retentions are independent, and history is the one that
				// grows without bound if nothing collects it.
				var pruned int64
				var err error
				if s.config.PruneMaxAge > 0 {
					pruned, err = s.Prune(ctx, s.config.PruneMaxAge)
				} else {
					pruned, err = s.pruneHistory(ctx)
				}
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
	historyPruned, err := s.pruneHistory(ctx)
	if err != nil {
		return total, err
	}
	total += historyPruned

	return total, nil
}

// PruneHistoryForTest exposes pruneHistory to tests in other packages so they
// can exercise the loop's PruneMaxAge<=0 branch without waiting on a ticker.
func (s *Store) PruneHistoryForTest(ctx context.Context) (int64, error) { return s.pruneHistory(ctx) }

// pruneHistory deletes history intervals past the retention window. Separate
// from Prune so the background loop can run it even when current-state
// pruning is switched off (PruneMaxAge <= 0) - the two windows are unrelated.
func (s *Store) pruneHistory(ctx context.Context) (int64, error) {
	if !s.config.HistoryEnabled {
		return 0, nil
	}
	retention := s.config.HistoryRetention
	if retention <= 0 {
		retention = storage.DefaultHistoryRetention
	}
	n, err := s.history.DeleteStale(ctx, time.Now().Add(-retention))
	if err != nil {
		return 0, fmt.Errorf("pruning history: %w", err)
	}
	return n, nil
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), services.name),
			display_name = COALESCE(NULLIF(excluded.display_name, ''), services.display_name),
			resolved_name = COALESCE(NULLIF(excluded.resolved_name, ''), services.resolved_name),
			type = COALESCE(NULLIF(excluded.type, ''), services.type),
			tech = COALESCE(NULLIF(excluded.tech, ''), services.tech),
			icon = COALESCE(NULLIF(excluded.icon, ''), services.icon),
			namespace = COALESCE(NULLIF(excluded.namespace, ''), services.namespace),
			node = COALESCE(NULLIF(excluded.node, ''), services.node),
			pod_ip = COALESCE(NULLIF(excluded.pod_ip, ''), services.pod_ip),
			labels = CASE WHEN excluded.labels != '{}' AND excluded.labels != 'null' 
				THEN excluded.labels ELSE services.labels END,
			last_seen = excluded.last_seen,
			healthy = excluded.healthy,
			updated_at = excluded.updated_at
	`

	_, err := r.executor(ctx).ExecContext(ctx, query,
		svc.ID, svc.Name, svc.DisplayName, svc.ResolvedName, svc.Type, svc.Tech, svc.Icon,
		svc.Namespace, svc.Node, svc.PodIP, string(labelsJSON), now, svc.Healthy, now, now)
	return err
}

func (r *ServiceRepo) Get(ctx context.Context, id string) (*storage.Service, error) {
	query := `SELECT id, name, display_name, resolved_name, type, tech, icon, 
		namespace, node, pod_ip, labels, last_seen, healthy, created_at, updated_at
		FROM services WHERE id = ?`

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

	if filter.Node != "" {
		query += " AND node = ?"
		args = append(args, filter.Node)
	}
	if filter.Namespace != "" {
		query += " AND namespace = ?"
		args = append(args, filter.Namespace)
	}
	if filter.Type != "" {
		query += " AND type = ?"
		args = append(args, filter.Type)
	}
	if filter.LastSeenAfter != nil {
		query += " AND last_seen > ?"
		args = append(args, *filter.LastSeenAfter)
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
	_, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM services WHERE id = ?", id)
	return err
}

func (r *ServiceRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM services WHERE last_seen < ?", before)
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET
			pid = excluded.pid,
			process_name = excluded.process_name,
			command_line = excluded.command_line,
			working_dir = excluded.working_dir,
			env_var_names = excluded.env_var_names,
			listen_ports = excluded.listen_ports,
			config_files = excluded.config_files,
			dependencies = excluded.dependencies,
			http_info = excluded.http_info,
			db_info = excluded.db_info,
			k8s_metadata = excluded.k8s_metadata,
			code_context = excluded.code_context,
			inspected_at = excluded.inspected_at
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
		FROM service_inspections WHERE service_id = ?`

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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			count = connections.count + 1,
			protocol = excluded.protocol,
			bytes_sent = CASE WHEN excluded.bytes_sent > 0 THEN excluded.bytes_sent ELSE connections.bytes_sent END,
			bytes_recv = CASE WHEN excluded.bytes_recv > 0 THEN excluded.bytes_recv ELSE connections.bytes_recv END,
			bytes_sent_rate = excluded.bytes_sent_rate,
			bytes_recv_rate = excluded.bytes_recv_rate,
			packets_sent = CASE WHEN excluded.packets_sent > 0 THEN excluded.packets_sent ELSE connections.packets_sent END,
			packets_recv = CASE WHEN excluded.packets_recv > 0 THEN excluded.packets_recv ELSE connections.packets_recv END,
			last_seen = excluded.last_seen,
			latency_ms = CASE WHEN excluded.latency_ms > 0 THEN excluded.latency_ms ELSE connections.latency_ms END,
			updated_at = excluded.updated_at
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
		created_at, updated_at FROM connections WHERE id = ?`

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

	if filter.SourceID != "" {
		query += " AND source_id = ?"
		args = append(args, filter.SourceID)
	}
	if filter.TargetID != "" {
		query += " AND target_id = ?"
		args = append(args, filter.TargetID)
	}
	if filter.Port > 0 {
		query += " AND port = ?"
		args = append(args, filter.Port)
	}
	if filter.LastSeenAfter != nil {
		query += " AND last_seen > ?"
		args = append(args, *filter.LastSeenAfter)
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
	_, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM connections WHERE id = ?", id)
	return err
}

func (r *ConnectionRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM connections WHERE last_seen < ?", before)
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
		"UPDATE connections SET count = count + 1, last_seen = ?, updated_at = ? WHERE id = ?",
		time.Now(), time.Now(), id)
	return err
}

func (r *ConnectionRepo) UpdateStats(ctx context.Context, id string, bytesSent, bytesRecv uint64,
	bytesSentRate, bytesRecvRate float64, packetsSent, packetsRecv uint64) error {
	now := time.Now()
	_, err := r.executor(ctx).ExecContext(ctx,
		`UPDATE connections SET 
			bytes_sent = ?, bytes_recv = ?, 
			bytes_sent_rate = ?, bytes_recv_rate = ?,
			packets_sent = ?, packets_recv = ?,
			last_seen = ?, updated_at = ?
		WHERE id = ?`,
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
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_name) DO UPDATE SET
			cpu_percent = excluded.cpu_percent,
			mem_percent = excluded.mem_percent,
			mem_used = excluded.mem_used,
			mem_total = excluded.mem_total,
			last_seen = excluded.last_seen,
			updated_at = excluded.updated_at
	`
	_, err := r.executor(ctx).ExecContext(ctx, query,
		metrics.NodeName, metrics.CPUPercent, metrics.MemPercent, metrics.MemUsed, metrics.MemTotal, now, now)
	return err
}

func (r *MetricsRepo) Get(ctx context.Context, nodeName string) (*storage.NodeMetrics, error) {
	query := `SELECT node_name, cpu_percent, mem_percent, mem_used, mem_total, last_seen, updated_at
		FROM node_metrics WHERE node_name = ?`

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
	_, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM node_metrics WHERE node_name = ?", nodeName)
	return err
}

func (r *MetricsRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.executor(ctx).ExecContext(ctx, "DELETE FROM node_metrics WHERE last_seen < ?", before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
