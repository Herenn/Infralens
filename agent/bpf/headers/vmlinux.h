/* SPDX-License-Identifier: (LGPL-2.1 OR BSD-2-Clause) */
/*
 * Minimal vmlinux.h for InfraLens eBPF programs
 * Contains kernel types needed for TCP tracing with CO-RE support
 */

#ifndef __VMLINUX_H__
#define __VMLINUX_H__

/* Prevent including other kernel headers */
#define _LINUX_TYPES_H
#define _LINUX_SOCKET_H

/* Enable preserve_access_index for CO-RE */
#pragma clang attribute push(__attribute__((preserve_access_index)), apply_to = record)

/*
 * Basic integer types
 */
typedef signed char __s8;
typedef unsigned char __u8;
typedef short __s16;
typedef unsigned short __u16;
typedef int __s32;
typedef unsigned int __u32;
typedef long long __s64;
typedef unsigned long long __u64;

typedef __s8 s8;
typedef __u8 u8;
typedef __s16 s16;
typedef __u16 u16;
typedef __s32 s32;
typedef __u32 u32;
typedef __s64 s64;
typedef __u64 u64;

/* Network byte order types */
typedef __u16 __be16;
typedef __u32 __be32;
typedef __u64 __be64;
typedef __u16 __le16;
typedef __u32 __le32;
typedef __u64 __le64;

typedef __u16 __sum16;
typedef __u32 __wsum;

/* Boolean type */
typedef _Bool bool;
#define true 1
#define false 0

/* Size types */
typedef unsigned long size_t;
typedef long ssize_t;

/*
 * BPF map types
 */
enum bpf_map_type {
    BPF_MAP_TYPE_UNSPEC = 0,
    BPF_MAP_TYPE_HASH = 1,
    BPF_MAP_TYPE_ARRAY = 2,
    BPF_MAP_TYPE_PROG_ARRAY = 3,
    BPF_MAP_TYPE_PERF_EVENT_ARRAY = 4,
    BPF_MAP_TYPE_PERCPU_HASH = 5,
    BPF_MAP_TYPE_PERCPU_ARRAY = 6,
    BPF_MAP_TYPE_STACK_TRACE = 7,
    BPF_MAP_TYPE_CGROUP_ARRAY = 8,
    BPF_MAP_TYPE_LRU_HASH = 9,
    BPF_MAP_TYPE_LRU_PERCPU_HASH = 10,
    BPF_MAP_TYPE_LPM_TRIE = 11,
    BPF_MAP_TYPE_ARRAY_OF_MAPS = 12,
    BPF_MAP_TYPE_HASH_OF_MAPS = 13,
    BPF_MAP_TYPE_DEVMAP = 14,
    BPF_MAP_TYPE_SOCKMAP = 15,
    BPF_MAP_TYPE_CPUMAP = 16,
    BPF_MAP_TYPE_XSKMAP = 17,
    BPF_MAP_TYPE_SOCKHASH = 18,
    BPF_MAP_TYPE_CGROUP_STORAGE = 19,
    BPF_MAP_TYPE_REUSEPORT_SOCKARRAY = 20,
    BPF_MAP_TYPE_PERCPU_CGROUP_STORAGE = 21,
    BPF_MAP_TYPE_QUEUE = 22,
    BPF_MAP_TYPE_STACK = 23,
    BPF_MAP_TYPE_SK_STORAGE = 24,
    BPF_MAP_TYPE_DEVMAP_HASH = 25,
    BPF_MAP_TYPE_STRUCT_OPS = 26,
    BPF_MAP_TYPE_RINGBUF = 27,
    BPF_MAP_TYPE_INODE_STORAGE = 28,
    BPF_MAP_TYPE_TASK_STORAGE = 29,
};

/* BPF map update flags */
#define BPF_ANY     0
#define BPF_NOEXIST 1
#define BPF_EXIST   2
#define BPF_F_LOCK  4

/* IPv6 address structure */
struct in6_addr {
    union {
        __u8 u6_addr8[16];
        __be16 u6_addr16[8];
        __be32 u6_addr32[4];
    } in6_u;
};

