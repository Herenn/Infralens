package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// getTestDSN returns the PostgreSQL DSN for testing.
// It checks for TEST_POSTGRES_DSN environment variable first,
// otherwise skips the test.
func getTestDSN(t *testing.T) string {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("Skipping PostgreSQL tests: TEST_POSTGRES_DSN not set. " +
			"To run these tests, set TEST_POSTGRES_DSN=postgres://user:pass@localhost:5432/testdb?sslmode=disable")
	}
	return dsn
}

// newTestStore creates a PostgreSQL store for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	dsn := getTestDSN(t)

	cfg := storage.Config{
		Driver:          "postgres",
		DSN:             dsn,
		AutoMigrate:     true,
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
	}

	store, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create PostgreSQL test store: %v", err)
	}

	t.Cleanup(func() {
		// Clean up test data
		cleanupTestData(t, store)
		store.Close()
	})

	return store
}

// cleanupTestData removes test data created during tests.
func cleanupTestData(t *testing.T, store *Store) {
	ctx := context.Background()

	// Delete all test data (services, connections, metrics with test prefixes)
	// This is a best-effort cleanup
	_, _ = store.db.ExecContext(ctx, "DELETE FROM services WHERE id LIKE 'suite-%' OR id LIKE 'conc-%' OR id LIKE 'list-%' OR id LIKE 'topo-%' OR id LIKE 'prune-%' OR id LIKE 'tx-%' OR id LIKE 'rw-%'")
	_, _ = store.db.ExecContext(ctx, "DELETE FROM connections WHERE id LIKE 'conn-%' OR id LIKE 'conc-conn-%' OR id LIKE 'topo-%'")
	_, _ = store.db.ExecContext(ctx, "DELETE FROM node_metrics WHERE node_name LIKE 'node-%'")
}

// TestPostgresStoreSuite runs the shared storage test suite against PostgreSQL.
func TestPostgresStoreSuite(t *testing.T) {
	store := newTestStore(t)
	storage.StoreSuite(t, store)
}

// Example usage to run tests:
// TEST_POSTGRES_DSN="postgres://postgres:postgres@localhost:5432/infralens_test?sslmode=disable" go test -v ./backend/storage/postgres/...

// TestPostgresConnection verifies basic PostgreSQL connectivity.
func TestPostgresConnection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Failed to ping PostgreSQL: %v", err)
	}
}

// TestPostgresTransactions tests PostgreSQL-specific transaction behavior.
func TestPostgresTransactions(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Test rollback on error
	testErr := storage.ErrInvalidInput
	err := store.RunInTransaction(ctx, func(txCtx context.Context) error {
		_ = store.Services().Upsert(txCtx, &storage.Service{
			ID:      "tx-rollback-test",
			Name:    "should-rollback",
			Healthy: true,
		})
		return testErr
	})

	if err != testErr {
		t.Errorf("Expected error %v, got %v", testErr, err)
	}

	// Verify rollback
	svc, _ := store.Services().Get(ctx, "tx-rollback-test")
	if svc != nil {
		t.Error("Transaction was not rolled back")
		store.Services().Delete(ctx, "tx-rollback-test")
	}
}

// TestPostgresJSONB tests PostgreSQL JSONB field handling.
func TestPostgresJSONB(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Test service with complex labels (stored as JSONB)
	svc := &storage.Service{
		ID:      "jsonb-test-svc",
		Name:    "jsonb-test",
		Healthy: true,
		Labels: map[string]string{
			"app":         "test",
			"environment": "staging",
			"version":     "1.0.0",
		},
	}

	if err := store.Services().Upsert(ctx, svc); err != nil {
		t.Fatalf("Upsert with labels failed: %v", err)
	}

	got, err := store.Services().Get(ctx, svc.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if got.Labels["app"] != "test" {
		t.Errorf("Labels not correctly stored/retrieved: got %v", got.Labels)
	}
	if got.Labels["environment"] != "staging" {
		t.Errorf("Labels missing 'environment': got %v", got.Labels)
	}

	store.Services().Delete(ctx, svc.ID)
}

// TestPostgresConcurrentConnections tests connection pool under load.
func TestPostgresConcurrentConnections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// This test specifically checks PostgreSQL connection pooling
	// by making many concurrent requests

	const numRequests = 50
	errChan := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func(idx int) {
			_, err := store.Services().Count(ctx)
			errChan <- err
		}(i)
	}

	for i := 0; i < numRequests; i++ {
		if err := <-errChan; err != nil {
			t.Errorf("Concurrent request %d failed: %v", i, err)
		}
	}
}
