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

// Protocol constants
#define PROTO_TCP 0
#define PROTO_UDP 1

// Event structure sent to userspace (for new connections)
// UNIFIED 64-byte layout with consistent address storage for IPv4/IPv6
// Layout:
//   offset 0:  direction   (1 byte)
//   offset 1:  _pad1       (3 bytes)
//   offset 4:  pid         (4 bytes)
//   offset 8:  comm        (16 bytes)
//   offset 24: src_addr    (16 bytes) - IPv4 in [0:4], IPv6 in [0:16]
//   offset 40: dst_addr    (16 bytes) - IPv4 in [0:4], IPv6 in [0:16]
//   offset 56: family      (2 bytes)  - AF_INET (2) or AF_INET6 (10)
//   offset 58: src_port    (2 bytes)
//   offset 60: dst_port    (2 bytes)
//   offset 62: protocol    (1 byte)   - PROTO_TCP (0) or PROTO_UDP (1)
//   offset 63: _pad2       (1 byte)
struct event_t {
    __u8  direction;            // 0 = outbound (connect), 1 = inbound (accept)
    __u8  _pad1[3];             // Padding for alignment
    __u32 pid;                  // Process ID
    char  comm[TASK_COMM_LEN];  // Process name (16 bytes)
    __u8  src_addr[16];         // Source address (unified v4/v6)
    __u8  dst_addr[16];         // Destination address (unified v4/v6)
    __u16 family;               // Address family: AF_INET (2) or AF_INET6 (10)
    __u16 src_port;             // Source port
    __u16 dst_port;             // Destination port (network byte order)
    __u8  protocol;             // PROTO_TCP (0) or PROTO_UDP (1)
    __u8  _pad2;                // Padding to reach 64 bytes
};

// Connection key for stats tracking (using sock pointer as unique ID)
struct conn_key_t {
    __u64 sock_ptr;             // Socket pointer (unique identifier)
};

// Connection stats aggregated in kernel (unified address storage)
struct conn_stats_t {
    __u64 bytes_sent;           // Total bytes sent
    __u64 bytes_recv;           // Total bytes received
    __u64 packets_sent;         // Packet count sent
    __u64 packets_recv;         // Packet count received
    __u32 pid;                  // Process ID
    __u16 family;               // Address family (AF_INET or AF_INET6)
    __u16 src_port;             // Source port
    __u16 dst_port;             // Destination port
    __u8  protocol;             // PROTO_TCP (0) or PROTO_UDP (1)
    __u8  _pad;                 // Padding
    __u8  src_addr[16];         // Source address (unified v4/v6)
    __u8  dst_addr[16];         // Destination address (unified v4/v6)
    char  comm[TASK_COMM_LEN];  // Process name
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
    stats->family = BPF_CORE_READ(sk, __sk_common.skc_family);
    stats->last_update = bpf_ktime_get_ns();
    
    bpf_get_current_comm(&stats->comm, sizeof(stats->comm));
    
