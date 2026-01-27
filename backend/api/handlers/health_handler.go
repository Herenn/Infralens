package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/Herenn/Infralens/backend/k8s"
	"github.com/Herenn/Infralens/backend/storage"
)

// Version is the backend version (set at build time)
var Version = "0.2.0"

// HealthHandler handles health and status endpoints.
type HealthHandler struct {
	store      storage.Store
	k8sWatcher *k8s.Watcher
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(store storage.Store, k8sWatcher *k8s.Watcher) *HealthHandler {
	return &HealthHandler{
		store:      store,
		k8sWatcher: k8sWatcher,
	}
}

// HandleHealth returns the health status.
func (h *HealthHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// HandleReady returns the readiness status.
func (h *HealthHandler) HandleReady(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check database connection
	if err := h.store.Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "not ready",
			"error":  "database connection failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

// HandleK8sStatus returns Kubernetes watcher status.
func (h *HealthHandler) HandleK8sStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"enabled": h.k8sWatcher.IsEnabled(),
		"cache":   h.k8sWatcher.Stats(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// HandleVersion returns the backend version.
func (h *HealthHandler) HandleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"version": Version,
	})
}
