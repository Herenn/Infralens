// Package service provides the business logic layer for InfraLens.
package service

import (
	"encoding/json"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// EventType represents the type of topology event.
type EventType string

const (
	EventServiceCreated    EventType = "service.created"
	EventServiceUpdated    EventType = "service.updated"
	EventServiceDeleted    EventType = "service.deleted"
	EventConnectionCreated EventType = "connection.created"
	EventConnectionUpdated EventType = "connection.updated"
	EventConnectionDeleted EventType = "connection.deleted"
	EventMetricsUpdated    EventType = "metrics.updated"
	EventTopologySnapshot  EventType = "topology.snapshot"
)

// Event represents a topology change event.
type Event struct {
	Type      EventType   `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// ServiceEvent contains service-related event data.
type ServiceEvent struct {
	ServiceID string `json:"service_id"`
	Name      string `json:"name,omitempty"`
	Node      string `json:"node,omitempty"`
	Type      string `json:"type,omitempty"`
	Healthy   bool   `json:"healthy"`
}

// ConnectionEvent contains connection-related event data.
type ConnectionEvent struct {
	ConnectionID  string  `json:"connection_id"`
	SourceID      string  `json:"source_id"`
	TargetID      string  `json:"target_id"`
	Port          uint16  `json:"port"`
	BytesSentRate float64 `json:"bytes_sent_rate,omitempty"`
	BytesRecvRate float64 `json:"bytes_recv_rate,omitempty"`
}

// MetricsEvent contains node metrics event data.
type MetricsEvent struct {
	NodeName   string  `json:"node_name"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float64 `json:"mem_percent"`
}

// Subscriber represents a client subscribed to events.
type Subscriber struct {
	ID     string
	Events chan Event
	Done   chan struct{}
}

// EventBus provides a pub/sub mechanism for topology events.
// It allows WebSocket handlers to subscribe to real-time updates
// instead of polling the full topology repeatedly.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string]*Subscriber
	bufferSize  int
	closed      bool
}

// NewEventBus creates a new event bus.
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	return &EventBus{
		subscribers: make(map[string]*Subscriber),
		bufferSize:  bufferSize,
	}
}

// Subscribe creates a new subscription and returns a subscriber.
func (eb *EventBus) Subscribe(id string) *Subscriber {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	// Close existing subscription with same ID
	if existing, ok := eb.subscribers[id]; ok {
		close(existing.Done)
		delete(eb.subscribers, id)
	}

	sub := &Subscriber{
		ID:     id,
		Events: make(chan Event, eb.bufferSize),
		Done:   make(chan struct{}),
	}
	eb.subscribers[id] = sub

	log.WithField("subscriber", id).Debug("New event subscriber")
	return sub
}

// Unsubscribe removes a subscription.
func (eb *EventBus) Unsubscribe(id string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if sub, ok := eb.subscribers[id]; ok {
		close(sub.Done)
		delete(eb.subscribers, id)
		log.WithField("subscriber", id).Debug("Subscriber removed")
	}
}

// Publish sends an event to all subscribers.
// Events are dropped if a subscriber's buffer is full.
func (eb *EventBus) Publish(event Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	if eb.closed {
		return
	}

	event.Timestamp = time.Now()

	for id, sub := range eb.subscribers {
		select {
		case sub.Events <- event:
			// Event delivered
		default:
			// Buffer full, skip this subscriber
			log.WithField("subscriber", id).Warn("Event buffer full, dropping event")
		}
	}
}

// PublishServiceCreated publishes a service created event.
func (eb *EventBus) PublishServiceCreated(svc ServiceEvent) {
	eb.Publish(Event{
		Type: EventServiceCreated,
		Data: svc,
	})
}

// PublishServiceUpdated publishes a service updated event.
func (eb *EventBus) PublishServiceUpdated(svc ServiceEvent) {
	eb.Publish(Event{
		Type: EventServiceUpdated,
		Data: svc,
	})
}

// PublishServiceDeleted publishes a service deleted event.
func (eb *EventBus) PublishServiceDeleted(serviceID string) {
	eb.Publish(Event{
		Type: EventServiceDeleted,
		Data: map[string]string{"service_id": serviceID},
	})
}

// PublishConnectionCreated publishes a connection created event.
func (eb *EventBus) PublishConnectionCreated(conn ConnectionEvent) {
	eb.Publish(Event{
		Type: EventConnectionCreated,
		Data: conn,
	})
}

// PublishConnectionUpdated publishes a connection updated event.
func (eb *EventBus) PublishConnectionUpdated(conn ConnectionEvent) {
	eb.Publish(Event{
		Type: EventConnectionUpdated,
		Data: conn,
	})
}

// PublishMetricsUpdated publishes a metrics updated event.
func (eb *EventBus) PublishMetricsUpdated(metrics MetricsEvent) {
	eb.Publish(Event{
		Type: EventMetricsUpdated,
		Data: metrics,
	})
}

// PublishTopologySnapshot publishes a full topology snapshot.
// Used for initial state when a client connects.
func (eb *EventBus) PublishTopologySnapshot(topology interface{}) {
	eb.Publish(Event{
		Type: EventTopologySnapshot,
		Data: topology,
	})
}

// SubscriberCount returns the number of active subscribers.
func (eb *EventBus) SubscriberCount() int {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.subscribers)
}

// Close closes the event bus and all subscriptions.
func (eb *EventBus) Close() {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	eb.closed = true
	for id, sub := range eb.subscribers {
		close(sub.Done)
		delete(eb.subscribers, id)
	}
}

// MarshalEvent serializes an event to JSON.
func MarshalEvent(e Event) ([]byte, error) {
	return json.Marshal(e)
}
