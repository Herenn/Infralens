// SPDX-License-Identifier: GPL-2.0
// InfraLens eBPF TCP Tracer (Dual-Stack IPv4/IPv6)
// Traces TCP connect and accept syscalls to detect service-to-service traffic.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_endian.h>

#define TASK_COMM_LEN 16
#define AF_INET  2
#define AF_INET6 10

// Event types
#define EVENT_TYPE_CONNECT 0
#define EVENT_TYPE_ACCEPT  1

// TCP states we care about
#define TCP_ESTABLISHED 1

// Event structure sent to userspace (Dual-Stack: supports both IPv4 and IPv6)
// Layout (72 bytes total):
//   offset 0:  type        (1 byte)
//   offset 1:  af          (1 byte) - AF_INET (2) or AF_INET6 (10)
//   offset 2:  _pad        (2 bytes)
//   offset 4:  pid         (4 bytes)
//   offset 8:  comm        (16 bytes)
//   offset 24: src_addr_v4 (4 bytes)
//   offset 28: dst_addr_v4 (4 bytes)
//   offset 32: src_addr_v6 (16 bytes)
//   offset 48: dst_addr_v6 (16 bytes)
//   offset 64: src_port    (2 bytes)
//   offset 66: dst_port    (2 bytes)
//   offset 68: timestamp   (8 bytes) - but we'll keep it simple for now
struct tcp_event {
    __u8  type;              // EVENT_TYPE_CONNECT or EVENT_TYPE_ACCEPT
    __u8  af;                // Address family: AF_INET (2) or AF_INET6 (10)
    __u8  _pad[2];           // Padding for alignment
    __u32 pid;               // Process ID
    char  comm[TASK_COMM_LEN]; // Process name
    __u32 src_addr_v4;       // Source IPv4 address
    __u32 dst_addr_v4;       // Destination IPv4 address
    __u8  src_addr_v6[16];   // Source IPv6 address
    __u8  dst_addr_v6[16];   // Destination IPv6 address
    __u16 src_port;          // Source port
    __u16 dst_port;          // Destination port
};

// Ring buffer for sending events to userspace
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 24); // 16MB buffer
} events SEC(".maps");

// Force BTF generation for tcp_event struct
const struct tcp_event *unused_tcp_event __attribute__((unused));

// Helper to emit a TCP event (supports both IPv4 and IPv6)
static __always_inline int emit_tcp_event(void *ctx, struct sock *sk, __u8 event_type) {
    struct tcp_event *event;
    
    // Get address family - support both IPv4 and IPv6
    __u16 family = BPF_CORE_READ(sk, __sk_common.skc_family);
    if (family != AF_INET && family != AF_INET6) {
        return 0;  // Unsupported address family
    }

    // Reserve space in ring buffer
    event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
    if (!event) {
        return 0;
    }

    // Zero out the struct to avoid leaking kernel memory
    __builtin_memset(event, 0, sizeof(*event));

    // Fill common event data
    event->type = event_type;
    event->af = (__u8)family;
    event->pid = bpf_get_current_pid_tgid() >> 32;
    bpf_get_current_comm(&event->comm, sizeof(event->comm));
    event->src_port = BPF_CORE_READ(sk, __sk_common.skc_num);
    event->dst_port = bpf_ntohs(BPF_CORE_READ(sk, __sk_common.skc_dport));

    // Fill address data based on family
    if (family == AF_INET) {
        event->src_addr_v4 = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
        event->dst_addr_v4 = BPF_CORE_READ(sk, __sk_common.skc_daddr);
    } else {
        // IPv6
        BPF_CORE_READ_INTO(&event->src_addr_v6, sk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
        BPF_CORE_READ_INTO(&event->dst_addr_v6, sk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
    }

    // Submit event
    bpf_ringbuf_submit(event, 0);
    return 0;
}

// Trace TCP state changes to capture established connections (IPv4 + IPv6)
// This is more reliable than tracing connect() directly
SEC("tracepoint/sock/inet_sock_set_state")
int trace_tcp_connect(struct trace_event_raw_inet_sock_set_state *ctx) {
    // Only care about transitions to ESTABLISHED state
    if (ctx->newstate != TCP_ESTABLISHED) {
        return 0;
    }
    
    // Support both IPv4 and IPv6
    __u16 family = ctx->family;
    if (family != AF_INET && family != AF_INET6) {
        return 0;
    }

    struct tcp_event *event;
    event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
    if (!event) {
        return 0;
    }

    // Zero out the struct to avoid leaking kernel memory
    __builtin_memset(event, 0, sizeof(*event));

    event->type = EVENT_TYPE_CONNECT;
    event->af = (__u8)family;
    event->pid = bpf_get_current_pid_tgid() >> 32;
    bpf_get_current_comm(&event->comm, sizeof(event->comm));
    event->src_port = ctx->sport;
    event->dst_port = ctx->dport;

    // Read addresses from tracepoint args based on family
    if (family == AF_INET) {
        event->src_addr_v4 = ctx->saddr[0];
        event->dst_addr_v4 = ctx->daddr[0];
    } else {
        // IPv6: saddr and daddr are 4 x __u32 arrays representing 128-bit addresses
        // Copy all 16 bytes (4 x 4 bytes)
        __builtin_memcpy(event->src_addr_v6, ctx->saddr, 16);
        __builtin_memcpy(event->dst_addr_v6, ctx->daddr, 16);
    }

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
