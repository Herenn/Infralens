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

// Event byte offsets matching the C struct tcp_event in tracer.c
// This avoids relying on Go struct alignment matching C compiler behavior.
//
// C struct layout (72 bytes):
//   offset 0:  type        (1 byte)
//   offset 1:  af          (1 byte)
//   offset 2:  _pad        (2 bytes)
//   offset 4:  pid         (4 bytes)
//   offset 8:  comm        (16 bytes)
//   offset 24: src_addr_v4 (4 bytes)
//   offset 28: dst_addr_v4 (4 bytes)
//   offset 32: src_addr_v6 (16 bytes)
//   offset 48: dst_addr_v6 (16 bytes)
//   offset 64: src_port    (2 bytes)
//   offset 66: dst_port    (2 bytes)
const (
	eventMinSize       = 68 // Minimum bytes needed to parse an event
	offsetType         = 0
	offsetAF           = 1
	offsetPID          = 4
	offsetComm         = 8
	offsetSrcAddrV4    = 24
	offsetDstAddrV4    = 28
	offsetSrcAddrV6    = 32
	offsetDstAddrV6    = 48
	offsetSrcPort      = 64
	offsetDstPort      = 66
	commLen            = 16
)

// parseEvent parses raw bytes from the perf buffer into an Event.
// Uses explicit byte offsets to be safe against C compiler alignment differences.
func parseEvent(data []byte) (*Event, error) {
	if len(data) < eventMinSize {
		return nil, fmt.Errorf("data too short: got %d bytes, need at least %d", len(data), eventMinSize)
	}

	// Read fields at explicit offsets (no struct alignment assumptions)
	// eventType := data[offsetType]  // Currently unused in Event struct
	// af := data[offsetAF]           // Address family (2=IPv4, 10=IPv6)
	pid := binary.LittleEndian.Uint32(data[offsetPID:])
	saddr := binary.LittleEndian.Uint32(data[offsetSrcAddrV4:])
	daddr := binary.LittleEndian.Uint32(data[offsetDstAddrV4:])
	dport := binary.LittleEndian.Uint16(data[offsetDstPort:])

	// Extract null-terminated comm string
	commBytes := data[offsetComm : offsetComm+commLen]
	comm := extractNullTerminatedString(commBytes)

	return &Event{
		PID:   pid,
		SAddr: saddr,
		DAddr: daddr,
		DPort: dport,
		Comm:  comm,
	}, nil
}

// extractNullTerminatedString extracts a string from a null-terminated byte slice.
func extractNullTerminatedString(data []byte) string {
	for i, b := range data {
		if b == 0 {
			return string(data[:i])
		}
	}
	return string(data)
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
