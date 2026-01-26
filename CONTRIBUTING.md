# Contributing to InfraLens

First off, thank you for considering contributing to InfraLens! 🎉

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [How to Contribute](#how-to-contribute)
- [Pull Request Process](#pull-request-process)
- [Style Guidelines](#style-guidelines)

## Code of Conduct

This project and everyone participating in it is governed by our commitment to providing a welcoming and inclusive environment. Please be respectful and constructive in all interactions.

## Getting Started

### Prerequisites

- **Linux** with kernel 5.8+ (for eBPF support)
- **Go 1.22+**
- **Node.js 18+**
- **clang/LLVM** (for compiling eBPF programs)
- **Docker** (optional, for containerized development)

### Development Setup

1. **Fork and clone the repository**
   ```bash
   git clone https://github.com/YOUR_USERNAME/Infralens.git
   cd infralens
   ```

2. **Install Go dependencies**
   ```bash
   go mod download
   ```

3. **Install eBPF tooling**
   ```bash
   # Ubuntu/Debian
   sudo apt-get install -y clang llvm libbpf-dev
   
   # Install bpf2go
   go install github.com/cilium/ebpf/cmd/bpf2go@v0.12.3
   ```

4. **Generate eBPF bindings**
   ```bash
   cd agent/ebpf
   go generate ./...
   ```

5. **Build components**
   ```bash
   # Agent
   cd agent && go build -o infralens-agent .
   
   # Backend
   cd backend && go build -o backend .
   
   # Frontend
   cd frontend && npm install && npm run dev
   ```

## How to Contribute

### Reporting Bugs

- Use the GitHub issue tracker
- Check if the issue already exists
- Include:
  - Clear description of the problem
  - Steps to reproduce
  - Expected vs actual behavior
  - Environment details (OS, kernel version, Go version)
  - Relevant logs

### Suggesting Features

- Open a GitHub issue with the "enhancement" label
- Describe the use case and expected behavior
- Explain why this would be useful to other users

### Submitting Code

1. **Create a feature branch**
   ```bash
   git checkout -b feature/your-feature-name
   ```

2. **Make your changes**
   - Follow the style guidelines below
   - Add tests if applicable
   - Update documentation if needed

3. **Test your changes**
   ```bash
   # Backend tests
   cd backend && go test ./...
   
   # Frontend tests
   cd frontend && npm test
   ```

4. **Commit with clear messages**
   ```bash
   git commit -m "feat: add support for UDP tracing"
   ```

5. **Push and create a Pull Request**
   ```bash
   git push origin feature/your-feature-name
   ```

## Pull Request Process

1. **Title**: Use conventional commit format
   - `feat:` for new features
   - `fix:` for bug fixes
   - `docs:` for documentation
   - `refactor:` for code refactoring
   - `test:` for adding tests
   - `chore:` for maintenance tasks

2. **Description**: Include
   - What the PR does
   - Why it's needed
   - How to test it
   - Screenshots (for UI changes)

3. **Review Process**
   - All PRs require at least one review
   - CI must pass
   - No merge conflicts

## Style Guidelines

### Go Code

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` and `golint`
- Keep functions small and focused
- Add comments for exported functions
- Use meaningful variable names

```go
// Good
func (g *ServiceGraph) GetService(id string) (*Service, bool) {
    g.mu.RLock()
    defer g.mu.RUnlock()
    svc, ok := g.services[id]
    return svc, ok
}

// Avoid
func (g *ServiceGraph) Get(i string) (*Service, bool) {
    g.mu.RLock()
    defer g.mu.RUnlock()
    s, o := g.services[i]
    return s, o
}
```

### eBPF C Code

- Use CO-RE principles with `vmlinux.h`
- Keep probes minimal (do work in userspace)
- Use `BPF_CORE_READ` for kernel struct access
- Document magic numbers and struct offsets

### TypeScript/React

- Use functional components with hooks
- Follow React best practices
- Use TypeScript strictly (no `any`)
- Keep components small and reusable

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(agent): add UDP tracing support

- Added kprobe for udp_sendmsg
- Added kprobe for udp_recvmsg  
- Updated perf event handling

Closes #123
```

## Project Structure

```
infralens/
├── agent/           # eBPF agent (DaemonSet)
│   ├── bpf/         # eBPF C programs
│   ├── ebpf/        # Go eBPF loader
│   ├── inspector/   # Deep inspection
│   └── metrics/     # Host metrics
├── backend/         # Backend aggregator
│   ├── api/         # HTTP handlers
│   ├── graph/       # Topology graph
│   ├── k8s/         # K8s integration
│   └── pkg/         # Shared packages
├── frontend/        # React dashboard
│   └── src/
│       ├── components/
│       └── hooks/
└── deploy/          # Deployment configs
```

## Questions?

- Open a GitHub Discussion
- Join our community (link TBD)

Thank you for contributing! 🙏
