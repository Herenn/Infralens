// SPDX-License-Identifier: GPL-2.0
// InfraLens eBPF Traffic Tracer
// Traces TCP connections and collects network metrics (bytes sent/received)

#include "headers/vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_tracing.h>

#define AF_INET  2
#define AF_INET6 10
#define TASK_COMM_LEN 16

// BPF_F_CURRENT_CPU flag for bpf_perf_event_output
#ifndef BPF_F_CURRENT_CPU
#define BPF_F_CURRENT_CPU 0xFFFFFFFFULL
#endif

// ============================================================================
// Data Structures
// ============================================================================

// Direction constants
#define DIRECTION_OUTBOUND 0  // Client initiated (connect)
#define DIRECTION_INBOUND  1  // Server accepted (accept)

// Event structure sent to userspace (for new connections)
struct event_t {
    __u32 pid;                  // Process ID
    __u32 af;                   // Address family (AF_INET or AF_INET6)
    __u32 saddr_v4;             // Source IPv4 address
    __u32 daddr_v4;             // Destination IPv4 address
    __u8  saddr_v6[16];         // Source IPv6 address
    __u8  daddr_v6[16];         // Destination IPv6 address
    __u16 dport;                // Destination port (network byte order)
    __u8  direction;            // 0 = outbound (connect), 1 = inbound (accept)
    __u8  _pad;                 // Padding for alignment
    char comm[TASK_COMM_LEN];   // Process name
};

// Connection key for stats tracking (using sock pointer as unique ID)
struct conn_key_t {
    __u64 sock_ptr;             // Socket pointer (unique identifier)
};

// Connection stats aggregated in kernel
struct conn_stats_t {
    __u64 bytes_sent;           // Total bytes sent
    __u64 bytes_recv;           // Total bytes received
    __u64 packets_sent;         // Packet count sent
    __u64 packets_recv;         // Packet count received
    __u32 pid;                  // Process ID
    __u32 af;                   // Address family
    __u32 saddr_v4;             // Source IPv4
    __u32 daddr_v4;             // Destination IPv4
    __u8  saddr_v6[16];         // Source IPv6
    __u8  daddr_v6[16];         // Destination IPv6
    __u16 dport;                // Destination port
    __u16 sport;                // Source port
    char comm[TASK_COMM_LEN];   // Process name
    __u64 last_update;          // Last update timestamp (ns)
};

// Force BTF type emission
const struct event_t *__unused_event __attribute__((unused));
const struct conn_stats_t *__unused_stats __attribute__((unused));

// ============================================================================
// BPF Maps
// ============================================================================

// Perf event array for sending connection events to userspace
struct {
    __uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
    __uint(key_size, sizeof(__u32));
    __uint(value_size, sizeof(__u32));
} events SEC(".maps");

// Hash map to store sock pointer between entry and return (for connect probes)
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, __u64);
    __type(value, struct sock *);
} sock_store SEC(".maps");

// LRU hash map to track connection stats (auto-evicts oldest entries when full)
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 16384);
    __type(key, struct conn_key_t);
    __type(value, struct conn_stats_t);
} conn_stats SEC(".maps");

// ============================================================================
// Helper: Initialize connection stats from socket
// ============================================================================

