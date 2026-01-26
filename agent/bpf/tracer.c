// SPDX-License-Identifier: GPL-2.0
// InfraLens eBPF TCP Tracer
// Traces TCP connect and accept syscalls to detect service-to-service traffic.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_endian.h>

#define TASK_COMM_LEN 16
#define AF_INET 2

// Event types
#define EVENT_TYPE_CONNECT 0
#define EVENT_TYPE_ACCEPT  1

// TCP states we care about
#define TCP_ESTABLISHED 1

// Event structure sent to userspace
struct tcp_event {
    __u8  type;           // EVENT_TYPE_CONNECT or EVENT_TYPE_ACCEPT
    __u8  _pad[3];        // Padding for alignment
    __u32 pid;            // Process ID
    char  comm[TASK_COMM_LEN]; // Process name
    __u32 src_addr_v4;    // Source IPv4 address
    __u32 dst_addr_v4;    // Destination IPv4 address
    __u16 src_port;       // Source port
    __u16 dst_port;       // Destination port
    __u64 timestamp;      // Event timestamp
};

// Ring buffer for sending events to userspace
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 24); // 16MB buffer
} events SEC(".maps");

// Force BTF generation for tcp_event struct
const struct tcp_event *unused_tcp_event __attribute__((unused));

// Helper to emit a TCP event
static __always_inline int emit_tcp_event(void *ctx, struct sock *sk, __u8 event_type) {
    struct tcp_event *event;
    
    // Only handle IPv4 for now
    __u16 family = BPF_CORE_READ(sk, __sk_common.skc_family);
    if (family != AF_INET) {
        return 0;
    }

    // Reserve space in ring buffer
    event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
    if (!event) {
        return 0;
    }

    // Fill event data
    event->type = event_type;
    event->pid = bpf_get_current_pid_tgid() >> 32;
    bpf_get_current_comm(&event->comm, sizeof(event->comm));
    
    event->src_addr_v4 = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
    event->dst_addr_v4 = BPF_CORE_READ(sk, __sk_common.skc_daddr);
    event->src_port = BPF_CORE_READ(sk, __sk_common.skc_num);
    event->dst_port = bpf_ntohs(BPF_CORE_READ(sk, __sk_common.skc_dport));
    event->timestamp = bpf_ktime_get_ns();

    // Submit event
    bpf_ringbuf_submit(event, 0);
    return 0;
}

// Trace TCP state changes to capture established connections
// This is more reliable than tracing connect() directly
SEC("tracepoint/sock/inet_sock_set_state")
int trace_tcp_connect(struct trace_event_raw_inet_sock_set_state *ctx) {
    // Only care about transitions to ESTABLISHED state
    if (ctx->newstate != TCP_ESTABLISHED) {
        return 0;
    }
    
    // Only handle IPv4
    if (ctx->family != AF_INET) {
        return 0;
    }

    struct tcp_event *event;
    event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
    if (!event) {
        return 0;
    }

    event->type = EVENT_TYPE_CONNECT;
    event->pid = bpf_get_current_pid_tgid() >> 32;
    bpf_get_current_comm(&event->comm, sizeof(event->comm));
    
    // Read addresses from tracepoint args
    event->src_addr_v4 = ctx->saddr[0];
    event->dst_addr_v4 = ctx->daddr[0];
    event->src_port = ctx->sport;
    event->dst_port = ctx->dport;
    event->timestamp = bpf_ktime_get_ns();

    bpf_ringbuf_submit(event, 0);
    return 0;
}

// Trace accept() to capture incoming connections
SEC("kretprobe/inet_csk_accept")
int trace_tcp_accept(struct pt_regs *ctx) {
    struct sock *sk = (struct sock *)PT_REGS_RC(ctx);
    if (!sk) {
        return 0;
    }

    return emit_tcp_event(ctx, sk, EVENT_TYPE_ACCEPT);
}

char LICENSE[] SEC("license") = "GPL";
