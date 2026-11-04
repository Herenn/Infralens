package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebhookDriftNotifier POSTs drift reports as JSON to a URL.
//
// The payload carries a human-readable "text" field alongside the structured
// lists, so the same URL works for a Slack (or Mattermost, or any
// Slack-compatible) incoming webhook - which renders "text" and ignores the
// rest - and for a custom receiver that wants the structured data.
type WebhookDriftNotifier struct {
	url    string
	client *http.Client
}

// NewWebhookDriftNotifier creates a notifier posting to url.
func NewWebhookDriftNotifier(url string) *WebhookDriftNotifier {
	return &WebhookDriftNotifier{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// driftWebhookPayload is the JSON body sent to the webhook.
type driftWebhookPayload struct {
	Text        string      `json:"text"`
	Source      string      `json:"source"`
	Event       string      `json:"event"`
	DetectedAt  time.Time   `json:"detected_at"`
	Appeared    []DriftEdge `json:"appeared"`
	Disappeared []DriftEdge `json:"disappeared"`

	// Totals are always complete; the lists above are capped at
	// maxDriftItems each. A receiver can compare the two to tell whether it
	// got everything.
	AppearedTotal    int `json:"appeared_total"`
	DisappearedTotal int `json:"disappeared_total"`
}

// NotifyDrift sends the report. Any non-2xx response is an error.
func (n *WebhookDriftNotifier) NotifyDrift(ctx context.Context, report *DriftReport) error {
	body, err := json.Marshal(buildDriftWebhookPayload(report))
	if err != nil {
		return fmt.Errorf("encoding drift notification: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building drift notification request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "InfraLens-Drift")

	resp, err := n.client.Do(req)
	if err != nil {
		// The URL is not included: Slack-style webhook URLs embed their
		// secret, and this error ends up in logs. *url.Error already carries
		// it, so unwrap to the underlying cause.
		return fmt.Errorf("posting drift notification: %w", unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("drift notification rejected: HTTP %d", resp.StatusCode)
	}
	return nil
}

func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

func buildDriftWebhookPayload(report *DriftReport) driftWebhookPayload {
	return driftWebhookPayload{
		Text:             driftSummaryText(report),
		Source:           "infralens",
		Event:            "topology.drift",
		DetectedAt:       report.DetectedAt,
		Appeared:         capDriftEdges(report.Appeared),
		Disappeared:      capDriftEdges(report.Disappeared),
		AppearedTotal:    len(report.Appeared),
		DisappearedTotal: len(report.Disappeared),
	}
}

func capDriftEdges(edges []DriftEdge) []DriftEdge {
	if edges == nil {
		return []DriftEdge{}
	}
	if len(edges) > maxDriftItems {
		return edges[:maxDriftItems]
	}
	return edges
}

// driftSummaryText renders the report as a short plain-text message.
func driftSummaryText(report *DriftReport) string {
	var b strings.Builder

	external := 0
	for _, e := range report.Appeared {
		if e.External {
			external++
		}
	}

	var parts []string
	if n := len(report.Appeared); n > 0 {
		p := fmt.Sprintf("%d new %s", n, plural(n, "dependency", "dependencies"))
		if external > 0 {
			p += fmt.Sprintf(" (%d to the public internet)", external)
		}
		parts = append(parts, p)
	}
	if n := len(report.Disappeared); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s no longer observed", n, plural(n, "dependency", "dependencies")))
	}
	fmt.Fprintf(&b, "InfraLens: architecture drift detected - %s", strings.Join(parts, ", "))

	writeDriftLines(&b, "New", report.Appeared)
	writeDriftLines(&b, "Gone", report.Disappeared)
	return b.String()
}

func writeDriftLines(b *strings.Builder, label string, edges []DriftEdge) {
	const maxLines = 10
	if len(edges) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:", label)
	for i, e := range edges {
		if i == maxLines {
			fmt.Fprintf(b, "\n  ...and %d more", len(edges)-maxLines)
			break
		}
		suffix := ""
		if e.External {
			suffix = " [internet]"
		}
		fmt.Fprintf(b, "\n  - %s -> %s (port %d/%s)%s", e.SourceID, e.TargetID, e.Port, protocolOrTCP(e.Protocol), suffix)
	}
}

func protocolOrTCP(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
