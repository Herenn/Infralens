// Package reporter handles sending TCP events, host metrics, and service inspection data to the backend aggregator.
package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Herenn/Infralens/agent/ebpf"
	"github.com/Herenn/Infralens/agent/inspector"
	"github.com/Herenn/Infralens/agent/metrics"
	log "github.com/sirupsen/logrus"
)

// Reporter sends TCP events, host metrics, and inspection data to the backend aggregator.
type Reporter struct {
	backendURL     string
	metricsURL     string
	inspectionURL  string
	nodeName       string
	client         *http.Client
}

// EventBatch represents a batch of events sent to the backend.
type EventBatch struct {
	NodeName  string         `json:"node_name"`
	Timestamp time.Time      `json:"timestamp"`
	Events    []EventPayload `json:"events"`
}

// EventPayload is the JSON representation of a traffic event.
type EventPayload struct {
	PID     uint32 `json:"pid"`
	Comm    string `json:"comm"`
	SrcAddr string `json:"src_addr"`
	DstAddr string `json:"dst_addr"`
	DstPort uint16 `json:"dst_port"`
}

// New creates a new Reporter that sends events to the specified backend.
func New(backendAddr, nodeName string) *Reporter {
	return &Reporter{
		backendURL:    fmt.Sprintf("http://%s/api/v1/events", backendAddr),
		metricsURL:    fmt.Sprintf("http://%s/api/v1/metrics", backendAddr),
		inspectionURL: fmt.Sprintf("http://%s/api/v1/inspection", backendAddr),
		nodeName:      nodeName,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

// ReportEvents sends a batch of events to the backend.
func (r *Reporter) ReportEvents(ctx context.Context, events []*ebpf.Event) error {
	if len(events) == 0 {
		return nil
	}

	payloads := make([]EventPayload, 0, len(events))
	for _, e := range events {
		payloads = append(payloads, EventPayload{
			PID:     e.PID,
			Comm:    e.Comm,
			SrcAddr: e.SrcIP(),
			DstAddr: e.DstIP(),
			DstPort: e.DPort,
		})
	}

	batch := EventBatch{
		NodeName:  r.nodeName,
		Timestamp: time.Now(),
		Events:    payloads,
	}

	data, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("marshaling events: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.backendURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	log.WithFields(log.Fields{
		"events": len(events),
		"status": resp.StatusCode,
	}).Debug("Events reported successfully")

	return nil
}

// ReportHostMetrics sends host metrics to the backend.
func (r *Reporter) ReportHostMetrics(ctx context.Context, m *metrics.HostMetrics) error {
	if m == nil {
		return nil
	}

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling metrics: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.metricsURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	log.WithFields(log.Fields{
		"cpu":    fmt.Sprintf("%.1f%%", m.CPUPercent),
		"mem":    fmt.Sprintf("%.1f%%", m.MemPercent),
		"status": resp.StatusCode,
	}).Debug("Host metrics reported successfully")

	return nil
}

// InspectionReport wraps inspection data with node context.
type InspectionReport struct {
	NodeName   string                      `json:"node_name"`
	Timestamp  time.Time                   `json:"timestamp"`
	ServiceID  string                      `json:"service_id"`  // IP of the service
	Inspection *inspector.InspectionResult `json:"inspection"`
}

// ReportInspection sends service inspection data to the backend.
func (r *Reporter) ReportInspection(ctx context.Context, serviceID string, result *inspector.InspectionResult) error {
	if result == nil {
		return nil
	}

	report := InspectionReport{
		NodeName:   r.nodeName,
		Timestamp:  time.Now(),
		ServiceID:  serviceID,
		Inspection: result,
	}

	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshaling inspection: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.inspectionURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	log.WithFields(log.Fields{
		"service_id": serviceID,
		"pid":        result.PID,
		"status":     resp.StatusCode,
	}).Debug("Inspection data reported successfully")

	return nil
}

// Close cleans up the reporter resources.
func (r *Reporter) Close() error {
	r.client.CloseIdleConnections()
	return nil
}
