// Package collector provides eBPF-based network event collection for the InfraLens agent.
//
// IMPORTANT: After modifying agent/bpf/traffic.c, you MUST regenerate the Go bindings!
//
// To regenerate the eBPF bindings, run from the agent/collector/ directory:
//
//	go generate
//
// Or from the repository root:
//
//	cd agent/collector && go generate
//
// Prerequisites:
//   - clang (LLVM 14+) must be installed and in PATH
//   - vmlinux.h must exist in ../bpf/headers/
//   - libbpf headers must exist in ../bpf/headers/bpf/
//
// What gets generated:
//   - bpf_bpfel_x86.go    - Go bindings for x86/amd64
//   - bpf_bpfel_arm64.go  - Go bindings for arm64
//   - bpf_bpfel_x86.o     - Compiled BPF bytecode for x86
//   - bpf_bpfel_arm64.o   - Compiled BPF bytecode for arm64
//
// The generated files contain:
//   - bpfObjects struct with all BPF programs and maps
//   - Type definitions matching C structs (event_t, conn_key_t, conn_stats_t)
//
// Current BPF programs in traffic.c:
//   - kprobe/tcp_v4_connect      - Outbound IPv4 connections
//   - kretprobe/tcp_v4_connect   - Capture IPv4 connection details
//   - kprobe/tcp_v6_connect      - Outbound IPv6 connections
//   - kretprobe/tcp_v6_connect   - Capture IPv6 connection details
//   - kprobe/tcp_sendmsg         - Track bytes sent
//   - kprobe/tcp_recvmsg         - Store socket for recv tracking
//   - kretprobe/tcp_recvmsg      - Track bytes received
//   - kprobe/tcp_close           - Connection cleanup
//   - kretprobe/inet_csk_accept  - Inbound connections (accept)

package collector

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64,arm64 -type event_t -type conn_key_t -type conn_stats_t bpf ../bpf/traffic.c -- -I../bpf/headers
