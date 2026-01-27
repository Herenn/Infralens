# InfraLens 🔍

[![Go Version](https://img.shields.io/github/go-mod/go-version/Herenn/Infralens)](https://go.dev/)
[![License](https://img.shields.io/github/license/Herenn/Infralens)](LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/Herenn/Infralens?style=social)](https://github.com/Herenn/Infralens)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
[![Release](https://img.shields.io/badge/release-v1.0.0-blue)](https://github.com/Herenn/Infralens/releases)

**Zero-Instrumentation Observability for Kubernetes & Linux Servers**

InfraLens is a next-generation observability tool that uses eBPF to automatically discover and visualize service-to-service communication in Kubernetes clusters—without requiring any code changes or sidecars.

![InfraLens Demo](docs/demo.gif)

## 🎯 Features

- **Zero Instrumentation**: No sidecars, no code changes, no SDK integration required
- **Real-time Topology**: Live visualization of service dependencies using React Flow
- **eBPF-Powered**: Efficient kernel-level tracing with <1% CPU overhead using `cilium/ebpf`
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

### Production Features (v1.0.0)

- **Persistent Storage**: SQLite/PostgreSQL for data persistence across restarts
- **Auto-Pruning**: Automatic cleanup of stale services and connections
- **API Key Authentication**: Secure agent-to-backend communication
- **Configurable CORS**: Environment-based CORS origin configuration
- **Event Bus**: Real-time event system for WebSocket optimization
- **Modular Architecture**: Clean separation of concerns with service layer
- **Prometheus Metrics**: Full observability with `/metrics` endpoint
- **Dual-Stack Networking**: Complete IPv4 + IPv6 support

## 🤖 AI-Powered Documentation

InfraLens includes a powerful AI documentation system that generates comprehensive service documentation by analyzing:

- **Source Code**: Reads README, Dockerfile, main entry files, and package manifests
- **Network Topology**: Understands service connections and dependencies
- **Deep Inspection**: Protocol-level service detection
- **Runtime Metrics**: CPU, memory, and throughput data

> 🔒 **Privacy First**: Only specific non-sensitive files (README, Dockerfile, package.json) are analyzed for context. Source code is sent to AI only on-demand and is **never stored permanently**. Sensitive data like `.env` files and secrets are automatically excluded.

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

```mermaid
flowchart TB
    subgraph infra["Your Infrastructure"]
        A[Service A] --> B[Service B]
        B --> C[Service C]
        A --> C
    end

    subgraph ebpf["Kernel Space"]
        trace[eBPF Probes<br/>tcp_connect / accept / send / recv]
    end

    subgraph agent["InfraLens Agent"]
        collector[Collector<br/>Event Parsing]
        inspector[Deep Inspector]
        metrics[Host Metrics]
    end

    subgraph backend["InfraLens Backend"]
        api[REST API]
        ws[WebSocket]
        db[(SQLite/Postgres)]
        ai[AI Providers]
    end

    subgraph frontend["InfraLens Frontend"]
        react[React Flow<br/>Topology View]
    end

    infra -.->|kernel tracing| ebpf
    ebpf --> agent
    agent -->|HTTP POST| backend
    backend <-->|real-time| frontend
```

<details>
<summary>ASCII Diagram (for non-GitHub viewers)</summary>

```
┌─────────────────────────────────────────────────────────────┐
│                    Your Infrastructure                       │
│  ┌─────────┐     ┌─────────┐     ┌─────────┐                │
│  │ Service │────▶│ Service │────▶│ Service │                │
│  │    A    │     │    B    │     │    C    │                │
│  └─────────┘     └─────────┘     └─────────┘                │
│        │              │              │                       │
│        └──────────────┼──────────────┘                       │
│              eBPF Tracing (kernel)                           │
│                       │                                      │
│  ┌────────────────────┴─────────────────────────────────┐   │
│  │                 InfraLens Agent                       │   │
│  │  (DaemonSet on each node)                            │   │
│  └──────────────────────┬───────────────────────────────┘   │
│                         │                                    │
│  ┌──────────────────────┴───────────────────────────────┐   │
│  │              InfraLens Backend                        │   │
│  │  (SQLite/Postgres + AI + WebSocket)                  │   │
│  └──────────────────────┬───────────────────────────────┘   │
│                         │                                    │
│  ┌──────────────────────┴───────────────────────────────┐   │
│  │              InfraLens Frontend                       │   │
│  │  (React Flow Topology Visualization)                 │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

</details>

## 🚀 Quick Start

### Option 1: One-Line Install (Linux Servers)

**Full Stack (Main Server):**
```bash
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-full.sh | sudo bash
```

**Agent Only (Additional Servers):**
```bash
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-agent.sh | sudo bash -s -- --backend=YOUR_BACKEND_IP:8080
```

### Option 2: Helm (Kubernetes)

```bash
helm install infralens ./deploy/helm/infralens -n infralens --create-namespace \
  --set ai.openai.apiKey="sk-..." \
  --set ingress.enabled=true
```

### Option 3: Docker Compose (Development)

```bash
cd deploy/docker-compose
cp env.example .env
# Edit .env with your API keys
docker-compose up -d
```

Access the dashboard at `http://localhost:3000`

## 📁 Project Structure

```
infralens/
├── agent/                    # eBPF Agent
│   ├── main.go              # Entry point
│   ├── bpf/                 # BPF C programs
│   │   ├── traffic.c        # Main tracer (CO-RE)
│   │   └── headers/         # vmlinux.h + libbpf
│   ├── collector/           # BPF Go bindings
│   │   ├── gen.go           # go:generate directive
│   │   ├── collector.go     # Event collection
│   │   └── types.go         # Event/Stats types
│   ├── inspector/           # Deep inspection
│   ├── metrics/             # Host monitoring
│   └── updater/             # Auto-update
│
├── backend/                  # Backend Server
│   ├── api/                 # HTTP handlers
│   ├── service/             # Business logic
│   ├── storage/             # SQLite/Postgres
│   ├── k8s/                 # K8s watcher
│   └── pkg/llm/             # AI providers
│
├── frontend/                 # React Dashboard
│   ├── src/components/      # UI components
│   └── src/hooks/           # WebSocket hook
│
├── deploy/                   # Deployment configs
│   ├── helm/                # Helm chart
│   ├── docker-compose/      # Docker Compose
│   └── k8s/                 # Kustomize
│
└── scripts/                  # Installation scripts
```

## ⚙️ Configuration

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

### AI Configuration

1. Click on any service node in the topology view
2. Go to the "AI Docs" tab
3. Click the ⚙️ Settings icon
4. Enter your API keys
5. Save configuration

Supports: OpenAI, Anthropic Claude, Google Gemini, Ollama (local), LM Studio

## 🔬 eBPF Probes

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

## 🔍 Deep Inspection

InfraLens performs protocol-aware deep inspection to understand services beyond just network connections:

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

| File Type | Purpose |
|-----------|---------|
| `README.md` | Project documentation |
| `Dockerfile` | Container configuration |
| `package.json` / `go.mod` / `requirements.txt` | Dependencies |
| `main.go` / `app.py` / `index.js` | Entry points |

### Security-First Approach

- **Read-only access** - No modifications to filesystem
- **Environment variable names only** - Values are never collected
- **Config file names only** - Contents are never read (except for AI context)
- **No secrets exposure** - Designed for production safety
- **Limited file sizes** - Max 50KB per file, 200 lines max

## 🔧 Development

### Prerequisites

- **Linux kernel 5.8+** with **BTF enabled** (`CONFIG_DEBUG_INFO_BTF=y`)
  - ⚠️ Check BTF: `ls /sys/kernel/btf/vmlinux` - file must exist
  - Ubuntu 20.04+, Debian 11+, Fedora 31+ have BTF by default
- Go 1.24+
- clang/LLVM (for BPF compilation)
- Node.js 18+ (for frontend)

### Build from Source

```bash
# Clone
git clone https://github.com/Herenn/Infralens.git
cd infralens

# Generate BPF bindings (Linux only)
cd agent/collector && go generate ./...

# Build
cd ../.. 
go build -o infralens-agent ./agent
go build -o infralens-backend ./backend

# Frontend
cd frontend && npm install && npm run build
```

## 🐛 Troubleshooting

### "undefined: bpfObjects"

Generate BPF bindings first:
```bash
cd agent/collector && go generate ./...
```

### "no BTF found for kernel"

Your kernel needs BTF support. Check: `ls /sys/kernel/btf/vmlinux`

### macOS Development

eBPF requires Linux. Use a Linux VM or remote server for testing.

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
| `/api/v1/version` | GET | Backend version info |
| `/metrics` | GET | Prometheus metrics endpoint |
| `/health` | GET | Health check |
| `/ready` | GET | Readiness check |

## 🛣️ Roadmap

### Completed ✅

- eBPF-based TCP tracing (IPv4 + IPv6)
- Real-time topology visualization
- Network throughput monitoring
- Kubernetes integration
- Multi-provider AI documentation
- SQLite/PostgreSQL persistence
- Production-ready modular architecture

### Planned

- UDP tracing
- Anomaly detection
- Service mesh integration
- Graph export (Mermaid, DOT)
- Delta-based WebSocket updates

## 🤝 Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md).

## 📄 License

Apache License 2.0 - see [LICENSE](LICENSE).

## 🙏 Acknowledgments

- [cilium/ebpf](https://github.com/cilium/ebpf) - Pure Go eBPF library
- [React Flow](https://reactflow.dev/) - Graph visualization
- [Hubble](https://github.com/cilium/hubble) - Inspiration