static __always_inline void init_conn_stats(struct conn_stats_t *stats, struct sock *sk, __u32 pid)
{
    __builtin_memset(stats, 0, sizeof(*stats));
    
    stats->pid = pid;
    stats->af = BPF_CORE_READ(sk, __sk_common.skc_family);
    stats->last_update = bpf_ktime_get_ns();
    
    bpf_get_current_comm(&stats->comm, sizeof(stats->comm));
    
    if (stats->af == AF_INET) {
        stats->saddr_v4 = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
        stats->daddr_v4 = BPF_CORE_READ(sk, __sk_common.skc_daddr);
    } else if (stats->af == AF_INET6) {
        BPF_CORE_READ_INTO(&stats->saddr_v6, sk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
        BPF_CORE_READ_INTO(&stats->daddr_v6, sk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
    }
    
    stats->dport = BPF_CORE_READ(sk, __sk_common.skc_dport);
    stats->sport = BPF_CORE_READ(sk, __sk_common.skc_num);
}

// ============================================================================
// IPv4: tcp_v4_connect
// ============================================================================

SEC("kprobe/tcp_v4_connect")
int BPF_KPROBE(kprobe_tcp_v4_connect, struct sock *sk)
{
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    bpf_map_update_elem(&sock_store, &pid_tgid, &sk, BPF_ANY);
    return 0;
}

SEC("kretprobe/tcp_v4_connect")
int BPF_KRETPROBE(kretprobe_tcp_v4_connect, int ret)
{
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    struct sock **skp;
    struct sock *sk;
    struct event_t event = {};

    skp = bpf_map_lookup_elem(&sock_store, &pid_tgid);
    if (!skp) return 0;
    sk = *skp;
    bpf_map_delete_elem(&sock_store, &pid_tgid);
    
    if (!sk) return 0;
    
    // Only report successful connections (0 = success, -115 = EINPROGRESS)
    if (ret != 0 && ret != -115) return 0;

    // Send connection event
    event.af = AF_INET;
    event.pid = pid_tgid >> 32;
    event.direction = DIRECTION_OUTBOUND;  // Outgoing connection
    bpf_get_current_comm(&event.comm, sizeof(event.comm));
    
    event.saddr_v4 = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
    event.daddr_v4 = BPF_CORE_READ(sk, __sk_common.skc_daddr);
    event.dport = BPF_CORE_READ(sk, __sk_common.skc_dport);

    bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, &event, sizeof(event));

    // Initialize stats tracking for this connection
    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    struct conn_stats_t stats = {};
    init_conn_stats(&stats, sk, event.pid);
    bpf_map_update_elem(&conn_stats, &key, &stats, BPF_ANY);

    return 0;
}

// ============================================================================
// IPv6: tcp_v6_connect  
// ============================================================================

SEC("kprobe/tcp_v6_connect")
int BPF_KPROBE(kprobe_tcp_v6_connect, struct sock *sk)
{
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    bpf_map_update_elem(&sock_store, &pid_tgid, &sk, BPF_ANY);
    return 0;
}

SEC("kretprobe/tcp_v6_connect")
int BPF_KRETPROBE(kretprobe_tcp_v6_connect, int ret)
{
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    struct sock **skp;
    struct sock *sk;
    struct event_t event = {};

    skp = bpf_map_lookup_elem(&sock_store, &pid_tgid);
    if (!skp) return 0;
    sk = *skp;
    bpf_map_delete_elem(&sock_store, &pid_tgid);
    
    if (!sk) return 0;
    
    // Only report successful connections
    if (ret != 0 && ret != -115) return 0;

    // Send connection event
    event.af = AF_INET6;
    event.pid = pid_tgid >> 32;
    event.direction = DIRECTION_OUTBOUND;  // Outgoing connection
    bpf_get_current_comm(&event.comm, sizeof(event.comm));
    
    BPF_CORE_READ_INTO(&event.saddr_v6, sk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
    BPF_CORE_READ_INTO(&event.daddr_v6, sk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
    event.dport = BPF_CORE_READ(sk, __sk_common.skc_dport);

    bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, &event, sizeof(event));

    // Initialize stats tracking for this connection
    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    struct conn_stats_t stats = {};
    init_conn_stats(&stats, sk, event.pid);
    bpf_map_update_elem(&conn_stats, &key, &stats, BPF_ANY);

    return 0;
}

// ============================================================================
// tcp_sendmsg: Track bytes sent
// int tcp_sendmsg(struct sock *sk, struct msghdr *msg, size_t size)
// We only need sk (arg1) and size (arg3), so we use PT_REGS to avoid msghdr issues
// ============================================================================

SEC("kprobe/tcp_sendmsg")
int kprobe_tcp_sendmsg(struct pt_regs *ctx)
{
    struct sock *sk = (struct sock *)PT_REGS_PARM1(ctx);
    size_t size = (size_t)PT_REGS_PARM3(ctx);

    if (!sk || size == 0) return 0;

    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    struct conn_stats_t *stats;

    stats = bpf_map_lookup_elem(&conn_stats, &key);
    if (stats) {
        // Update existing connection stats
        __sync_fetch_and_add(&stats->bytes_sent, size);
        __sync_fetch_and_add(&stats->packets_sent, 1);
        stats->last_update = bpf_ktime_get_ns();
    } else {
        // Connection not tracked (maybe established before agent started)
        // Initialize new stats entry
        struct conn_stats_t new_stats = {};
        __u64 pid_tgid = bpf_get_current_pid_tgid();
        init_conn_stats(&new_stats, sk, pid_tgid >> 32);
        new_stats.bytes_sent = size;
        new_stats.packets_sent = 1;
        bpf_map_update_elem(&conn_stats, &key, &new_stats, BPF_NOEXIST);
    }

    return 0;
}

// ============================================================================
// tcp_recvmsg: Track bytes received
// We need entry + return probes to capture sk pointer and return value
// ============================================================================

// Store sock pointer from tcp_recvmsg entry
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, __u64);   // pid_tgid
    __type(value, struct sock *);
} recvmsg_sock_store SEC(".maps");

