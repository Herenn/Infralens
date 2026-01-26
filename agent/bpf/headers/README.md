# BPF Headers

This directory contains headers required for compiling the eBPF programs.

## vmlinux.h

The `vmlinux.h` file contains BTF (BPF Type Format) definitions extracted from 
the Linux kernel. This file enables CO-RE (Compile Once - Run Everywhere) 
support, allowing our eBPF programs to run on different kernel versions.

### Generating vmlinux.h

To generate `vmlinux.h` for your kernel, run:

```bash
bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h
```

**Prerequisites:**
- Linux kernel 5.2+ with BTF enabled (`CONFIG_DEBUG_INFO_BTF=y`)
- `bpftool` installed (usually from `linux-tools-common` package)

### Using a pre-generated vmlinux.h

For CI/CD and development convenience, you can use pre-generated headers from:
- https://github.com/libbpf/libbpf-bootstrap (contains headers for common kernels)

The vmlinux.h we use should be compatible with kernels 5.10+.

## Other Headers

The `<bpf/bpf_helpers.h>` and related headers come from libbpf and are included
via the system include path or the libbpf submodule.
