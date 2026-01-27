// Package storage provides a shared test suite for storage implementations.
// This file contains tests that run against any Store implementation.
package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// StoreSuite runs the complete test suite against a Store implementation.
// Call this from SQLite and Postgres test files with their respective stores.
func StoreSuite(t *testing.T, store Store) {
	t.Run("ServiceRepository", func(t *testing.T) {
		ServiceRepoSuite(t, store)
	})

	t.Run("ConnectionRepository", func(t *testing.T) {
		ConnectionRepoSuite(t, store)
	})

	t.Run("MetricsRepository", func(t *testing.T) {
		MetricsRepoSuite(t, store)
	})

	t.Run("Store", func(t *testing.T) {
		StoreFunctionsSuite(t, store)
	})

	t.Run("Concurrency", func(t *testing.T) {
		ConcurrencySuite(t, store)
	})
}

// =============================================================================
// ServiceRepository Suite
// =============================================================================

func ServiceRepoSuite(t *testing.T, store Store) {
	ctx := context.Background()

	t.Run("Upsert_Insert", func(t *testing.T) {
		svc := &Service{
			ID:           fmt.Sprintf("suite-svc-%d", time.Now().UnixNano()),
			Name:         "test-service",
			DisplayName:  "Test Service",
			ResolvedName: "Pod: default/test-pod",
			Type:         "web_server",
			Tech:         "nginx",
			Icon:         "server",
			Namespace:    "default",
			Node:         "node-1",
			PodIP:        "10.0.0.1",
			Labels:       map[string]string{"app": "test"},
			Healthy:      true,
		}

		if err := store.Services().Upsert(ctx, svc); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}

		got, err := store.Services().Get(ctx, svc.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got == nil {
			t.Fatal("Expected service, got nil")
		}
		if got.Name != svc.Name {
			t.Errorf("Name mismatch: got %q, want %q", got.Name, svc.Name)
		}
		if got.Labels["app"] != "test" {
			t.Errorf("Labels mismatch: got %v", got.Labels)
		}

		// Cleanup
		store.Services().Delete(ctx, svc.ID)
	})

	t.Run("Upsert_Update", func(t *testing.T) {
		svc := &Service{
			ID:      fmt.Sprintf("suite-update-%d", time.Now().UnixNano()),
			Name:    "original-name",
			Type:    "web_server",
			Healthy: true,
		}
		if err := store.Services().Upsert(ctx, svc); err != nil {
			t.Fatalf("Initial Upsert failed: %v", err)
		}

		svc.Name = "updated-name"
		svc.Tech = "nginx"
		if err := store.Services().Upsert(ctx, svc); err != nil {
			t.Fatalf("Update Upsert failed: %v", err)
		}

		got, err := store.Services().Get(ctx, svc.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.Name != "updated-name" {
			t.Errorf("Name not updated: got %q", got.Name)
		}

		store.Services().Delete(ctx, svc.ID)
	})

	t.Run("Get_NotFound", func(t *testing.T) {
		got, err := store.Services().Get(ctx, "non-existent-id-12345")
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if got != nil {
			t.Errorf("Expected nil for non-existent service, got %+v", got)
		}
	})

	t.Run("List_WithFilters", func(t *testing.T) {
		prefix := fmt.Sprintf("list-%d", time.Now().UnixNano())
		services := []*Service{
			{ID: prefix + "-1", Name: "service-a", Node: "node-1", Namespace: "default", Healthy: true},
			{ID: prefix + "-2", Name: "service-b", Node: "node-1", Namespace: "production", Healthy: true},
			{ID: prefix + "-3", Name: "service-c", Node: "node-2", Namespace: "default", Healthy: true},
		}
		for _, svc := range services {
			if err := store.Services().Upsert(ctx, svc); err != nil {
				t.Fatalf("Upsert failed: %v", err)
			}
		}

		// Test filter by node
		nodeFiltered, err := store.Services().List(ctx, ServiceFilter{Node: "node-1"})
		if err != nil {
			t.Fatalf("List by node failed: %v", err)
		}
		if len(nodeFiltered) < 2 {
			t.Errorf("Expected at least 2 services for node-1, got %d", len(nodeFiltered))
		}

		// Cleanup
		for _, svc := range services {
			store.Services().Delete(ctx, svc.ID)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		svc := &Service{ID: fmt.Sprintf("delete-%d", time.Now().UnixNano()), Name: "to-delete", Healthy: true}
		store.Services().Upsert(ctx, svc)

		if err := store.Services().Delete(ctx, svc.ID); err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		got, _ := store.Services().Get(ctx, svc.ID)
		if got != nil {
			t.Error("Service still exists after delete")
		}
	})

	t.Run("Count", func(t *testing.T) {
		initialCount, _ := store.Services().Count(ctx)

		prefix := fmt.Sprintf("count-%d", time.Now().UnixNano())
		for i := 0; i < 3; i++ {
			svc := &Service{ID: fmt.Sprintf("%s-%d", prefix, i), Name: "test", Healthy: true}
			store.Services().Upsert(ctx, svc)
		}

		newCount, err := store.Services().Count(ctx)
		if err != nil {
			t.Fatalf("Count failed: %v", err)
		}
		if newCount != initialCount+3 {
			t.Errorf("Count = %d, want %d", newCount, initialCount+3)
		}

		// Cleanup
		for i := 0; i < 3; i++ {
			store.Services().Delete(ctx, fmt.Sprintf("%s-%d", prefix, i))
		}
	})
}

// =============================================================================
// ConnectionRepository Suite
// =============================================================================

func ConnectionRepoSuite(t *testing.T, store Store) {
	ctx := context.Background()

	t.Run("Upsert", func(t *testing.T) {
		conn := &Connection{
			ID:            fmt.Sprintf("conn-%d", time.Now().UnixNano()),
			SourceID:      "svc-a",
			TargetID:      "svc-b",
			Port:          8080,
			Count:         1,
			BytesSent:     1000,
			BytesRecv:     2000,
			BytesSentRate: 100.5,
			BytesRecvRate: 200.5,
		}

		if err := store.Connections().Upsert(ctx, conn); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}

		got, err := store.Connections().Get(ctx, conn.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got == nil {
			t.Fatal("Expected connection, got nil")
		}
		if got.Port != 8080 {
			t.Errorf("Port mismatch: got %d, want 8080", got.Port)
		}

		store.Connections().Delete(ctx, conn.ID)
	})

	t.Run("Upsert_IncrementsCount", func(t *testing.T) {
		conn := &Connection{
			ID:       fmt.Sprintf("conn-count-%d", time.Now().UnixNano()),
			SourceID: "svc-a",
			TargetID: "svc-b",
			Port:     80,
			Count:    1,
		}

		store.Connections().Upsert(ctx, conn)
		store.Connections().Upsert(ctx, conn)

		got, _ := store.Connections().Get(ctx, conn.ID)
		if got.Count != 2 {
			t.Errorf("Count = %d, want 2", got.Count)
		}

		store.Connections().Delete(ctx, conn.ID)
	})

	t.Run("UpdateStats", func(t *testing.T) {
		conn := &Connection{
			ID:       fmt.Sprintf("conn-stats-%d", time.Now().UnixNano()),
			SourceID: "a",
			TargetID: "b",
			Port:     80,
		}
		store.Connections().Upsert(ctx, conn)

		err := store.Connections().UpdateStats(ctx, conn.ID, 5000, 10000, 500.0, 1000.0, 50, 100)
		if err != nil {
			t.Fatalf("UpdateStats failed: %v", err)
		}

		got, _ := store.Connections().Get(ctx, conn.ID)
		if got.BytesSent != 5000 {
			t.Errorf("BytesSent = %d, want 5000", got.BytesSent)
		}

		store.Connections().Delete(ctx, conn.ID)
	})
}

// =============================================================================
// MetricsRepository Suite
// =============================================================================

func MetricsRepoSuite(t *testing.T, store Store) {
	ctx := context.Background()

	t.Run("Upsert_And_Get", func(t *testing.T) {
		nodeName := fmt.Sprintf("node-%d", time.Now().UnixNano())
		m := &NodeMetrics{
			NodeName:   nodeName,
			CPUPercent: 45.5,
			MemPercent: 60.0,
			MemUsed:    8000000000,
			MemTotal:   16000000000,
		}

		if err := store.Metrics().Upsert(ctx, m); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}

		got, err := store.Metrics().Get(ctx, nodeName)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got.CPUPercent != 45.5 {
			t.Errorf("CPUPercent = %f, want 45.5", got.CPUPercent)
		}

		store.Metrics().Delete(ctx, nodeName)
	})
}

// =============================================================================
// Store Functions Suite
// =============================================================================

func StoreFunctionsSuite(t *testing.T, store Store) {
	ctx := context.Background()

	t.Run("GetTopology", func(t *testing.T) {
		prefix := fmt.Sprintf("topo-%d", time.Now().UnixNano())

		store.Services().Upsert(ctx, &Service{ID: prefix + "-svc-1", Name: "a", Healthy: true})
		store.Services().Upsert(ctx, &Service{ID: prefix + "-svc-2", Name: "b", Healthy: true})
		store.Connections().Upsert(ctx, &Connection{ID: prefix + "-c1", SourceID: prefix + "-svc-1", TargetID: prefix + "-svc-2", Port: 80})

		topo, err := store.GetTopology(ctx)
		if err != nil {
			t.Fatalf("GetTopology failed: %v", err)
		}

		if len(topo.Services) < 2 {
			t.Errorf("Got %d services, want at least 2", len(topo.Services))
		}

		// Cleanup
		store.Services().Delete(ctx, prefix+"-svc-1")
		store.Services().Delete(ctx, prefix+"-svc-2")
		store.Connections().Delete(ctx, prefix+"-c1")
	})

	t.Run("Prune", func(t *testing.T) {
		prefix := fmt.Sprintf("prune-%d", time.Now().UnixNano())

		store.Services().Upsert(ctx, &Service{ID: prefix + "-fresh", Name: "fresh", Healthy: true})
		store.Services().Upsert(ctx, &Service{ID: prefix + "-stale", Name: "stale", Healthy: true})

		// Note: Actually making data stale requires DB-level access
		// This test just verifies Prune doesn't error on valid data
		_, err := store.Prune(ctx, 1*time.Hour)
		if err != nil {
			t.Fatalf("Prune failed: %v", err)
		}

		// Cleanup
		store.Services().Delete(ctx, prefix+"-fresh")
		store.Services().Delete(ctx, prefix+"-stale")
	})

	t.Run("Ping", func(t *testing.T) {
		if err := store.Ping(ctx); err != nil {
			t.Errorf("Ping failed: %v", err)
		}
	})

	t.Run("RunInTransaction", func(t *testing.T) {
		prefix := fmt.Sprintf("tx-%d", time.Now().UnixNano())

		err := store.RunInTransaction(ctx, func(txCtx context.Context) error {
			return store.Services().Upsert(txCtx, &Service{ID: prefix + "-svc", Name: "tx", Healthy: true})
		})
		if err != nil {
			t.Fatalf("Transaction failed: %v", err)
		}

		svc, _ := store.Services().Get(ctx, prefix+"-svc")
		if svc == nil {
			t.Error("Transaction was not committed")
		}

		store.Services().Delete(ctx, prefix+"-svc")
	})
}

// =============================================================================
// Concurrency Suite - Stress Tests
// =============================================================================

func ConcurrencySuite(t *testing.T, store Store) {
	ctx := context.Background()

	t.Run("ConcurrentServiceUpserts", func(t *testing.T) {
		prefix := fmt.Sprintf("conc-%d", time.Now().UnixNano())
		serviceID := prefix + "-shared-svc"

		// Create initial service
		store.Services().Upsert(ctx, &Service{
			ID:      serviceID,
			Name:    "concurrent-test",
			Healthy: true,
		})

		// Spawn 20 goroutines doing concurrent upserts
		var wg sync.WaitGroup
		errChan := make(chan error, 20)

		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				svc := &Service{
					ID:      serviceID,
					Name:    fmt.Sprintf("updated-%d", idx),
					Tech:    fmt.Sprintf("tech-%d", idx),
					Healthy: true,
				}
				if err := store.Services().Upsert(ctx, svc); err != nil {
					errChan <- fmt.Errorf("goroutine %d: %w", idx, err)
				}
			}(i)
		}

		wg.Wait()
		close(errChan)

		// Check for errors
		for err := range errChan {
			t.Errorf("Concurrent upsert error: %v", err)
		}

		// Verify service still exists and is consistent
		svc, err := store.Services().Get(ctx, serviceID)
		if err != nil {
			t.Fatalf("Get after concurrent upserts failed: %v", err)
		}
		if svc == nil {
			t.Fatal("Service disappeared after concurrent upserts")
		}

		store.Services().Delete(ctx, serviceID)
	})

	t.Run("ConcurrentConnectionUpdates", func(t *testing.T) {
		prefix := fmt.Sprintf("conc-conn-%d", time.Now().UnixNano())
		connID := prefix + "-shared-conn"

		// Create initial connection
		store.Connections().Upsert(ctx, &Connection{
			ID:       connID,
			SourceID: "src",
			TargetID: "dst",
			Port:     8080,
			Count:    0,
		})

		// Spawn 20 goroutines doing concurrent updates
		var wg sync.WaitGroup
		errChan := make(chan error, 20)
		numGoroutines := 20

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				// Each goroutine upserts (which increments count)
				conn := &Connection{
					ID:       connID,
					SourceID: "src",
					TargetID: "dst",
					Port:     8080,
					Count:    1,
				}
				if err := store.Connections().Upsert(ctx, conn); err != nil {
					errChan <- fmt.Errorf("goroutine %d: %w", idx, err)
				}
			}(i)
		}

		wg.Wait()
		close(errChan)

		// Check for errors
		for err := range errChan {
			t.Errorf("Concurrent update error: %v", err)
		}

		// Verify final count
		conn, err := store.Connections().Get(ctx, connID)
		if err != nil {
			t.Fatalf("Get after concurrent updates failed: %v", err)
		}
		if conn == nil {
			t.Fatal("Connection disappeared after concurrent updates")
		}

		// Count MUST be exactly numGoroutines (initial insert sets count=0, each upsert adds +1)
		// This strict assertion ensures our atomic ON CONFLICT logic is working correctly.
		// If this test ever fails, it indicates a race condition in the database layer.
		expectedCount := int64(numGoroutines)
		if conn.Count != expectedCount {
			t.Errorf("Connection count = %d, expected exactly %d (atomic increment failure)", conn.Count, expectedCount)
		}

		t.Logf("Verified atomic counter: %d concurrent upserts resulted in count = %d", numGoroutines, conn.Count)

		store.Connections().Delete(ctx, connID)
	})

	t.Run("ConcurrentReadsAndWrites", func(t *testing.T) {
		prefix := fmt.Sprintf("rw-%d", time.Now().UnixNano())

		// Create some initial data
		for i := 0; i < 5; i++ {
			store.Services().Upsert(ctx, &Service{
				ID:      fmt.Sprintf("%s-svc-%d", prefix, i),
				Name:    fmt.Sprintf("service-%d", i),
				Healthy: true,
			})
		}

		// Mix of readers and writers
		var wg sync.WaitGroup
		errChan := make(chan error, 40)

		// 10 readers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := store.Services().List(ctx, ServiceFilter{})
				if err != nil {
					errChan <- fmt.Errorf("reader: %w", err)
				}
			}()
		}

		// 10 writers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				svc := &Service{
					ID:      fmt.Sprintf("%s-svc-%d", prefix, idx%5),
					Name:    fmt.Sprintf("updated-service-%d", idx),
					Healthy: true,
				}
				if err := store.Services().Upsert(ctx, svc); err != nil {
					errChan <- fmt.Errorf("writer %d: %w", idx, err)
				}
			}(i)
		}

		wg.Wait()
		close(errChan)

		for err := range errChan {
			t.Errorf("Concurrent read/write error: %v", err)
		}

		// Cleanup
		for i := 0; i < 5; i++ {
			store.Services().Delete(ctx, fmt.Sprintf("%s-svc-%d", prefix, i))
		}
	})
}
