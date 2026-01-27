// Package handlers provides HTTP handlers for the InfraLens API.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	log "github.com/sirupsen/logrus"
)

// EventHandler handles event ingestion endpoints.
type EventHandler struct {
	processor *service.EventProcessor
}

// NewEventHandler creates a new event handler.
func NewEventHandler(processor *service.EventProcessor) *EventHandler {
	return &EventHandler{processor: processor}
}

// EventBatch represents a batch of events from an agent.
type EventBatch struct {
	NodeName    string             `json:"node_name"`
	ClusterName string             `json:"cluster_name,omitempty"`
	Timestamp   time.Time          `json:"timestamp"`
	Events      []service.TCPEvent `json:"events"`
}

// ThroughputReport represents throughput stats from an agent.
type ThroughputReport struct {
	NodeName    string                    `json:"node_name"`
	ClusterName string                    `json:"cluster_name,omitempty"`
	Timestamp   time.Time                 `json:"timestamp"`
	IntervalMs  int64                     `json:"interval_ms"`
	Connections []service.ThroughputStats `json:"connections"`
}

// InspectionReport represents deep inspection data from an agent.
type InspectionReport struct {
	NodeName   string                     `json:"node_name"`
	Timestamp  time.Time                  `json:"timestamp"`
	ServiceID  string                     `json:"service_id"`
	Inspection *service.ServiceInspection `json:"inspection"`
}

// HandleEvents processes incoming TCP events from agents.
func (h *EventHandler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	var batch EventBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Use cluster name for grouping if provided
	nodeName := batch.NodeName
	if batch.ClusterName != "" {
		nodeName = batch.ClusterName
	}

	log.WithFields(log.Fields{
		"node":    batch.NodeName,
		"cluster": batch.ClusterName,
		"group":   nodeName,
		"events":  len(batch.Events),
	}).Debug("Received event batch")

	ctx := r.Context()
	for _, event := range batch.Events {
		if err := h.processor.ProcessTCPEvent(ctx, nodeName, event); err != nil {
			log.WithError(err).Warn("Failed to process TCP event")
		}
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// HandleStats processes incoming throughput stats from agents.
func (h *EventHandler) HandleStats(w http.ResponseWriter, r *http.Request) {
	var report ThroughputReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	nodeName := report.NodeName
	if report.ClusterName != "" {
		nodeName = report.ClusterName
	}

	log.WithFields(log.Fields{
		"node":        report.NodeName,
		"connections": len(report.Connections),
		"interval_ms": report.IntervalMs,
	}).Debug("Received throughput report")

	ctx := r.Context()
	for _, stats := range report.Connections {
		if err := h.processor.ProcessThroughputStats(ctx, nodeName, stats); err != nil {
			log.WithError(err).Warn("Failed to process throughput stats")
		}
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// HandleMetrics processes incoming host metrics from agents.
func (h *EventHandler) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	var metrics service.HostMetrics
	if err := json.NewDecoder(r.Body).Decode(&metrics); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	log.WithFields(log.Fields{
		"node":    metrics.NodeName,
		"cluster": metrics.ClusterName,
		"cpu":     fmt.Sprintf("%.1f%%", metrics.CPUPercent),
		"mem":     fmt.Sprintf("%.1f%%", metrics.MemPercent),
	}).Debug("Received host metrics")

	ctx := r.Context()
	if err := h.processor.ProcessHostMetrics(ctx, metrics); err != nil {
		log.WithError(err).Error("Failed to process host metrics")
		http.Error(w, "Failed to process metrics", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// HandleInspection processes deep inspection data from agents.
func (h *EventHandler) HandleInspection(w http.ResponseWriter, r *http.Request) {
	var report InspectionReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	log.WithFields(log.Fields{
		"node":       report.NodeName,
		"service_id": report.ServiceID,
	}).Debug("Received inspection data")

	ctx := r.Context()
	if err := h.processor.ProcessInspection(ctx, report.ServiceID, report.Inspection); err != nil {
		log.WithError(err).Error("Failed to process inspection data")
		http.Error(w, "Failed to process inspection", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}
