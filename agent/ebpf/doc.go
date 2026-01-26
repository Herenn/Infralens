// Package ebpf provides eBPF-based network tracing for the InfraLens agent.
//
// This package uses the cilium/ebpf library to load and manage eBPF programs
// that trace TCP connect and accept syscalls. The traced events are exposed
// through a ring buffer and can be consumed via a Go channel.
//
// # Building
//
// The eBPF programs are compiled using bpf2go. To regenerate the Go bindings:
//
//	cd agent/ebpf
//	go generate ./...
//
// This requires:
//   - clang (for compiling C to BPF bytecode)
//   - vmlinux.h in ../bpf/headers/ (for BTF/CO-RE support)
//
// # Architecture
//
// The tracer attaches to the following kernel functions:
//   - kprobe/tcp_v4_connect: Entry point for outgoing TCP connections
//   - kretprobe/tcp_v4_connect: Return point (checks success)
//   - kretprobe/inet_csk_accept: Return point for accepted connections
//
// Events are sent to userspace via a BPF ring buffer, which is more efficient
// than perf buffers for high-throughput scenarios.
//
// # Events
//
// Two types of events are traced:
//   - EventTypeConnect (0): Outgoing TCP connection initiated
//   - EventTypeAccept (1): Incoming TCP connection accepted
//
// Each TrafficEvent includes:
//   - Process ID (PID) and Thread ID (TID)
//   - Process name (comm)
//   - Source and destination IPv4 addresses
//   - Source and destination ports
//   - Timestamp (nanoseconds since boot)
//
// # Usage
//
//	tracer, err := ebpf.NewTracer()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer tracer.Close()
//
//	// Option 1: Read from channel
//	for event := range tracer.Events() {
//	    fmt.Printf("%s: %s:%d -> %s:%d (pid=%d, comm=%s)\n",
//	        event.Type, event.SrcAddr, event.SrcPort,
//	        event.DstAddr, event.DstPort, event.PID, event.Comm)
//	}
//
//	// Option 2: Batch read
//	events, err := tracer.ReadEvents()
//	if err != nil {
//	    log.Warn(err)
//	}
//	for _, e := range events {
//	    // process event
//	}
package ebpf