/* IPv4 address structure */
struct in_addr {
    __be32 s_addr;
};

/* Socket address structures (used to read UDP sendto destinations) */
struct sockaddr_in {
    __u16 sin_family;
    __be16 sin_port;
    struct in_addr sin_addr;
};

struct sockaddr_in6 {
    __u16 sin6_family;
    __be16 sin6_port;
    __be32 sin6_flowinfo;
    struct in6_addr sin6_addr;
    __u32 sin6_scope_id;
};

/*
 * Message header (minimal) - only msg_name is accessed.
 * CO-RE relocates the field offset against the running kernel's BTF.
 */
struct msghdr {
    void *msg_name;
};

/*
 * Socket common structure - key for TCP tracing
 */
struct sock_common {
    union {
        struct {
            __be32 skc_daddr;      /* Foreign IPv4 address */
            __be32 skc_rcv_saddr;  /* Bound local IPv4 address */
        };
    };
    union {
        struct {
            __be16 skc_dport;      /* Destination port */
            __u16 skc_num;         /* Local port */
        };
    };
    unsigned short skc_family;     /* Address family (AF_INET, AF_INET6) */
    volatile unsigned char skc_state; /* Connection state */
    int skc_bound_dev_if;          /* Bound device index */
    struct in6_addr skc_v6_daddr;  /* IPv6 destination address */
    struct in6_addr skc_v6_rcv_saddr; /* IPv6 source address */
};

/*
 * Socket structure
 */
struct sock {
    struct sock_common __sk_common;
};

/*
 * Task structure (minimal)
 */
#define TASK_COMM_LEN 16

struct task_struct {
    int pid;
    int tgid;
    char comm[TASK_COMM_LEN];
};

/*
 * Architecture-specific register definitions for kprobe/kretprobe
 * bpf_tracing.h will use these via PT_REGS_* macros
 */

/* x86_64 */
#if defined(__TARGET_ARCH_x86) || defined(__x86_64__) || defined(__i386__)

struct pt_regs {
    unsigned long r15;
    unsigned long r14;
    unsigned long r13;
    unsigned long r12;
    unsigned long bp;
    unsigned long bx;
    unsigned long r11;
    unsigned long r10;
    unsigned long r9;
    unsigned long r8;
    unsigned long ax;
    unsigned long cx;
    unsigned long dx;
    unsigned long si;
    unsigned long di;
    unsigned long orig_ax;
    unsigned long ip;
    unsigned long cs;
    unsigned long flags;
    unsigned long sp;
    unsigned long ss;
};

/* ARM64 / AArch64 */
#elif defined(__TARGET_ARCH_arm64) || defined(__aarch64__)

struct user_pt_regs {
    __u64 regs[31];
    __u64 sp;
    __u64 pc;
    __u64 pstate;
};

struct pt_regs {
    union {
        struct user_pt_regs user_regs;
        struct {
            __u64 regs[31];
            __u64 sp;
            __u64 pc;
            __u64 pstate;
        };
    };
    __u64 orig_x0;
    __s32 syscallno;
    __u32 unused2;
    __u64 sdei_ttbr1;
    __u64 pmr_save;
    __u64 stackframe[2];
    __u64 lockdep_hardirqs;
    __u64 exit_rcu;
};

/* ARM 32-bit */
#elif defined(__TARGET_ARCH_arm) || defined(__arm__)

struct pt_regs {
    long uregs[18];
};

/* Default fallback - define minimal struct */
#else

struct pt_regs {
    unsigned long regs[32];
};

#endif

/*
 * Tracepoint structure for inet_sock_set_state
 */
struct trace_event_raw_inet_sock_set_state {
    unsigned short common_type;
    unsigned char common_flags;
    unsigned char common_preempt_count;
    int common_pid;
    
    const void *skaddr;
    int oldstate;
    int newstate;
    __u16 sport;
    __u16 dport;
    __u16 family;
    __u16 protocol;
    __u8 saddr[4];
    __u8 daddr[4];
    __u8 saddr_v6[16];
    __u8 daddr_v6[16];
};

#pragma clang attribute pop

#endif /* __VMLINUX_H__ */
