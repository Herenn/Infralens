// Package ebpf provides eBPF-based network tracing for the InfraLens agent.
//
// DEPRECATED: This file (tracer.go) contains legacy code that is no longer used.
// The main agent implementation in agent/main.go directly loads and attaches
// BPF programs using the generated bpfObjects from bpf_bpfel_*.go.
//
// To regenerate the BPF bindings after modifying traffic.c, run:
//
//	cd agent/ebpf && go generate
//
// This requires clang/LLVM to be installed.
package ebpf

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/perf"
	"github.com/cilium/ebpf/rlimit"
	log "github.com/sirupsen/logrus"
)

// Tracer manages the eBPF programs for tracing TCP connections.
type Tracer struct {
	objs   bpfObjects
	links  []link.Link
	reader *perf.Reader

	// Event channel for async reading
	eventCh chan *Event
	errorCh chan error

	// For graceful shutdown
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Event represents a TCP connection event from the eBPF program.
type Event struct {
	PID   uint32
	SAddr uint32
	DAddr uint32
	DPort uint16
	Comm  string
}

// NewTracer creates and initializes a new eBPF tracer.
func NewTracer() (*Tracer, error) {
	// Remove rlimit for eBPF operations (required for older kernels)
	if err := rlimit.RemoveMemlock(); err != nil {
		log.WithError(err).Warn("Failed to remove memlock rlimit, continuing anyway")
	}

	// Load pre-compiled eBPF programs and maps
	var objs bpfObjects
	if err := loadBpfObjects(&objs, nil); err != nil {
		var ve *ebpf.VerifierError
		if errors.As(err, &ve) {
			log.WithField("verifier_log", fmt.Sprintf("%+v", ve)).Error("eBPF verifier error")
		}
		return nil, fmt.Errorf("loading eBPF objects: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Tracer{
		objs:    objs,
		eventCh: make(chan *Event, 1000),
		errorCh: make(chan error, 10),
		cancel:  cancel,
	}

	// Attach kprobe on tcp_v4_connect
	kp, err := link.Kprobe("tcp_v4_connect", objs.KprobeTcpV4Connect, nil)
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("attaching kprobe/tcp_v4_connect: %w", err)
	}
	t.links = append(t.links, kp)
	log.Debug("Attached kprobe/tcp_v4_connect")

	// Open perf event reader
	reader, err := perf.NewReader(objs.Events, 4096)
	if err != nil {
		t.Close()
		return nil, fmt.Errorf("creating perf reader: %w", err)
	}
	t.reader = reader

	// Start background event reader
	t.wg.Add(1)
	go t.readLoop(ctx)

	log.Info("eBPF programs loaded and attached successfully")
	return t, nil
}

// readLoop continuously reads events from the perf buffer in the background.
func (t *Tracer) readLoop(ctx context.Context) {
	defer t.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		record, err := t.reader.Read()
		if err != nil {
			if errors.Is(err, perf.ErrClosed) {
				return
			}
			select {
			case t.errorCh <- fmt.Errorf("perf read: %w", err):
			default:
			}
			continue
		}

		// Skip lost events
		if record.LostSamples > 0 {
			log.WithField("lost", record.LostSamples).Warn("Lost perf events")
			continue
		}

		event, err := parseEvent(record.RawSample)
		if err != nil {
			log.WithError(err).Debug("Failed to parse event")
			continue
		}

		select {
		case t.eventCh <- event:
		default:
			log.Warn("Event channel full, dropping event")
		}
	}
}

// parseEvent parses raw bytes from the perf buffer into an Event.
func parseEvent(data []byte) (*Event, error) {
	if len(data) < 26 { // 4 + 4 + 4 + 2 + 16 = 30, but padding may differ
		return nil, fmt.Errorf("data too short: %d bytes", len(data))
	}

	reader := bytes.NewReader(data)

	var raw struct {
		PID   uint32
		SAddr uint32
		DAddr uint32
		DPort uint16
		_     [2]byte // padding
		Comm  [16]byte
	}

	if err := binary.Read(reader, binary.LittleEndian, &raw); err != nil {
		return nil, fmt.Errorf("binary read: %w", err)
	}

	// Convert null-terminated comm to string
	comm := string(raw.Comm[:])
	for i, b := range raw.Comm {
		if b == 0 {
			comm = string(raw.Comm[:i])
			break
		}
	}

	return &Event{
		PID:   raw.PID,
		SAddr: raw.SAddr,
		DAddr: raw.DAddr,
		DPort: raw.DPort,
		Comm:  comm,
	}, nil
}

// Events returns a channel that receives parsed events.
func (t *Tracer) Events() <-chan *Event {
	return t.eventCh
}

// Errors returns a channel that receives errors from the reader loop.
func (t *Tracer) Errors() <-chan error {
	return t.errorCh
}

// ReadEvents reads all available events from the channel without blocking.
func (t *Tracer) ReadEvents() ([]*Event, error) {
	var events []*Event
	var lastErr error

	// Drain error channel first
	for {
		select {
		case err := <-t.errorCh:
			lastErr = err
		default:
			goto readEvents
		}
	}

readEvents:
	for {
		select {
		case event := <-t.eventCh:
			events = append(events, event)
		default:
			return events, lastErr
		}
	}
}

// Close releases all resources held by the tracer.
func (t *Tracer) Close() error {
	if t.cancel != nil {
		t.cancel()
	}

	if t.reader != nil {
		t.reader.Close()
	}

	t.wg.Wait()

	close(t.eventCh)
	close(t.errorCh)

	for _, l := range t.links {
		if err := l.Close(); err != nil {
			log.WithError(err).Warn("Failed to close link")
		}
	}

	return t.objs.Close()
}