    if (stats->family == AF_INET) {
        // Store IPv4 in first 4 bytes of unified field
        __u32 saddr = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
        __u32 daddr = BPF_CORE_READ(sk, __sk_common.skc_daddr);
        __builtin_memcpy(stats->src_addr, &saddr, 4);
        __builtin_memcpy(stats->dst_addr, &daddr, 4);
    } else if (stats->family == AF_INET6) {
        // Store full 16-byte IPv6 address
        BPF_CORE_READ_INTO(stats->src_addr, sk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
        BPF_CORE_READ_INTO(stats->dst_addr, sk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
    }
    
    stats->dst_port = BPF_CORE_READ(sk, __sk_common.skc_dport);
    stats->src_port = BPF_CORE_READ(sk, __sk_common.skc_num);
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

    // Send connection event (unified format)
    event.family = AF_INET;
    event.pid = pid_tgid >> 32;
    event.direction = DIRECTION_OUTBOUND;
    bpf_get_current_comm(&event.comm, sizeof(event.comm));
    
    // Store IPv4 in first 4 bytes of unified address fields
    __u32 saddr = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
    __u32 daddr = BPF_CORE_READ(sk, __sk_common.skc_daddr);
    __builtin_memcpy(event.src_addr, &saddr, 4);
    __builtin_memcpy(event.dst_addr, &daddr, 4);
    
    event.src_port = BPF_CORE_READ(sk, __sk_common.skc_num);
    event.dst_port = BPF_CORE_READ(sk, __sk_common.skc_dport);

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

    // Send connection event (unified format)
    event.family = AF_INET6;
    event.pid = pid_tgid >> 32;
    event.direction = DIRECTION_OUTBOUND;
    bpf_get_current_comm(&event.comm, sizeof(event.comm));
    
    // Store full 16-byte IPv6 address
    BPF_CORE_READ_INTO(event.src_addr, sk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
    BPF_CORE_READ_INTO(event.dst_addr, sk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
    
    event.src_port = BPF_CORE_READ(sk, __sk_common.skc_num);
    event.dst_port = BPF_CORE_READ(sk, __sk_common.skc_dport);

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
    event.family = family;
    bpf_get_current_comm(&event.comm, sizeof(event.comm));

    if (family == AF_INET) {
        // For accepted connections, skc_rcv_saddr is our local address (dst from client's view)
        // skc_daddr is the remote peer (src from client's view)
        // We swap them so the event looks like: remote -> local
        __u32 saddr = BPF_CORE_READ(newsk, __sk_common.skc_daddr);       // Remote (peer/client)
        __u32 daddr = BPF_CORE_READ(newsk, __sk_common.skc_rcv_saddr);   // Local (us/server)
        __builtin_memcpy(event.src_addr, &saddr, 4);
        __builtin_memcpy(event.dst_addr, &daddr, 4);
        event.dst_port = __builtin_bswap16(BPF_CORE_READ(newsk, __sk_common.skc_num));
    } else if (family == AF_INET6) {
        // Same swap for IPv6
        BPF_CORE_READ_INTO(event.src_addr, newsk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
        BPF_CORE_READ_INTO(event.dst_addr, newsk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
        event.dst_port = __builtin_bswap16(BPF_CORE_READ(newsk, __sk_common.skc_num));
    } else {
        return 0;  // Unsupported address family
    }

    bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, &event, sizeof(event));

    // Initialize stats tracking for this accepted connection
    struct conn_key_t key = { .sock_ptr = (__u64)newsk };
    struct conn_stats_t stats = {};
    init_conn_stats(&stats, newsk, event.pid);
    bpf_map_update_elem(&conn_stats, &key, &stats, BPF_ANY);

    return 0;
}

// ============================================================================
// UDP tracing
// int udp_sendmsg(struct sock *sk, struct msghdr *msg, size_t len)
// int udpv6_sendmsg(struct sock *sk, struct msghdr *msg, size_t len)
//
// Unlike TCP there is no connect/accept handshake to hook, so flows are
// discovered on first send: a connection event is emitted when a socket is
// seen for the first time, and per-flow stats accumulate in conn_stats.
// For unconnected sockets (plain sendto), the destination is read from
// msg->msg_name, which the syscall layer has already copied into kernel
// memory. This surfaces DNS, StatsD, syslog, and similar UDP traffic.
// ============================================================================

SEC("kprobe/udp_sendmsg")
int BPF_KPROBE(kprobe_udp_sendmsg, struct sock *sk, struct msghdr *msg, size_t len)
{
    if (!sk || len == 0) return 0;

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u32 pid = pid_tgid >> 32;

    // Connected sockets carry the destination on the socket itself
    __u32 daddr = BPF_CORE_READ(sk, __sk_common.skc_daddr);
    __u16 dport = BPF_CORE_READ(sk, __sk_common.skc_dport);

    // Unconnected sockets (sendto): destination is in msg_name
    if (daddr == 0 || dport == 0) {
        struct sockaddr_in *usin = (struct sockaddr_in *)BPF_CORE_READ(msg, msg_name);
        if (usin) {
            daddr = BPF_CORE_READ(usin, sin_addr.s_addr);
            dport = BPF_CORE_READ(usin, sin_port);
        }
    }
    if (daddr == 0 || dport == 0) return 0;

    __u32 saddr = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
    __u16 sport = BPF_CORE_READ(sk, __sk_common.skc_num);

    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    struct conn_stats_t *stats = bpf_map_lookup_elem(&conn_stats, &key);
    if (stats) {
        __sync_fetch_and_add(&stats->bytes_sent, len);
        __sync_fetch_and_add(&stats->packets_sent, 1);
        stats->last_update = bpf_ktime_get_ns();
        // Track the latest destination for unconnected sockets
        __builtin_memcpy(stats->dst_addr, &daddr, 4);
        stats->dst_port = dport;
        return 0;
    }

    // First sighting of this socket: create stats + emit a flow event
    struct conn_stats_t new_stats = {};
    new_stats.pid = pid;
    new_stats.family = AF_INET;
    new_stats.protocol = PROTO_UDP;
    new_stats.last_update = bpf_ktime_get_ns();
    bpf_get_current_comm(&new_stats.comm, sizeof(new_stats.comm));
    __builtin_memcpy(new_stats.src_addr, &saddr, 4);
    __builtin_memcpy(new_stats.dst_addr, &daddr, 4);
    new_stats.src_port = sport;
    new_stats.dst_port = dport;
    new_stats.bytes_sent = len;
    new_stats.packets_sent = 1;
    bpf_map_update_elem(&conn_stats, &key, &new_stats, BPF_NOEXIST);

    struct event_t event = {};
    event.family = AF_INET;
    event.protocol = PROTO_UDP;
    event.pid = pid;
    event.direction = DIRECTION_OUTBOUND;
    bpf_get_current_comm(&event.comm, sizeof(event.comm));
    __builtin_memcpy(event.src_addr, &saddr, 4);
    __builtin_memcpy(event.dst_addr, &daddr, 4);
    event.src_port = sport;
    event.dst_port = dport;
    bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, &event, sizeof(event));

    return 0;
}

SEC("kprobe/udpv6_sendmsg")
int BPF_KPROBE(kprobe_udpv6_sendmsg, struct sock *sk, struct msghdr *msg, size_t len)
{
    if (!sk || len == 0) return 0;

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u32 pid = pid_tgid >> 32;

    __u8 daddr[16] = {};
    BPF_CORE_READ_INTO(&daddr, sk, __sk_common.skc_v6_daddr.in6_u.u6_addr8);
    __u16 dport = BPF_CORE_READ(sk, __sk_common.skc_dport);

    // Check whether the socket destination is unset (all zero)
    __u64 d0, d1;
    __builtin_memcpy(&d0, daddr, 8);
    __builtin_memcpy(&d1, daddr + 8, 8);

    if ((d0 == 0 && d1 == 0) || dport == 0) {
        struct sockaddr_in6 *usin = (struct sockaddr_in6 *)BPF_CORE_READ(msg, msg_name);
        if (usin) {
            BPF_CORE_READ_INTO(&daddr, usin, sin6_addr.in6_u.u6_addr8);
            dport = BPF_CORE_READ(usin, sin6_port);
        }
        __builtin_memcpy(&d0, daddr, 8);
        __builtin_memcpy(&d1, daddr + 8, 8);
    }
    if ((d0 == 0 && d1 == 0) || dport == 0) return 0;

    __u8 saddr[16] = {};
    BPF_CORE_READ_INTO(&saddr, sk, __sk_common.skc_v6_rcv_saddr.in6_u.u6_addr8);
    __u16 sport = BPF_CORE_READ(sk, __sk_common.skc_num);

    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    struct conn_stats_t *stats = bpf_map_lookup_elem(&conn_stats, &key);
    if (stats) {
        __sync_fetch_and_add(&stats->bytes_sent, len);
        __sync_fetch_and_add(&stats->packets_sent, 1);
        stats->last_update = bpf_ktime_get_ns();
        __builtin_memcpy(stats->dst_addr, daddr, 16);
        stats->dst_port = dport;
        return 0;
    }

    struct conn_stats_t new_stats = {};
    new_stats.pid = pid;
    new_stats.family = AF_INET6;
    new_stats.protocol = PROTO_UDP;
    new_stats.last_update = bpf_ktime_get_ns();
    bpf_get_current_comm(&new_stats.comm, sizeof(new_stats.comm));
    __builtin_memcpy(new_stats.src_addr, saddr, 16);
    __builtin_memcpy(new_stats.dst_addr, daddr, 16);
    new_stats.src_port = sport;
    new_stats.dst_port = dport;
    new_stats.bytes_sent = len;
    new_stats.packets_sent = 1;
    bpf_map_update_elem(&conn_stats, &key, &new_stats, BPF_NOEXIST);

    struct event_t event = {};
    event.family = AF_INET6;
    event.protocol = PROTO_UDP;
    event.pid = pid;
    event.direction = DIRECTION_OUTBOUND;
    bpf_get_current_comm(&event.comm, sizeof(event.comm));
    __builtin_memcpy(event.src_addr, saddr, 16);
    __builtin_memcpy(event.dst_addr, daddr, 16);
    event.src_port = sport;
    event.dst_port = dport;
    bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, &event, sizeof(event));

    return 0;
}

// ============================================================================
// udp_recvmsg: Track bytes received on UDP sockets
// Reuses recvmsg_sock_store (keyed by pid_tgid) like tcp_recvmsg.
// ============================================================================

SEC("kprobe/udp_recvmsg")
int kprobe_udp_recvmsg(struct pt_regs *ctx)
{
    struct sock *sk = (struct sock *)PT_REGS_PARM1(ctx);
    if (!sk) return 0;

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    bpf_map_update_elem(&recvmsg_sock_store, &pid_tgid, &sk, BPF_ANY);
    return 0;
}

static __always_inline int udp_recvmsg_ret(struct pt_regs *ctx)
{
    int ret = PT_REGS_RC(ctx);

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    struct sock **skp = bpf_map_lookup_elem(&recvmsg_sock_store, &pid_tgid);
    if (!skp) return 0;

    struct sock *sk = *skp;
    bpf_map_delete_elem(&recvmsg_sock_store, &pid_tgid);

    if (!sk || ret <= 0) return 0;

    struct conn_key_t key = { .sock_ptr = (__u64)sk };
    struct conn_stats_t *stats = bpf_map_lookup_elem(&conn_stats, &key);
    if (stats) {
        __sync_fetch_and_add(&stats->bytes_recv, ret);
        __sync_fetch_and_add(&stats->packets_recv, 1);
        stats->last_update = bpf_ktime_get_ns();
    }

    return 0;
}

SEC("kretprobe/udp_recvmsg")
int kretprobe_udp_recvmsg(struct pt_regs *ctx)
{
    return udp_recvmsg_ret(ctx);
}

SEC("kprobe/udpv6_recvmsg")
int kprobe_udpv6_recvmsg(struct pt_regs *ctx)
{
    struct sock *sk = (struct sock *)PT_REGS_PARM1(ctx);
    if (!sk) return 0;

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    bpf_map_update_elem(&recvmsg_sock_store, &pid_tgid, &sk, BPF_ANY);
    return 0;
}

SEC("kretprobe/udpv6_recvmsg")
int kretprobe_udpv6_recvmsg(struct pt_regs *ctx)
{
    return udp_recvmsg_ret(ctx);
}

char LICENSE[] SEC("license") = "GPL";
