package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// DriftHandler serves the persisted architecture drift feed.
type DriftHandler struct {
	topology *service.TopologyService
}

// NewDriftHandler creates a drift feed handler.
func NewDriftHandler(topology *service.TopologyService) *DriftHandler {
	return &DriftHandler{topology: topology}
}

// DriftEventResponse is one drift event. Times are RFC 3339 with full
// sub-second precision, in UTC, so a client can hand detected_at back as
// ?since= verbatim to ask for "anything newer than this".
type DriftEventResponse struct {
	ID           int64  `json:"id"`
	DetectedAt   string `json:"detected_at"`
	Kind         string `json:"kind"`
	ConnectionID string `json:"connection_id"`
	SourceID     string `json:"source_id"`
	SourceName   string `json:"source_name,omitempty"`
	TargetID     string `json:"target_id"`
	TargetName   string `json:"target_name,omitempty"`
	Port         uint16 `json:"port"`
	Protocol     string `json:"protocol"`
	External     bool   `json:"external"`
	FirstSeen    string `json:"first_seen"`
	LastSeen     string `json:"last_seen"`
}

// DriftEventsResponse is one page of the feed, newest first. NextCursor is
// absent on the last page.
type DriftEventsResponse struct {
	Events     []DriftEventResponse `json:"events"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// DriftEventsMessage is the WebSocket "drift" message payload: events just
// recorded by the detector, newest pass only.
//
// At most MaxWSDriftEvents events are carried. A larger pass (a rollout that
// adds thousands of edges) sends the first MaxWSDriftEvents with Truncated
// set and Total the full count: clients should refetch the feed rather than
// treat the message as complete.
type DriftEventsMessage struct {
	Events    []DriftEventResponse `json:"events"`
	Total     int                  `json:"total"`
	Truncated bool                 `json:"truncated,omitempty"`
}

// MaxWSDriftEvents caps the events in one WebSocket "drift" message.
const MaxWSDriftEvents = 50

func driftEventsMessage(events []storage.DriftEvent) DriftEventsMessage {
	msg := DriftEventsMessage{Total: len(events)}
	if len(events) > MaxWSDriftEvents {
		events = events[:MaxWSDriftEvents]
		msg.Truncated = true
	}
	msg.Events = convertDriftEvents(events)
	return msg
}

// DriftSummaryResponse counts the feed for a window, plus what the detector
// is doing - enough for an unread badge and an informative empty state.
type DriftSummaryResponse struct {
	Since       *string               `json:"since"`
	Total       int64                 `json:"total"`
	Appeared    int64                 `json:"appeared"`
	Disappeared int64                 `json:"disappeared"`
	External    int64                 `json:"external"`
	Latest      *string               `json:"latest"`
	Detector    DriftDetectorResponse `json:"detector"`
}

// DriftDetectorResponse is the detector's state. Running is false while it
// loads its baseline, or when detection is switched off.
type DriftDetectorResponse struct {
	Running              bool    `json:"running"`
	WarmupUntil          *string `json:"warmup_until"`
	LastCheck            *string `json:"last_check"`
	CheckIntervalSeconds int64   `json:"check_interval_seconds,omitempty"`
	GoneAfterSeconds     int64   `json:"gone_after_seconds,omitempty"`
}

func formatDriftTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func optionalDriftTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := formatDriftTime(t)
	return &s
}

func convertDriftEvent(ev storage.DriftEvent) DriftEventResponse {
	protocol := ev.Protocol
	if protocol == "" {
		protocol = "tcp"
	}
	return DriftEventResponse{
		ID:           ev.ID,
		DetectedAt:   formatDriftTime(ev.DetectedAt),
		Kind:         string(ev.Kind),
		ConnectionID: ev.ConnectionID,
		SourceID:     ev.SourceID,
		SourceName:   ev.SourceName,
		TargetID:     ev.TargetID,
		TargetName:   ev.TargetName,
		Port:         ev.Port,
		Protocol:     protocol,
		External:     ev.External,
		FirstSeen:    formatDriftTime(ev.FirstSeen),
		LastSeen:     formatDriftTime(ev.LastSeen),
	}
}

func convertDriftEvents(events []storage.DriftEvent) []DriftEventResponse {
	out := make([]DriftEventResponse, 0, len(events))
	for _, ev := range events {
		out = append(out, convertDriftEvent(ev))
	}
	return out
}

// encodeDriftCursor / decodeDriftCursor make the keyset position opaque to
// clients, so its shape can change without breaking them.
func encodeDriftCursor(c storage.DriftEventCursor) string {
	raw := fmt.Sprintf("%d:%d", c.DetectedAt.UnixNano(), c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeDriftCursor(s string) (*storage.DriftEventCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	at, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, errors.New("malformed cursor")
	}
	nanos, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	return &storage.DriftEventCursor{DetectedAt: time.Unix(0, nanos).UTC(), ID: n}, nil
}

// parseDriftWindow reads ?since= and ?until= (RFC 3339, both exclusive).
// "Exclusive since" is what lets a client pass back the detected_at of the
// newest event it has already shown and get only newer ones. problem is a
// client-facing message, empty when the parameters are fine.
func parseDriftWindow(r *http.Request) (since, until time.Time, problem string) {
	q := r.URL.Query()
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return since, until, "Invalid 'since' parameter: expected RFC 3339 timestamp"
		}
		since = t // exclusive: passed to the store as After
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return since, until, "Invalid 'until' parameter: expected RFC 3339 timestamp"
		}
		until = t
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return since, until, "Invalid range: 'until' must not be before 'since'"
	}
	return since, until, ""
}

// HandleListEvents returns the drift feed, newest first.
//
// Query parameters (all optional):
//   - since, until: RFC 3339; only events detected strictly after / before
//   - kind: "appeared" or "disappeared"
//   - external: "true" (internet-bound only) or "false" (internal only)
//   - limit: page size, default 50, capped at 500
//   - cursor: next_cursor from the previous page
func (h *DriftHandler) HandleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	since, until, problem := parseDriftWindow(r)
	if problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}
	filter := storage.DriftEventFilter{After: since, Until: until}

	if raw := q.Get("kind"); raw != "" {
		kind := storage.DriftEventKind(raw)
		if !kind.Valid() {
			http.Error(w, "Invalid 'kind' parameter: expected 'appeared' or 'disappeared'", http.StatusBadRequest)
			return
		}
		filter.Kind = kind
	}
	if raw := q.Get("external"); raw != "" {
		external, perr := strconv.ParseBool(raw)
		if perr != nil {
			http.Error(w, "Invalid 'external' parameter: expected 'true' or 'false'", http.StatusBadRequest)
			return
		}
		filter.External = &external
	}

	limit, ok := parseLimit(r, service.DefaultDriftEventLimit)
	if !ok {
		http.Error(w, "Invalid 'limit' parameter: expected a positive integer", http.StatusBadRequest)
		return
	}
	filter.Limit = limit

	if raw := q.Get("cursor"); raw != "" {
		cursor, cerr := decodeDriftCursor(raw)
		if cerr != nil {
			http.Error(w, "Invalid 'cursor' parameter", http.StatusBadRequest)
			return
		}
		filter.Before = cursor
	}

	page, err := h.topology.ListDriftEvents(r.Context(), filter)
	if errors.Is(err, service.ErrHistoryDisabled) {
		http.Error(w, "Topology history is not enabled", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to get drift events", http.StatusInternalServerError)
		return
	}

	resp := DriftEventsResponse{Events: convertDriftEvents(page.Events)}
	if page.Next != nil {
		resp.NextCursor = encodeDriftCursor(*page.Next)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleSummary counts drift events detected after ?since= (RFC 3339,
// exclusive; default: the whole retained feed) and reports the detector's
// state. Cheap: one grouped COUNT, meant to be polled for an unread badge.
func (h *DriftHandler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	since, until, problem := parseDriftWindow(r)
	if problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}

	sum, err := h.topology.GetDriftSummary(r.Context(), storage.DriftEventFilter{After: since, Until: until})
	if errors.Is(err, service.ErrHistoryDisabled) {
		http.Error(w, "Topology history is not enabled", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Failed to get drift summary", http.StatusInternalServerError)
		return
	}

	st := h.topology.GetDriftStatus()
	resp := DriftSummaryResponse{
		Total:       sum.Total,
		Appeared:    sum.Appeared,
		Disappeared: sum.Disappeared,
		External:    sum.External,
		Detector: DriftDetectorResponse{
			Running:              st.Running,
			WarmupUntil:          optionalDriftTime(st.WarmupUntil),
			LastCheck:            optionalDriftTime(st.LastCheck),
			CheckIntervalSeconds: int64(st.Interval / time.Second),
			GoneAfterSeconds:     int64(st.GoneAfter / time.Second),
		},
	}
	if raw := r.URL.Query().Get("since"); raw != "" {
		resp.Since = &raw
	}
	if sum.Latest != nil {
		resp.Latest = optionalDriftTime(*sum.Latest)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
