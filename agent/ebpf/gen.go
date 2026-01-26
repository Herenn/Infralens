// Package ebpf contains the go:generate directive for compiling the eBPF program.
//
// To regenerate the eBPF bindings, run from the agent/ directory:
//
//	go generate ./...
//
// Or from the agent/ebpf/ directory:
//
//	go generate
//
// Prerequisites:
//   - clang (LLVM) must be installed and in PATH
//   - vmlinux.h must exist in ../bpf/headers/
//   - libbpf headers must exist in ../bpf/headers/bpf/

package ebpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64,arm64 -type event_t -type conn_key_t -type conn_stats_t bpf ../bpf/traffic.c -- -I../bpf/headers