SEC("kprobe/tcp_recvmsg")
int kprobe_tcp_recvmsg(struct pt_regs *ctx)
{
    struct sock *sk = (struct sock *)PT_REGS_PARM1(ctx);
    if (!sk) return 0;

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    bpf_map_update_elem(&recvmsg_sock_store, &pid_tgid, &sk, BPF_ANY);
    return 0;
}

SEC("kretprobe/tcp_recvmsg")
int kretprobe_tcp_recvmsg(struct pt_regs *ctx)
{
    int ret = PT_REGS_RC(ctx);
    if (ret <= 0) return 0;  // No data received or error

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    struct sock **skp = bpf_map_lookup_elem(&recvmsg_sock_store, &pid_tgid);
    if (!skp) return 0;

    struct sock *sk = *skp;
    bpf_map_delete_elem(&recvmsg_sock_store, &pid_tgid);

    if (!sk) return 0;

    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    struct conn_stats_t *stats = bpf_map_lookup_elem(&conn_stats, &key);
    if (stats) {
        __sync_fetch_and_add(&stats->bytes_recv, ret);
        __sync_fetch_and_add(&stats->packets_recv, 1);
        stats->last_update = bpf_ktime_get_ns();
    }

    return 0;
}

// ============================================================================
// tcp_close: Clean up connection stats when socket closes
// void tcp_close(struct sock *sk, long timeout)
// ============================================================================

SEC("kprobe/tcp_close")
int kprobe_tcp_close(struct pt_regs *ctx)
{
    struct sock *sk = (struct sock *)PT_REGS_PARM1(ctx);
    if (!sk) return 0;

    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    
    // Note: We don't delete immediately - let the LRU handle eviction
    // This allows userspace to read final stats before cleanup
    // The entry will be auto-evicted when map is full
    
    // Update timestamp to mark as recently seen
    struct conn_stats_t *stats = bpf_map_lookup_elem(&conn_stats, &key);
    if (stats) {
        stats->last_update = bpf_ktime_get_ns();
    }

    return 0;
}

// ============================================================================
// inet_csk_accept: Capture incoming (accepted) TCP connections
// struct sock *inet_csk_accept(struct sock *sk, int flags, int *err, bool kern)
// Returns the new child socket for the accepted connection
// ============================================================================

SEC("kretprobe/inet_csk_accept")
int kretprobe_inet_csk_accept(struct pt_regs *ctx)
{
    struct sock *newsk = (struct sock *)PT_REGS_RC(ctx);
    if (!newsk) return 0;  // Accept failed or no pending connection

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u16 family = BPF_CORE_READ(newsk, __sk_common.skc_family);

    struct event_t event = {};
    event.pid = pid_tgid >> 32;
    event.direction = DIRECTION_INBOUND;  // Incoming connection (accepted)
    bpf_get_current_comm(&event.comm, sizeof(event.comm));

    if (family == AF_INET) {
        event.af = AF_INET;
        // For accepted connections, skc_rcv_saddr is our local address (dst from client's view)
        // skc_daddr is the remote peer (src from client's view)
        // We swap them so the event looks like: remote -> local
        event.saddr_v4 = BPF_CORE_READ(newsk, __sk_common.skc_daddr);       // Remote (peer/client)
        event.daddr_v4 = BPF_CORE_READ(newsk, __sk_common.skc_rcv_saddr);   // Local (us/server)
        event.dport = BPF_CORE_READ(newsk, __sk_common.skc_num);            // Our listening port
    } else if (family == AF_INET6) {
        event.af = AF_INET6;
        // Same swap for IPv6
        BPF_CORE_READ_INTO(&event.saddr_v6, newsk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
        BPF_CORE_READ_INTO(&event.daddr_v6, newsk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
        event.dport = BPF_CORE_READ(newsk, __sk_common.skc_num);
    } else {
        return 0;  // Unsupported address family
    }

    // Convert port to network byte order (to match connect events)
    event.dport = __builtin_bswap16(event.dport);

    bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, &event, sizeof(event));

    // Initialize stats tracking for this accepted connection
    struct conn_key_t key = { .sock_ptr = (__u64)newsk };
    struct conn_stats_t stats = {};
    init_conn_stats(&stats, newsk, event.pid);
    bpf_map_update_elem(&conn_stats, &key, &stats, BPF_ANY);

    return 0;
}

char LICENSE[] SEC("license") = "GPL";
