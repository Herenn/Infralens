<div align="center">

# 🔍 InfraLens

**See every connection in your infrastructure — without touching a single line of code.**

InfraLens uses eBPF to discover and visualize service-to-service communication in real time,
on Kubernetes clusters and plain Linux servers. No sidecars. No SDKs. No instrumentation.

[![CI](https://github.com/Herenn/Infralens/actions/workflows/ci.yml/badge.svg)](https://github.com/Herenn/Infralens/actions/workflows/ci.yml)
[![Release](https://img.shields.io/badge/release-v2.1.0-blue)](https://github.com/Herenn/Infralens/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Herenn/Infralens)](https://go.dev/)
[![License](https://img.shields.io/github/license/Herenn/Infralens)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
[![GitHub Stars](https://img.shields.io/github/stars/Herenn/Infralens?style=social)](https://github.com/Herenn/Infralens/stargazers)

[**Try the demo**](#-try-it-in-30-seconds) · [**Install**](#-installation) · [**How it works**](#%EF%B8%8F-how-it-works) · [**Features**](#-features) · [**Contributing**](#-contributing)

![InfraLens Demo](docs/demo.gif)

</div>

## ⚡ Try It in 30 Seconds

No Linux, no eBPF, no agents — demo mode simulates a realistic multi-node topology with live traffic. Works on macOS, Windows, and Linux:

```bash
git clone https://github.com/Herenn/Infralens.git
cd Infralens/deploy/docker-compose
docker compose -f demo.yml up -d
```

Open **[http://localhost:3000](http://localhost:3000)** — you'll see servers, services, live throughput, and UDP flows. Click any node to inspect it, press `/` to search.

## 🤔 Why InfraLens?

You inherit a cluster, or your architecture diagram is six months stale, and the question is always the same: *what actually talks to what?* Traditional APM answers it by making you instrument every service. Service-mesh tools answer it if you're willing to run a mesh.

InfraLens answers it from the kernel — deploy the agent, and every TCP connection and UDP flow on the box appears on a live map within seconds.

|  | **InfraLens** | Traditional APM | Service Mesh Observability |
|---|---|---|---|
| Code changes required | **None** | SDK in every service | None |
| Infrastructure required | **One agent per node** | Agents + config per app | Full mesh (sidecars/CNI) |
| Works outside Kubernetes | **Yes — any Linux server** | Varies | No |
| Sees non-HTTP traffic (DBs, queues, DNS) | **Yes — TCP + UDP at kernel level** | Only instrumented calls | Mostly yes |
| Explains unknown services | **Yes — fingerprinting + AI docs** | No | No |
| Time to first insight | **Seconds** | Days–weeks | Days |

## 🎯 Features

**See everything**
- 🕸️ **Live topology map** — services grouped by server, edges showing real-time throughput, rendered with React Flow
- 🌐 **TCP + UDP, IPv4 + IPv6** — outbound, inbound, and UDP flows (DNS, StatsD, syslog) traced at the kernel with <1% CPU overhead
- 📊 **Host metrics** — per-server CPU/RAM bars, bytes/packets per connection with live rates

**Understand it**
- 🔎 **Service fingerprinting** — PostgreSQL, Redis, Nginx, Kafka, and dozens more identified from ports and process names
- 🧠 **AI-generated docs** — click any service and get architecture, security, and performance analysis (OpenAI, Anthropic, Gemini, or fully local via Ollama/LM Studio)
- 🔬 **Deep inspection** — protocol probing (HTTP headers, DB handshakes), dependency discovery from `package.json`/`go.mod`/`requirements.txt`
- ⌨️ **Search & export** — find any node instantly (`/`), export the graph as PNG, JSON, Mermaid, or Graphviz DOT

**Run it anywhere**
- ☸️ **Kubernetes native** — DaemonSet + RBAC, automatic IP → Pod/Service name resolution via `client-go` informers
- 🖥️ **Plain Linux servers** — one-line installer, systemd service, multi-node agents reporting to one backend
- 🔒 **Production ready** — API-key agent auth, HTTPS support, SQLite/PostgreSQL persistence, Prometheus `/metrics`, CO-RE portability across kernels 5.8+

<details>
<summary><b>What's new in v2.0.0</b></summary>

- **Demo mode** — try InfraLens without Linux/eBPF/agents (`DEMO_MODE=true`)
- **UDP tracing** — DNS, StatsD, and other UDP flows with dashed edges and protocol badges
- **Topology search** — by name, IP, technology, or node
- **Delta WebSocket updates** — incremental updates instead of full snapshots every 2s
- **Graph export** — Mermaid and Graphviz DOT
- **Working agent auth** — `--api-key` / `INFRALENS_API_KEY` + HTTPS backend URLs
- **Proxy-friendly frontend** — same-origin URLs work behind any ingress/TLS

See [CHANGELOG.md](CHANGELOG.md) for the full history.

</details>

## 🏗️ How It Works

```mermaid
flowchart TB
    subgraph infra["Your Infrastructure"]
        A[Service A] --> B[Service B]
        B --> C[Service C]
        A --> C
    end

    subgraph ebpf["Kernel Space"]
        trace[eBPF Probes<br/>tcp_connect / accept / send / recv / udp_sendmsg]
    end

    subgraph agent["InfraLens Agent"]
        collector[Collector<br/>Event Parsing]
        inspector[Deep Inspector]
        metrics[Host Metrics]
    end

    subgraph backend["InfraLens Backend"]
        api[REST API]
        ws[WebSocket<br/>delta updates]
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

1. **Agent** (one per node) attaches eBPF probes to kernel functions like `tcp_v4_connect` and `udp_sendmsg`, capturing every connection with process context — no packet capture, no proxies.
2. **Backend** aggregates events from all agents, resolves Kubernetes IPs to Pod/Service names, fingerprints services, and persists to SQLite or PostgreSQL.
3. **Frontend** renders the live topology over a delta-based WebSocket, with search, filters, drill-down drawers, and AI documentation.

## 🚀 Installation

### Kubernetes (Helm)

```bash
helm install infralens ./deploy/helm/infralens -n infralens --create-namespace \
  --set ingress.enabled=true \
  --set backend.auth.apiKey="$(openssl rand -hex 32)"
```

### Linux Servers (one-liner)

```bash
# Full stack (main server)
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-full.sh | sudo bash

# Agent only (each additional server)
curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-agent.sh | sudo bash -s -- --backend=YOUR_BACKEND_IP:8080
```

### Docker Compose

```bash
cd deploy/docker-compose
cp env.example .env   # optional: API keys, auth
docker compose up -d
```

Dashboard: `http://localhost:3000`

> **Requirements for real tracing:** Linux kernel 5.8+ with BTF (`ls /sys/kernel/btf/vmlinux` must exist — default on Ubuntu 20.04+, Debian 11+, Fedora 31+). The agent is CO-RE: compile once, run on any supported kernel. No kernel requirements for [demo mode](#-try-it-in-30-seconds).

## 🤖 AI-Powered Documentation

Click any service → **AI Docs** → get a generated explanation of what the service does, its tech stack, network behavior, security considerations, and recommendations — built from its network topology, protocol probes, runtime metrics, and (optionally) project files like README and Dockerfile.

| Provider | Type | Default Model |
|----------|------|---------------|
| OpenAI | Cloud | GPT-3.5-turbo |
| Anthropic | Cloud | Claude 3 Haiku |
| Google Gemini | Cloud | Gemini Pro |
| Ollama | **Local** | Llama2 |
| LM Studio | **Local** | Any compatible |

> 🔒 **Privacy first**: only non-sensitive files (README, Dockerfile, package manifests) are used for context, on-demand, never stored. `.env` files and secrets are always excluded. Use Ollama/LM Studio to keep everything on your own hardware.

## ⚙️ Configuration

Common settings (see the full reference below):

```bash
DEMO_MODE=true                  # Simulated topology, no agents needed
DB_DRIVER=postgres              # sqlite (default) or postgres
API_KEY=$(openssl rand -hex 32) # Require agent authentication
CORS_ORIGINS=https://infralens.example.com
```

Agents authenticate with `--api-key` (or `INFRALENS_API_KEY`) and support HTTPS backends:

```bash
sudo ./infralens-agent --backend=https://infralens.example.com --api-key="your-secret-key"
```

<details>
<summary><b>All environment variables</b></summary>

```bash
# ── Server ──────────────────────────────────────────────
LISTEN_ADDR=:8080              # HTTP listen address
DEBUG=false                    # Enable debug logging
DEMO_MODE=false                # Simulate a live topology (no agents needed)
READ_TIMEOUT=15s               # HTTP read timeout
WRITE_TIMEOUT=15s              # HTTP write timeout

# ── Database ────────────────────────────────────────────
DB_DRIVER=sqlite               # Database driver: sqlite or postgres
DB_DSN=infralens.db            # SQLite: file path, Postgres: connection string
DB_AUTO_MIGRATE=true           # Run migrations on startup
DB_MAX_OPEN_CONNS=25           # Max open connections (default: 1 for SQLite, 25 for Postgres)
DB_MAX_IDLE_CONNS=5            # Max idle connections
DB_CONN_MAX_LIFETIME=5m        # Connection max lifetime

# ── Data pruning ────────────────────────────────────────
PRUNE_INTERVAL=5m              # How often the prune loop runs (0 disables it entirely,
                               # including HISTORY_RETENTION below)
PRUNE_MAX_AGE=30m              # Delete current state older than this (0 = never expire
                               # current state; history retention still applies)

# ── Topology history ─────────────────────────────────────
HISTORY_ENABLED=true           # Record topology history (on by default; ~30% more work per ingested event serially, ~40% under concurrent agent load)
                               # NOTE: history needs a persistent volume. Unlike current
                               # state, it cannot be re-derived by the agents once lost.
                               # Helm: --set backend.persistence.enabled=true
HISTORY_RETENTION=720h         # How long history is kept (30 days)
HISTORY_MAX_GAP=5m             # Gap before a re-appearance opens a new interval

# ── Security ────────────────────────────────────────────
API_KEY=                       # API key for agent auth (empty = disabled)
API_KEY_HEADER=X-API-Key       # Header name for API key
CORS_ORIGINS=*                 # Comma-separated allowed origins
CORS_CREDENTIALS=true          # Allow credentials in CORS

# ── AI providers ────────────────────────────────────────
OPENAI_API_KEY=sk-...          # OpenAI API key
OPENAI_MODEL=gpt-3.5-turbo
ANTHROPIC_API_KEY=sk-ant-...   # Anthropic API key
ANTHROPIC_MODEL=claude-3-haiku-20240307
GEMINI_API_KEY=AIza...         # Google Gemini API key
GEMINI_MODEL=gemini-pro
OLLAMA_URL=http://localhost:11434
OLLAMA_MODEL=llama2
LMSTUDIO_URL=http://localhost:1234
LMSTUDIO_MODEL=
DEFAULT_LLM_PROVIDER=openai
```

</details>

<details>
<summary><b>Security details: protected endpoints, CORS, databases</b></summary>

**Protected endpoints (when `API_KEY` is set):**
`POST /api/v1/events`, `/api/v1/stats`, `/api/v1/metrics`, `/api/v1/inspection`

**Public endpoints (always accessible):**
`GET /api/v1/topology`, `/api/v1/topology/history/range`, `/api/v1/topology/history/stale`, `/api/v1/topology/history/diff`, `/api/v1/services` (including `/{id}` and `/{id}/impact`), `/api/v1/graph/stats`, `/api/v1/graph/criticality`, `/api/v1/graph/orphans`, `/api/v1/ws` (WebSocket), `/health`, `/ready`

**CORS:**

```bash
export CORS_ORIGINS="*"                                    # development
export CORS_ORIGINS="https://infralens.example.com"        # production
```

**Databases:**

```bash
# SQLite (default - development/single node)
export DB_DRIVER=sqlite
export DB_DSN=infralens.db

# PostgreSQL (production/high volume)
export DB_DRIVER=postgres
export DB_DSN="postgres://user:password@localhost:5432/infralens?sslmode=disable"
```

</details>

## 🔬 Under the Hood

### eBPF Probes

| Probe | Purpose | Direction |
|-------|---------|-----------|
| `kprobe(+ret)/tcp_v4_connect` | Outbound IPv4 connections | Outbound |
| `kprobe(+ret)/tcp_v6_connect` | Outbound IPv6 connections | Outbound |
| `kretprobe/inet_csk_accept` | Accepted (incoming) connections | **Inbound** |
| `kprobe/tcp_sendmsg` | Bytes sent | Throughput |
| `kprobe(+ret)/tcp_recvmsg` | Bytes received | Throughput |
| `kprobe/tcp_close` | Connection cleanup | Cleanup |
| `kprobe/udp_sendmsg` / `udpv6_sendmsg` | UDP flow discovery + bytes sent | **UDP** |
| `kprobe(+ret)/udp_recvmsg` / `udpv6_recvmsg` | UDP bytes received | **UDP** |

Events carry a `direction` (`0` outbound / `1` inbound) and a `protocol` (`tcp`/`udp`) field. UDP flows are discovered on first send — for unconnected sockets the destination is read from the syscall's `msg_name`. In the UI, UDP edges render dashed with an amber `UDP` badge.

### Deep Inspection

| Service Type | Detection Method | Data Collected |
|--------------|------------------|----------------|
| **HTTP services** | Probe `/`, `/health`, `/metrics` | Server header, endpoints, health |
| **PostgreSQL** | SSL request handshake | Version, connection status |
| **MySQL** | Protocol greeting packet | Version string |
| **Redis** | PING command | Connection status |
| **MongoDB** | Wire protocol | Connection status |
| **Node.js / Python / Go** | `package.json` / `requirements.txt` / `go.mod` | Dependencies, frameworks |

Inspection is **read-only** by design: environment variable *names* only (never values), config file *names* only, `.env` and secrets always excluded, file reads capped at 50KB/200 lines.

<details>
<summary><b>Project structure</b></summary>

```
infralens/
├── agent/                    # eBPF Agent
│   ├── main.go              # Entry point
│   ├── bpf/                 # BPF C programs (traffic.c, CO-RE headers)
│   ├── collector/           # BPF Go bindings + event parsing
│   ├── inspector/           # Deep inspection
│   ├── metrics/             # Host monitoring
│   └── updater/             # Auto-update
├── backend/                  # Backend Server
│   ├── api/                 # HTTP handlers
│   ├── service/             # Business logic (+ demo simulator)
│   ├── storage/             # SQLite/Postgres
│   ├── k8s/                 # K8s watcher
│   └── pkg/llm/             # AI providers
├── frontend/                 # React Dashboard (React Flow + Tailwind)
├── deploy/                   # Helm chart, Docker Compose, Kustomize
└── scripts/                  # Installation scripts
```

</details>

<details>
<summary><b>API reference</b></summary>

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/events` | POST | Receive connection events from agents |
| `/api/v1/stats` | POST | Receive throughput stats from agents |
| `/api/v1/metrics` | POST | Receive host metrics (CPU/RAM) from agents |
| `/api/v1/inspection` | POST | Receive deep inspection data from agents |
| `/api/v1/topology` | GET | Current service topology with node metrics. With `?at=<RFC3339>`, the topology reconstructed from history at that instant instead (requires `HISTORY_ENABLED`) |
| `/api/v1/topology/export` | GET | Export topology as Mermaid or DOT (`?format=mermaid\|dot`) |
| `/api/v1/topology/history/range` | GET | Earliest/latest instants covered by recorded history, for sizing a timeline control (requires `HISTORY_ENABLED`) |
| `/api/v1/topology/history/stale` | GET | Decommission candidates: services not seen since `?olderThan=<Go duration>` (default: 7 days, or half `HISTORY_RETENTION` when that is shorter — a cutoff beyond the retention window would only ever match already-pruned data). `?limit=` caps results (default 100). Requires `HISTORY_ENABLED` |
| `/api/v1/topology/history/diff` | GET | What appeared/disappeared between `?from=` and `?to=` (both required RFC 3339 timestamps; `to` must not precede `from`, and the span must not exceed 2x `HISTORY_RETENTION`. Requires `HISTORY_ENABLED`) |
| `/api/v1/services` | GET | List all discovered services |
| `/api/v1/services/{id}` | GET | Service details |
| `/api/v1/services/{id}/impact` | GET | Blast radius: the subgraph reachable from this service. `?direction=upstream` (default) is what calls it, transitively; `downstream` is what it calls. `?depth=` caps traversal hops (default 5, max 20) |
| `/api/v1/ws` | WebSocket | Real-time topology updates (snapshot + deltas) |
| `/api/v1/graph/stats` | GET | Graph statistics |
| `/api/v1/graph/criticality` | GET | Services ranked by upstream blast radius - the riskiest single points of failure. `?limit=` caps results (default 20) |
| `/api/v1/graph/orphans` | GET | Services with no connections at all (neither caller nor callee). `?limit=` caps results (default 100) |
| `/api/v1/k8s/status` | GET | K8s watcher status |
| `/api/v1/ai/*` | GET/POST | AI status, config, docs generation, Q&A |
| `/api/v1/version` | GET | Backend version info |
| `/metrics` | GET | Prometheus metrics |
| `/health`, `/ready` | GET | Health/readiness checks |

</details>

## 🔧 Development

```bash
git clone https://github.com/Herenn/Infralens.git
cd Infralens

# Generate BPF bindings (Linux only, needs clang/LLVM)
cd agent/collector && go generate ./... && cd ../..

# Build
go build -o infralens-agent ./agent
go build -o infralens-backend ./backend

# Frontend
cd frontend && npm install && npm run dev
```

**Prerequisites:** Go 1.24+, clang/LLVM, Node.js 20+. For agent testing: Linux kernel 5.8+ with BTF.

**No Linux machine?** Run the backend with `DEMO_MODE=true` and develop the frontend against simulated data.

<details>
<summary><b>Troubleshooting</b></summary>

**"undefined: bpfObjects"** — generate BPF bindings first: `cd agent/collector && go generate ./...`

**"no BTF found for kernel"** — your kernel needs BTF support; check `ls /sys/kernel/btf/vmlinux`

**macOS development** — eBPF requires Linux; use a VM, a remote server, or demo mode.

</details>

## 🛣️ Roadmap

- [x] eBPF TCP tracing (IPv4 + IPv6) with throughput
- [x] Kubernetes service discovery & real-time topology
- [x] Multi-provider AI documentation
- [x] SQLite/PostgreSQL persistence
- [x] UDP tracing *(v2.0)*
- [x] Delta-based WebSocket updates *(v2.0)*
- [x] Demo mode, topology search, Mermaid/DOT export *(v2.0)*
- [ ] Historical time-series storage & time-travel view
- [ ] Anomaly detection & alerting
- [ ] HTTP-level (L7) request tracing
- [ ] Service mesh integration

## 🤝 Contributing

Contributions are very welcome — this project is young and there's a lot of interesting work to pick up, from eBPF probes to React Flow UX. See [CONTRIBUTING.md](CONTRIBUTING.md) to get started, or open an issue to discuss an idea first.

## 📄 License

Apache License 2.0 — see [LICENSE](LICENSE).

## 🙏 Acknowledgments

- [cilium/ebpf](https://github.com/cilium/ebpf) — pure Go eBPF library
- [React Flow](https://reactflow.dev/) — graph visualization
- [Hubble](https://github.com/cilium/hubble) — inspiration

---

<div align="center">

**If InfraLens saved you an afternoon of spelunking through your infrastructure, consider giving it a ⭐ — it helps others find the project.**

</div>
