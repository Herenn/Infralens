# InfraLens 🔍

[![Go Version](https://img.shields.io/github/go-mod/go-version/Herenn/Infralens)](https://go.dev/)
[![License](https://img.shields.io/github/license/Herenn/Infralens)](LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/Herenn/Infralens?style=social)](https://github.com/Herenn/Infralens)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
[![Release](https://img.shields.io/badge/release-v0.2.3-blue)](https://github.com/Herenn/Infralens/releases)

**Zero-Instrumentation Observability for Kubernetes**

InfraLens is a next-generation observability tool that uses eBPF to automatically discover and visualize service-to-service communication in Kubernetes clusters—without requiring any code changes or sidecars.

![InfraLens Demo](docs/demo.gif)

## 🎯 Features

- **Zero Instrumentation**: No sidecars, no code changes, no SDK integration required
- **Real-time Topology**: Live visualization of service dependencies using React Flow
- **eBPF-Powered**: Efficient kernel-level tracing with minimal overhead using `cilium/ebpf`
- **IPv4 + IPv6**: Full support for both IPv4 (`tcp_v4_connect`) and IPv6 (`tcp_v6_connect`)
- **Ingress Visibility**: Detect external incoming connections via `inet_csk_accept` tracing
- **Network Throughput**: Real-time bytes/packets sent/received with rate calculations
- **Service Fingerprinting**: Automatic technology detection (PostgreSQL, Redis, Nginx, etc.) based on ports and process names
- **Host Resource Monitoring**: Live CPU & RAM usage per server with color-coded status bars
- **Visual Grouping**: Services grouped by physical/virtual server with infrastructure-style layout
- **Kubernetes Native**: Deploys as a DaemonSet with full RBAC support
- **K8s Service Discovery**: Automatic IP → Pod/Service name resolution using `client-go` informers
- **Multi-Node Support**: Agents on multiple servers report to a central backend
- **CO-RE Compatible**: Compile Once – Run Everywhere across kernel versions 5.8+
- **Deep Inspection**: Protocol probing for HTTP, PostgreSQL, MySQL, Redis, MongoDB
- **Dependency Discovery**: Auto-detect package.json, go.mod, requirements.txt
- **Smart Code Analysis**: Automatic source code discovery with line number references
- **AI Documentation**: Multi-provider AI support with intelligent service documentation
- **Auto-Update**: Agents automatically check for updates and self-update
- **Cached AI Docs**: AI documentation persists in browser - no regeneration needed

### v0.2.0 New Features

- **Persistent Storage**: SQLite database for data persistence across restarts
- **Auto-Pruning**: Automatic cleanup of stale services and connections
- **API Key Authentication**: Secure agent-to-backend communication
- **Configurable CORS**: Environment-based CORS origin configuration
- **Event Bus**: Real-time event system for WebSocket optimization
- **Modular Architecture**: Clean separation of concerns with service layer

## 🤖 AI-Powered Documentation

InfraLens includes a powerful AI documentation system that generates comprehensive service documentation by analyzing:

- **Source Code**: Reads README, Dockerfile, main entry files, and package manifests
- **Network Topology**: Understands service connections and dependencies
- **Deep Inspection**: Protocol-level service detection
- **Runtime Metrics**: CPU, memory, and throughput data

### Generated Documentation Includes

| Section | Description |
|---------|-------------|
| 🎯 **What This Service Does** | Purpose and functionality explanation |
| 🛠️ **Technical Stack** | Languages, frameworks, and dependencies |
| 🏗️ **Architecture & Data Flow** | Service role and communication patterns |
| 📂 **Code Analysis** | Key files and functions with line numbers |
| 🌐 **Network Behavior** | Ports, protocols, and connections |
| 🛡️ **Security Considerations** | Vulnerabilities and recommendations |
| ⚡ **Performance & Reliability** | Resource usage and scaling insights |
| 📋 **Recommendations** | Actionable improvement suggestions |

### Supported AI Providers

| Provider | Type | Default Model | Configuration |
|----------|------|---------------|---------------|
| **OpenAI** | Cloud | GPT-3.5-turbo | API Key |
| **Anthropic** | Cloud | Claude 3 Haiku | API Key |
| **Google Gemini** | Cloud | Gemini Pro | API Key |
| **Ollama** | Local | Llama2 | Server URL |
| **LM Studio** | Local | Any compatible | Server URL |

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                          │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                      User Namespace                          │   │
│  │   ┌─────────┐     ┌─────────┐     ┌─────────┐                │   │
│  │   │ Service │────▶│ Service │────▶│ Service │                │   │
│  │   │    A    │     │    B    │     │    C    │                │   │
│  │   └─────────┘     └─────────┘     └─────────┘                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                              │                                      │
│        eBPF Tracing (connect + accept + sendmsg + recvmsg)          │
│                              │                                      │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                    InfraLens Namespace                       │   │
│  │                                                              │   │
│  │  ┌─────────────────────────────────────────────────────┐     │   │
│  │  │              Agent (DaemonSet)                      │     │   │
│  │  │  ┌─────────┐  ┌─────────┐  ┌─────────┐              │     │   │
│  │  │  │ Node 1  │  │ Node 2  │  │ Node 3  │              │     │   │
│  │  │  │  Agent  │  │  Agent  │  │  Agent  │              │     │   │
│  │  │  └────┬────┘  └────┬────┘  └────┬────┘              │     │   │
│  │  └───────┼────────────┼────────────┼──────────────────-┘     │   │
│  │          │            │            │                         │   │
│  │          └────────────┼────────────┘                         │   │
│  │                       ▼                                      │   │
│  │              ┌──────────────┐                                │   │
│  │              │   Backend    │◀──── WebSocket ────┐           │   │
│  │              │ (Aggregator) │         │          │           │   │
│  │              │   + AI LLM   │         │          │           │   │
│  │              └──────────────┘         │          │           │   │
│  │                                ┌──────┴───────┐  │           │   │
│  │                                │   Frontend   │──┘           │   │
│  │                                │ (React Flow) │              │   │
│  │                                └──────────────┘              │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

### Components

| Component | Description | Technology |
|-----------|-------------|------------|
| **Agent** | DaemonSet that runs on every node, traces TCP syscalls (IPv4 + IPv6), collects throughput metrics, host resources, and source code context | Go, cilium/ebpf, bpf2go, gopsutil |
| **Backend** | Central aggregator with SQLite persistence, service topology graph, IP resolution, fingerprinting, and AI documentation | Go, gorilla/mux, WebSocket, client-go, SQLite, multi-LLM |
| **Frontend** | React dashboard with grouped server nodes, CPU/RAM bars, throughput labels, port badges, and AI documentation viewer | React, React Flow, TailwindCSS |

### Backend Architecture (v0.2.0)

```
┌─────────────────────────────────────────────────────────────────┐
│                         Backend Server                          │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                      API Layer                             │ │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐       │ │
│  │  │  Event   │ │ Topology │ │ WebSocket│ │    AI    │       │ │
│  │  │ Handler  │ │ Handler  │ │ Handler  │ │ Handler  │       │ │
│  │  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘       │ │
│  └───────┼────────────┼────────────┼────────────┼─────────────┘ │
│          │            │            │            │               │
│  ┌───────┴────────────┴────────────┴────────────┴─────────────┐ │
│  │                     Middleware                             │ │
│  │  [ Recovery ] [ Logging ] [ CORS ] [ API Key Auth ]        │ │
│  └────────────────────────────────────────────────────────────┘ │
│                              │                                  │
│  ┌───────────────────────────┴────────────────────────────────┐ │
│  │                    Service Layer                           │ │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐            │ │
│  │  │  Topology  │  │   Event    │  │  EventBus  │◀─┐         │ │
│  │  │  Service   │  │ Processor  │  │ (Pub/Sub)  │  │ WebSocket│
│  │  └────┬───────┘  └────┬───────┘  └────────────┘  │         │ │
│  └───────┼───────────────┼──────────────────────────┼─────────┘ │
│          │               │                          │           │
│  ┌───────┴───────────────┴──────────────────────────┴─────────┐ │
│  │                   Storage Layer                            │ │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐                  │ │
│  │  │ Service  │  │Connection│  │ Metrics  │                  │ │
│  │  │  Repo    │  │   Repo   │  │   Repo   │                  │ │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘                  │ │
│  │       └─────────────┼─────────────┘                        │ │
│  │                     ▼                                      │ │
│  │              ┌────────────┐                                │ │
│  │              │   SQLite   │  (Auto-migrate, Auto-prune)    │ │
│  │              │ infralens.db│                               │ │
│  │              └────────────┘                                │ │
│  └────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

## 📁 Project Structure

```
infralens/
├── agent/                    # eBPF Agent (DaemonSet)
│   ├── main.go              # Agent entry point with perf event loop
│   ├── bpf/                 # eBPF C programs
│   │   ├── traffic.c        # kprobes for connect/accept/sendmsg/recvmsg (CO-RE)
│   │   └── headers/         # vmlinux.h + libbpf headers
│   │       ├── vmlinux.h
│   │       └── bpf/
│   ├── ebpf/                # Go eBPF loader
│   │   ├── gen.go           # go:generate directive for bpf2go
│   │   ├── loader.go        # LoadObjects() wrapper
│   │   └── bpf_bpfel_*.go   # Generated code (do not edit)
│   ├── inspector/           # Deep inspection module
│   │   ├── inspector.go     # Process & protocol inspection
│   │   └── codereader.go    # Smart file discovery for AI context
│   ├── metrics/             # Host resource monitoring
│   │   └── collector.go     # CPU/RAM collection using gopsutil
│   └── reporter/            # Backend communication
│       └── reporter.go      # Event & metrics reporting
│
├── backend/                  # Backend Aggregator (v0.2.0 Architecture)
│   ├── main.go              # HTTP server entry point
│   ├── config/              # Configuration management
│   │   └── config.go        # Environment-based config loader
│   ├── api/                 # API Layer
│   │   ├── router.go        # Route registration & server setup
│   │   ├── handlers/        # Request handlers (split by domain)
│   │   │   ├── event_handler.go    # Agent ingestion (/events, /stats, /metrics)
│   │   │   ├── topology_handler.go # Graph queries (/topology, /services)
│   │   │   ├── websocket_handler.go # Real-time updates (/ws)
│   │   │   ├── ai_handler.go       # AI documentation (/ai/*)
│   │   │   └── health_handler.go   # Health checks (/health, /ready)
│   │   └── middleware/      # HTTP middleware
│   │       ├── auth.go      # API key authentication
│   │       ├── cors.go      # Configurable CORS
│   │       └── logging.go   # Request logging & panic recovery
│   ├── service/             # Business Logic Layer
│   │   ├── topology.go      # Graph operations & coordination
│   │   ├── processor.go     # Event processing logic
│   │   └── eventbus.go      # Pub/sub for real-time updates
│   ├── storage/             # Persistence Layer
│   │   ├── repository.go    # Repository interfaces
│   │   └── sqlite/          # SQLite implementation
│   │       └── sqlite.go    # CRUD operations, migrations, pruning
│   ├── graph/               # Legacy (kept for compatibility)
│   │   └── graph.go         # In-memory graph (deprecated)
│   ├── k8s/
│   │   └── watcher.go       # K8s Pod/Service informers for IP resolution
│   └── pkg/
│       ├── fingerprint/     # Service type detection
│       │   └── fingerprint.go  # Port/process → technology mapping
│       └── llm/             # AI documentation system
│           ├── provider.go  # Provider interface & manager
│           ├── openai.go    # OpenAI integration
│           ├── anthropic.go # Anthropic Claude integration
│           ├── gemini.go    # Google Gemini integration
│           ├── ollama.go    # Ollama local LLM integration
│           └── docs.go      # Documentation generation logic
│
├── frontend/                 # React Dashboard
│   ├── src/
│   │   ├── App.tsx          # React Flow canvas with node grouping
│   │   ├── components/
│   │   │   ├── ServiceNode.tsx      # Service visualization with port badges
│   │   │   ├── ServerNode.tsx       # Server group with CPU/RAM bars
│   │   │   ├── ConnectionEdge.tsx   # Connection with throughput labels
│   │   │   ├── ServiceDrawer.tsx    # Detail panel with AI docs tab
│   │   │   ├── MarkdownRenderer.tsx # Styled markdown for AI output
│   │   │   ├── Header.tsx           # Top navigation
│   │   │   └── Sidebar.tsx          # Service details panel
│   │   ├── hooks/
│   │   │   └── useWebSocket.ts      # Real-time topology updates
│   │   ├── utils/
│   │   │   └── layout.ts            # Hierarchical graph layout
│   │   └── types.ts                 # TypeScript interfaces
│   ├── package.json
│   ├── Dockerfile           # Multi-stage build with nginx
│   ├── nginx.conf           # Frontend proxy configuration
│   └── vite.config.ts
│
├── deploy/                   # Deployment configs
│   ├── helm/                # Helm chart (recommended)
│   │   └── infralens/
│   │       ├── Chart.yaml
│   │       ├── values.yaml
│   │       └── templates/
│   │           ├── agent-daemonset.yaml
│   │           ├── backend-deployment.yaml
│   │           ├── frontend-deployment.yaml
│   │           ├── rbac.yaml
│   │           ├── secrets.yaml
│   │           └── ingress.yaml
│   ├── docker-compose/      # Docker Compose (development/single-node)
│   │   ├── docker-compose.yml
│   │   └── env.example
│   ├── k8s/                 # Raw Kubernetes manifests
│   │   ├── namespace.yaml
│   │   ├── agent-daemonset.yaml
│   │   ├── backend-deployment.yaml
│   │   ├── frontend-deployment.yaml
│   │   └── kustomization.yaml
│   └── README.md            # Deployment documentation
│
├── docker/                   # Dockerfiles
│   ├── Dockerfile.agent     # Multi-stage with clang/LLVM for BPF
│   ├── Dockerfile.backend
│   └── Dockerfile.frontend
│
├── Dockerfile.agent         # Root-level agent Dockerfile
├── Dockerfile.backend       # Root-level backend Dockerfile
├── go.mod                   # Go 1.24+ module
├── Makefile                 # Build automation
└── README.md
```

## 🔌 eBPF Probes

InfraLens uses the following kernel probes to capture network activity:

| Probe | Hook Point | Purpose | Direction |
|-------|------------|---------|-----------|
| `kprobe/tcp_v4_connect` | Entry | Store socket for IPv4 outbound | Outbound |
| `kretprobe/tcp_v4_connect` | Return | Capture IPv4 connection details | Outbound |
| `kprobe/tcp_v6_connect` | Entry | Store socket for IPv6 outbound | Outbound |
| `kretprobe/tcp_v6_connect` | Return | Capture IPv6 connection details | Outbound |
| `kretprobe/inet_csk_accept` | Return | Capture accepted (incoming) connections | **Inbound** |
| `kprobe/tcp_sendmsg` | Entry | Track bytes sent | Throughput |
| `kprobe/tcp_recvmsg` | Entry | Store socket for recv tracking | Throughput |
| `kretprobe/tcp_recvmsg` | Return | Track bytes received | Throughput |
| `kprobe/tcp_close` | Entry | Update connection timestamps | Cleanup |

### Event Direction

Events include a `direction` field to distinguish traffic flow:
- `0` = **Outbound** (connect): Local process initiated connection to remote
- `1` = **Inbound** (accept): Remote client connected to local server

This enables accurate visualization of external traffic hitting your services.

## 🔬 Deep Inspection

InfraLens performs protocol-aware deep inspection to understand services beyond just network connections:

### What It Detects

| Service Type | Detection Method | Data Collected |
|--------------|------------------|----------------|
| **HTTP Services** | Probe `/`, `/health`, `/metrics` | Server header, endpoints, health status |
| **PostgreSQL** | SSL request handshake | Version, connection status |
| **MySQL** | Protocol greeting packet | Version string |
| **Redis** | PING command | Connection status |
| **MongoDB** | Wire protocol | Connection status |
| **Node.js** | `package.json` parsing | Dependencies, frameworks |
| **Python** | `requirements.txt` parsing | Dependencies |
| **Go** | `go.mod` parsing | Module dependencies |

### Smart Code Analysis

The agent automatically discovers and reads project files for AI context:

| File Type | Purpose | Line Numbers |
|-----------|---------|--------------|
| `README.md` | Project documentation | ✅ |
| `Dockerfile` | Container configuration | ✅ |
| `package.json` / `go.mod` / `requirements.txt` | Dependencies | ✅ |
| `main.go` / `app.py` / `index.js` | Entry points | ✅ |

**Example AI output with line references:**
> The `main()` function (Lines 45-120) initializes the HTTP server and sets up routing...

### Security-First Approach

- **Read-only access** - No modifications to filesystem
- **Environment variable names only** - Values are never collected
- **Config file names only** - Contents are never read (except for AI context)
- **No secrets exposure** - Designed for production safety
- **Limited file sizes** - Max 50KB per file, 200 lines max

### Agent Flags

```bash
--inspect           Enable deep inspection (default: true)
--inspect-cooldown  Time between re-inspecting same PID (default: 30s)
--auto-update       Enable automatic self-updates (default: true)
--update-interval   Interval to check for updates (default: 1h)
--version           Show agent version and exit
```

### Auto-Update

Agents automatically check for updates from the backend server and self-update when a new version is available:

1. Agent checks backend `/api/v1/version` endpoint periodically (default: every hour)
2. If a newer version is detected, the agent downloads and installs it
3. The agent restarts itself via systemctl

To disable auto-update:
```bash
sudo ./infralens-agent --backend=localhost:8080 --auto-update=false
```

---

> 💡 **Love InfraLens?** Give us a ⭐ on [GitHub](https://github.com/Herenn/Infralens)! It helps the project grow and motivates us to add more features.

---

## 🚀 Deployment

InfraLens supports multiple deployment methods. Choose based on your environment:

| Method | Best For | Complexity |
|--------|----------|------------|
| **Native Scripts** | Linux Servers (Recommended) | ⭐ Easy |
| **Helm** | Production Kubernetes | ⭐⭐ Medium |
| **Docker Compose** | Development / Single-node | ⭐ Easy |
| **Kustomize** | GitOps / Custom K8s | ⭐⭐⭐ Advanced |

### 🖥️ Option 1: Native Installation (Recommended for Servers)

The easiest way to install InfraLens on Linux servers. One-liner installation with automatic setup.

#### Full Stack (Main Server)

Install the complete InfraLens stack (Agent + Backend + Frontend) on your main monitoring server:

```bash
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-full.sh | sudo bash
```

This will:
- Install Go 1.24 and Node.js 20
- Build Agent, Backend, and Frontend from source
- Configure Nginx as reverse proxy (port 3001)
- Create and start systemd services
- Enable auto-start on boot

**After installation:**
```bash
# View the dashboard
http://YOUR_SERVER_IP:3001

# Configure AI (optional)
sudo vim /etc/systemd/system/infralens-backend.service
# Add your API keys to Environment variables
sudo systemctl daemon-reload && sudo systemctl restart infralens-backend

# View logs
journalctl -u infralens-backend -f
journalctl -u infralens-agent -f
```

#### Agent Only (Additional Servers)

Install just the Agent on additional servers you want to monitor:

```bash
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-agent.sh | sudo bash -s -- --backend=YOUR_BACKEND_IP:8080
```

**Options:**
```bash
--backend=IP:PORT   # Backend server address (required)
--node=NAME         # Custom node name (default: hostname)
```

**Example:**
```bash
# Install agent pointing to main server at 192.168.1.100
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-agent.sh | \
  sudo bash -s -- --backend=192.168.1.100:8080 --node=web-server-1
```

#### Uninstall

```bash
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/uninstall.sh | sudo bash
```

### 🎯 Option 2: Helm Chart (Recommended for Kubernetes)

```bash
# Add the InfraLens Helm repository (if published)
# helm repo add infralens https://charts.infralens.io

# Or install from local chart
cd deploy/helm

# Install with default values
helm install infralens ./infralens -n infralens --create-namespace

# Install with custom values
helm install infralens ./infralens -n infralens --create-namespace \
  --set ai.openai.apiKey="sk-..." \
  --set ai.defaultProvider="openai" \
  --set ingress.enabled=true \
  --set ingress.host="infralens.example.com"

# Upgrade existing installation
helm upgrade infralens ./infralens -n infralens

# Uninstall
helm uninstall infralens -n infralens
```

**Configure `values.yaml`:**

```yaml
# AI Configuration
ai:
  defaultProvider: "openai"
  openai:
    apiKey: "sk-..."
  anthropic:
    apiKey: "sk-ant-..."

# Enable Ingress
ingress:
  enabled: true
  host: infralens.example.com
  tls:
    enabled: true

# Resource limits
backend:
  resources:
    limits:
      memory: 512Mi
      cpu: 500m
```

#### Kubernetes Agents with External Backend

If you have a backend running on a separate server (e.g., via [Native Installation](#️-option-1-native-installation-recommended-for-servers)) and want to monitor Kubernetes nodes, deploy **only the agents** pointing to your external backend:

```bash
# Install agents only (disable backend and frontend in cluster)
helm install infralens-agents ./infralens -n infralens --create-namespace \
  --set backend.enabled=false \
  --set frontend.enabled=false \
  --set agent.externalBackend.enabled=true \
  --set agent.externalBackend.url="http://YOUR_BACKEND_IP:8080" \
  --set agent.clusterName="Production-K8s"
```

**Example values file for agent-only deployment:**

```yaml
# values-agents-only.yaml
backend:
  enabled: false

frontend:
  enabled: false

agent:
  enabled: true
  clusterName: "Production-K8s"  # All K8s nodes grouped under this name
  externalBackend:
    enabled: true
    url: "http://192.168.1.100:8080"  # Your backend server
```

Then install with:
```bash
helm install infralens-agents ./infralens -n infralens --create-namespace -f values-agents-only.yaml
```

**Key Options:**
- `agent.clusterName` - Groups all K8s nodes under a single name in the UI (default: "Kubernetes")
- Without cluster name, each K8s node appears as a separate server

This allows you to monitor **both** standalone servers and Kubernetes clusters from a single InfraLens dashboard!

### 🐳 Option 3: Docker Compose (Development Only)

> ⚠️ **Note:** Docker Compose is recommended for development/testing only. The agent container has limited visibility into host processes. For production, use [Native Installation](#️-option-1-native-installation-recommended-for-servers).

```bash
cd deploy/docker-compose

# Copy and edit environment file
cp env.example .env
vim .env  # Add your API keys

# Start all services
docker-compose up -d

# View logs
docker-compose logs -f

# Stop services
docker-compose down
```

**Required: Edit `.env` file:**

```bash
# AI Providers (at least one required for AI features)
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...
DEFAULT_LLM_PROVIDER=openai
```

Access the dashboard at `http://localhost:3000`

### ☸️ Option 4: Kustomize (GitOps)

```bash
# Deploy all components
kubectl apply -k deploy/k8s/

# Check status
kubectl -n infralens get pods

# View agent logs
kubectl -n infralens logs -l app.kubernetes.io/component=agent -f

# Port-forward to access the frontend
kubectl -n infralens port-forward svc/infralens-frontend 3000:80
```

### 🔧 Building Docker Images

```bash
# Build all images
docker build -t infralens/agent:latest -f Dockerfile.agent .
docker build -t infralens/backend:latest -f Dockerfile.backend .
docker build -t infralens/frontend:latest -f frontend/Dockerfile ./frontend

# Push to registry
docker push infralens/agent:latest
docker push infralens/backend:latest
docker push infralens/frontend:latest
```

## 🖥️ Local Development

### Prerequisites

- **Operating System:** Linux (kernel 5.8+ with BTF support)
  - ⚠️ **macOS/Windows:** eBPF requires Linux - see [Testing on macOS](#-macos-testing-limitations)
- **Go 1.24+**
- **clang/LLVM** (for compiling eBPF C code)
- **Kernel BTF:** Check with `ls /sys/kernel/btf/vmlinux`
- Node.js 18+ (for frontend)

### 1. Install Dependencies

**On Ubuntu/Debian:**
```bash
sudo apt-get update
sudo apt-get install -y clang llvm libbpf-dev linux-headers-$(uname -r) golang-1.24
```

**On Fedora/RHEL:**
```bash
sudo dnf install -y clang llvm libbpf-devel kernel-devel golang
```

### 2. Clone and Setup

```bash
git clone https://github.com/Herenn/Infralens.git
cd infralens
go mod download

# Install bpf2go tool
go install github.com/cilium/ebpf/cmd/bpf2go@v0.12.3
```

### 3. Generate eBPF Go Bindings

⚠️ **This step is required before building!**

```bash
cd agent/ebpf
go generate ./...
```

This compiles `traffic.c` and generates Go bindings (`bpf_bpfel_*.go`) for both amd64 and arm64.

### 4. Build and Run

```bash
# Build agent
cd agent
go build -o infralens-agent .
sudo ./infralens-agent --backend=localhost:8080 --node=dev

# In another terminal - Build backend
cd backend
go build -o backend .
./backend --debug

# In another terminal - Run frontend
cd frontend
npm install
npm run dev
```

Open http://localhost:3000 to see the topology visualization.

### 5. Multi-Server Setup

To monitor multiple servers, run the agent on each server pointing to the central backend:

**Server 1 (Backend + Agent):**
```bash
# Start backend
cd backend && ./backend --debug

# Start agent (same server)
cd agent && sudo ./infralens-agent --backend=localhost:8080 --node=server1
```

**Server 2+ (Agent only):**
```bash
sudo ./infralens-agent --backend=<server1-ip>:8080 --node=server2
```

## ⚙️ Configuration (v0.2.0)

### All Environment Variables

```bash
# ═══════════════════════════════════════════════════════════════════
# SERVER CONFIGURATION
# ═══════════════════════════════════════════════════════════════════
LISTEN_ADDR=:8080              # HTTP listen address
DEBUG=false                    # Enable debug logging
READ_TIMEOUT=15s               # HTTP read timeout
WRITE_TIMEOUT=15s              # HTTP write timeout

# ═══════════════════════════════════════════════════════════════════
# DATABASE
# ═══════════════════════════════════════════════════════════════════
DB_DRIVER=sqlite               # Database driver: sqlite or postgres
DB_DSN=infralens.db            # SQLite: file path, Postgres: connection string
DB_AUTO_MIGRATE=true           # Run migrations on startup
DB_MAX_OPEN_CONNS=25           # Max open connections (default: 1 for SQLite, 25 for Postgres)
DB_MAX_IDLE_CONNS=5            # Max idle connections
DB_CONN_MAX_LIFETIME=5m        # Connection max lifetime

# ═══════════════════════════════════════════════════════════════════
# DATA PRUNING (Auto-cleanup of stale data)
# ═══════════════════════════════════════════════════════════════════
PRUNE_INTERVAL=5m              # How often to prune (0 to disable)
PRUNE_MAX_AGE=30m              # Delete data older than this

# ═══════════════════════════════════════════════════════════════════
# SECURITY
# ═══════════════════════════════════════════════════════════════════
API_KEY=                       # API key for agent auth (empty = disabled)
API_KEY_HEADER=X-API-Key       # Header name for API key
CORS_ORIGINS=*                 # Comma-separated allowed origins
CORS_CREDENTIALS=true          # Allow credentials in CORS

# ═══════════════════════════════════════════════════════════════════
# AI PROVIDERS
# ═══════════════════════════════════════════════════════════════════
OPENAI_API_KEY=sk-...          # OpenAI API key
OPENAI_MODEL=gpt-3.5-turbo     # OpenAI model
ANTHROPIC_API_KEY=sk-ant-...   # Anthropic API key
ANTHROPIC_MODEL=claude-3-haiku-20240307
GEMINI_API_KEY=AIza...         # Google Gemini API key
GEMINI_MODEL=gemini-pro
OLLAMA_URL=http://localhost:11434  # Ollama server URL
OLLAMA_MODEL=llama2
LMSTUDIO_URL=http://localhost:1234 # LM Studio server URL
LMSTUDIO_MODEL=
DEFAULT_LLM_PROVIDER=openai    # Default AI provider
```

### Security Configuration

#### API Key Authentication

Protect agent ingestion endpoints with API key authentication:

```bash
# Generate a secure API key
export API_KEY=$(openssl rand -hex 32)

# Configure backend
export API_KEY="your-secret-api-key"

# Configure agents to use the key
sudo ./infralens-agent --backend=server:8080 --api-key="your-secret-api-key"
```

**Protected endpoints (when API_KEY is set):**
- `POST /api/v1/events`
- `POST /api/v1/stats`
- `POST /api/v1/metrics`
- `POST /api/v1/inspection`

**Public endpoints (always accessible):**
- `GET /api/v1/topology`
- `GET /api/v1/services`
- `GET /api/v1/ws` (WebSocket)
- `GET /health`, `GET /ready`

#### CORS Configuration

```bash
# Development (allow all)
export CORS_ORIGINS="*"

# Production (specific origins)
export CORS_ORIGINS="https://infralens.example.com,https://admin.example.com"
```

### Database Configuration

InfraLens supports SQLite (default) and PostgreSQL.

#### SQLite (Default - Development/Single-Node)

```bash
export DB_DRIVER=sqlite
export DB_DSN=infralens.db
```

#### PostgreSQL (Production/High-Volume)

```bash
export DB_DRIVER=postgres
export DB_DSN="postgres://user:password@localhost:5432/infralens?sslmode=disable"
export DB_MAX_OPEN_CONNS=25
export DB_MAX_IDLE_CONNS=5
```

**PostgreSQL Docker Quick Start:**

```bash
# Start PostgreSQL container
docker run -d --name infralens-db \
  -e POSTGRES_USER=infralens \
  -e POSTGRES_PASSWORD=secret \
  -e POSTGRES_DB=infralens \
  -p 5432:5432 \
  postgres:16-alpine

# Configure InfraLens
export DB_DRIVER=postgres
export DB_DSN="postgres://infralens:secret@localhost:5432/infralens?sslmode=disable"
```

## 🤖 AI Configuration

### UI Configuration

1. Click on any service node in the topology view
2. Go to the "AI Docs" tab
3. Click the ⚙️ Settings icon
4. Enter your API keys
5. Save configuration

### AI API Endpoints

```bash
# Get AI status
GET /api/v1/ai/status

# Configure AI providers
POST /api/v1/ai/config
{
  "openai_api_key": "sk-...",
  "anthropic_api_key": "sk-ant-...",
  "default_provider": "openai"
}

# Generate documentation for a service
POST /api/v1/ai/docs?serviceId=127.0.0.1/backend
{
  "provider": "anthropic"  // optional override
}

# Ask a question about a service
POST /api/v1/ai/ask?serviceId=127.0.0.1/backend
{
  "question": "What are the security concerns for this service?"
}

# List available providers
GET /api/v1/ai/providers
```

## 📡 API Reference

### Backend API (Port 8080)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/events` | POST | Receive connection events from agents |
| `/api/v1/stats` | POST | Receive throughput stats from agents |
| `/api/v1/metrics` | POST | Receive host metrics (CPU/RAM) from agents |
| `/api/v1/inspection` | POST | Receive deep inspection data from agents |
| `/api/v1/topology` | GET | Get current service topology with node metrics |
| `/api/v1/services` | GET | List all discovered services |
| `/api/v1/services/{id}` | GET | Get service details |
| `/api/v1/ws` | WebSocket | Real-time topology updates |
| `/api/v1/graph/stats` | GET | Get graph statistics |
| `/api/v1/k8s/status` | GET | K8s watcher status (pods/services cached) |
| `/api/v1/ai/status` | GET | AI provider configuration status |
| `/api/v1/ai/config` | POST | Configure AI providers |
| `/api/v1/ai/docs` | POST | Generate AI documentation |
| `/api/v1/ai/ask` | POST | Ask AI questions about services |
| `/api/v1/ai/providers` | GET | List available AI providers |
| `/health` | GET | Health check |
| `/ready` | GET | Readiness check |

## 🔧 Troubleshooting

### ❌ "undefined: bpfObjects" Build Error

**Cause:** eBPF Go bindings haven't been generated yet.

**Solution:**
```bash
# Install bpf2go
go install github.com/cilium/ebpf/cmd/bpf2go@v0.12.3

# Generate bindings
cd agent/ebpf
go generate ./...

# Now build
cd ..
go build -o infralens-agent .
```

### ❌ "exec: llvm-strip: executable file not found"

**On macOS:**
```bash
brew install llvm
export PATH="/opt/homebrew/opt/llvm/bin:$PATH"
cd agent/ebpf && go generate
```

**On Linux:**
```bash
sudo apt-get install -y clang llvm libbpf-dev
```

### ❌ "no BTF found for kernel" or "not supported"

**Cause:** Your Linux kernel doesn't have BTF (BPF Type Format) support.

**Check BTF support:**
```bash
ls /sys/kernel/btf/vmlinux
# If file doesn't exist, BTF is not available
```

**Solutions:**
- Use a kernel 5.8+ with `CONFIG_DEBUG_INFO_BTF=y`
- On Ubuntu 20.04+: BTF is enabled by default
- On other distros: Install `linux-headers-$(uname -r)` or upgrade kernel

### ⚠️ macOS Testing Limitations

**Important:** eBPF is Linux-specific and **cannot run on macOS**, even with Docker Desktop.

**Why Docker Desktop doesn't work:**
- Uses a minimal LinuxKit VM without BTF support
- `--pid=host` and `--network=host` don't work properly
- vDSO not properly exposed to containers

**Recommended Testing Options for macOS Users:**

1. **Cloud Linux VM** (Fastest - 5 minutes setup)
   ```bash
   # SSH into any Linux server
   ssh your-vm
   git clone <repo> && cd infralens
   
   # Install dependencies
   sudo apt-get update
   sudo apt-get install -y clang llvm libbpf-dev golang-1.24
   
   # Build and run
   cd agent/ebpf && go generate
   cd .. && go build -o infralens-agent .
   sudo ./infralens-agent
   ```

2. **Local Linux VM (UTM, Parallels, VirtualBox)**
   - Install Ubuntu Server 22.04+ ARM64
   - Allocate 2GB RAM, 2 CPUs
   - Follow same steps as cloud VM

3. **Remote Kubernetes Cluster**
   - Use EKS, GKE, or AKS
   - Deploy with Helm or `kubectl apply -k deploy/k8s/`

### ❌ "Permission denied" or "Operation not permitted"

**Cause:** eBPF requires elevated privileges.

**Solution:**
```bash
# Run with sudo
sudo ./infralens-agent

# Or add capabilities in Kubernetes
securityContext:
  privileged: true
  capabilities:
    add: [SYS_ADMIN, SYS_PTRACE, NET_ADMIN]
```

### ❌ AI Documentation Returns 404

**Cause:** Service ID contains special characters (like `/`).

**Solution:** Service IDs now use the format `IP/process` (e.g., `127.0.0.1/backend`). The frontend automatically URL-encodes these. If making direct API calls:

```bash
# Use query parameters
curl -X POST "http://localhost:8080/api/v1/ai/docs?serviceId=127.0.0.1%2Fbackend"
```

### 🐛 Enable Debug Logging

```bash
# Agent
sudo ./infralens-agent --log-level=debug

# Backend
./backend --debug
```

## 🛣️ Roadmap

### Phase 1 (MVP) ✅
- [x] Project structure and scaffolding
- [x] eBPF kprobe on `tcp_v4_connect`
- [x] CO-RE compatible with `vmlinux.h`
- [x] Go loader using `cilium/ebpf` and `bpf2go`
- [x] Perf event buffer streaming to userspace
- [x] Backend event aggregation with in-memory graph
- [x] REST API + WebSocket for real-time updates
- [x] React Flow topology visualization
- [x] Kubernetes deployment manifests (DaemonSet, RBAC)
- [x] Multi-stage Dockerfile with BPF toolchain

### Phase 2 (Enhancements) ✅
- [x] IPv6 support (`tcp_v6_connect` tracing)
- [x] K8s service name resolution (Pod IP → Pod/Service name via `client-go` informers)
- [x] `/api/v1/k8s/status` endpoint for watcher diagnostics
- [x] Frontend K8s resource badges (Pod/Service type indicators)
- [x] Network throughput tracking (`tcp_sendmsg`/`tcp_recvmsg` kprobes)
- [x] Bytes sent/received with rate calculation (B/s)
- [x] Service fingerprinting (port/process → technology detection)
- [x] Host resource monitoring (CPU/RAM via gopsutil)
- [x] Visual grouping by server/node
- [x] Multi-server agent deployment support
- [x] Connection port badges on service nodes
- [x] Accept tracing (`kretprobe/inet_csk_accept`) - Ingress Visibility
- [x] Deep inspection (protocol probing)
- [x] Dependency discovery

### Phase 3 (AI Integration) ✅
- [x] Multi-provider LLM support (OpenAI, Anthropic, Gemini, Ollama)
- [x] AI-powered service documentation
- [x] Smart code context analysis with line numbers
- [x] Natural language Q&A about services
- [x] Styled markdown rendering with icons
- [x] Helm chart for production deployment
- [x] Docker Compose for development

### Phase 4 (Production Ready) ✅ - v0.2.0
- [x] **Persistent storage** - SQLite with auto-migrations
- [x] **Automatic pruning** - Cleanup stale data on configurable interval
- [x] **API Key authentication** - Secure agent-to-backend communication
- [x] **Configurable CORS** - Environment-based origin whitelist
- [x] **Event Bus architecture** - Pub/sub for real-time WebSocket updates
- [x] **Modular handlers** - Clean separation of API concerns
- [x] **Service layer** - Business logic abstraction
- [x] **Repository pattern** - Swappable storage backends

### Phase 6 (Observability & Testing) ✅ - v0.2.3
- [x] Prometheus metrics package (`/metrics` endpoint)
- [x] HTTP request instrumentation middleware
- [x] Database query metrics
- [x] Event processing metrics
- [x] Unit tests for storage layer (20 tests)
- [x] Integration tests for API handlers (15 tests)
- [x] Service layer hygiene enforcement

### Phase 5 (Scalability) ✅ - v0.2.2
- [x] **PostgreSQL adapter** - Production-ready for high-volume deployments
- [x] **golang-migrate integration** - Professional database migrations
- [x] **Legacy code cleanup** - Removed zombie graph package and old handler

### Phase 6 (Future)
- [ ] UDP tracing (`udp_sendmsg`/`udp_recvmsg`)
- [ ] Prometheus metrics (`/metrics` endpoint)
- [ ] Anomaly detection (unusual traffic patterns)
- [ ] Intelligent alerting on topology changes
- [ ] Service dependency graph export (Mermaid, DOT)
- [ ] Delta-based WebSocket updates (only send changes)
- [ ] Horizontal scaling with shared database

## 🤝 Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details.

## 📄 License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [cilium/ebpf](https://github.com/cilium/ebpf) - Pure Go eBPF library
- [React Flow](https://reactflow.dev/) - Graph visualization library
- [Hubble](https://github.com/cilium/hubble) - Inspiration for eBPF-based observability
- [libbpf-bootstrap](https://github.com/libbpf/libbpf-bootstrap) - eBPF project templates
