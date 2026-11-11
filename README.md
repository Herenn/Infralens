<div align="center">

# 🔍 InfraLens

**See every connection in your infrastructure — without touching a single line of code.**

InfraLens uses eBPF to discover and visualize service-to-service communication in real time,
on Kubernetes clusters and plain Linux servers. No sidecars. No SDKs. No instrumentation.

[![CI](https://github.com/Herenn/Infralens/actions/workflows/ci.yml/badge.svg)](https://github.com/Herenn/Infralens/actions/workflows/ci.yml)
[![Release](https://img.shields.io/badge/release-v3.1.0-blue)](https://github.com/Herenn/Infralens/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Herenn/Infralens)](https://go.dev/)
[![License](https://img.shields.io/github/license/Herenn/Infralens)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
[![GitHub Stars](https://img.shields.io/github/stars/Herenn/Infralens?style=social)](https://github.com/Herenn/Infralens/stargazers)

Built and maintained by [Eren Çiftçi](https://github.com/Herenn) · Commercial support and consulting by [Opsilla](https://opsilla.com)

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

Open **[http://localhost:3000](http://localhost:3000)** — you'll see servers, services, live throughput, and UDP flows, plus two weeks of simulated history to scrub through, a Changes feed, and idle services. Click any node to inspect it, press `/` to search.

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
- 🏷️ **DNS names for external destinations** *(experimental, off by default)* — `api.stripe.com` instead of a bare IP, learned from the DNS answers your workloads received

**Understand it**
- 🔎 **Service fingerprinting** — PostgreSQL, Redis, Nginx, Kafka, and dozens more identified from ports and process names
- 🧠 **AI-generated docs** — click any service and get architecture, security, and performance analysis (OpenAI, Anthropic, Gemini, or fully local via Ollama/LM Studio)
- 🔌 **MCP server** — ask Claude Code, Cursor, or any MCP client "what calls payments-db?" — read-only topology tools at `/mcp`
- 🔬 **Deep inspection** — protocol probing (HTTP headers, DB handshakes), dependency discovery from `package.json`/`go.mod`/`requirements.txt`
- ⌨️ **Search & export** — find any node instantly (`/`), export the graph as PNG, JSON, Mermaid, or Graphviz DOT

**Remember it** *(v3)*
- ⏪ **Time travel** — topology history is recorded continuously; scrub the timeline to see the architecture as it was at any moment in the retention window (30 days by default)
- 🔀 **Topology diff** — what appeared and disappeared between any two instants
- 🔔 **Changes feed & drift notifications** — see when a never-before-seen dependency appears (internet-bound ones flagged) or an established one stops being observed, in the UI as it happens and optionally on a webhook (Slack-compatible)
- 💥 **Blast radius & criticality** — what breaks if a service goes down, and which services are the riskiest single points of failure
- 🧹 **Orphans & decommission candidates** — services nothing talks to, and services not seen in days
- 💸 **Idle spend** *(beta)* — cleanup candidates with the network evidence for each and an approximate share of what their node costs

**Run it anywhere**
- ☸️ **Kubernetes native** — DaemonSet + RBAC, automatic IP → Pod/Service name resolution via `client-go` informers
- 🖥️ **Plain Linux servers** — one-line installer, systemd service, multi-node agents reporting to one backend
- 🔒 **Production ready** — optional UI login, API-key agent auth, HTTPS support, SQLite/PostgreSQL persistence, Prometheus `/metrics`, CO-RE portability across kernels 5.8+

<details>
<summary><b>What's new in v3.1.0</b></summary>

- **UI login** *(opt-in)* — set `UI_PASSWORD` and the UI and every read endpoint require signing in (or the API key)
- **Changes feed** — drift events persisted and shown in the UI with an unread badge, filters, and a jump to that moment in time travel; detection no longer needs a webhook
- **MCP server** — read-only topology tools for AI assistants at `/mcp`, with a separate read-only `MCP_API_KEY`
- **Idle spend** *(beta)* — what idle services cost, with evidence, from node prices you supply
- **DNS names for external destinations** *(experimental, off by default)* — agent `--dns-names`
- **Current AI models** — live model lists, new defaults, clear errors when a model is unavailable
- **Fixed:** time travel and diffs on servers not running in UTC; the frontend's nginx under Helm and raw Kubernetes manifests

Upgrading: **back up the database first** — after v3.1 has run, v3.0 will not start on it. Old SQLite timestamps are converted to UTC automatically at first start. Nothing changes for auth unless you set `UI_PASSWORD`. See [CHANGELOG.md](CHANGELOG.md).

</details>

<details>
<summary><b>What's new in v3.0.0</b></summary>

- **Topology history** — interval-based recording, `GET /topology?at=`, and a timeline scrubber in the UI
- **Architecture drift notifications** — `DRIFT_WEBHOOK_URL`
- **Topology diff, blast radius, criticality ranking, orphan detection, decommission candidates**

Upgrading: history needs a persistent volume to survive restarts (Helm: `--set backend.persistence.enabled=true`). New read endpoints are public, like `/topology` — read *Security details* under Configuration before exposing InfraLens on an untrusted network. See [CHANGELOG.md](CHANGELOG.md).

</details>

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
  --set backend.auth.apiKey="$(openssl rand -hex 32)" \
  --set backend.auth.uiPassword="$(openssl rand -base64 24)"   # optional: require login
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
| OpenAI | Cloud | `gpt-6-luna` |
| Anthropic | Cloud | `claude-haiku-5-5` |
| Google Gemini | Cloud | `gemini-3.5-flash-lite` |
| Ollama | **Local** | First installed model |
| LM Studio | **Local** | First loaded model |

Keys and models can be set from the UI or with environment variables (`OPENAI_API_KEY`, `OPENAI_MODEL`, …; an empty `*_MODEL` means the default above). The model pickers list what your account or local server actually offers, read live from each provider. If a provider rejects a model, the UI says so and suggests available ones instead of failing silently.

> 🔒 **Privacy first**: only non-sensitive files (README, Dockerfile, package manifests) are used for context, on-demand, never stored. `.env` files and secrets are always excluded. Use Ollama/LM Studio to keep everything on your own hardware.

## 🔌 Connect Your AI Assistant (MCP)

The backend serves a read-only [Model Context Protocol](https://modelcontextprotocol.io) endpoint at `/mcp` (Streamable HTTP; through the bundled frontend too, e.g. `http://localhost:3000/mcp`). Ask your assistant *"what breaks if redis goes down?"*, *"what changed since yesterday?"* or *"which services has nothing called in a week?"* and it answers from the live map and its history.

Give AI clients the read-only `MCP_API_KEY` (valid on `/mcp` only), not the agents' `API_KEY`:

```bash
export INFRALENS_MCP_API_KEY=...   # the backend's MCP_API_KEY

# Claude Code
claude mcp add --transport http infralens http://localhost:8080/mcp \
  --header "Authorization: Bearer $INFRALENS_MCP_API_KEY"
```

<details>
<summary><b>Other clients, tools, and access rules</b></summary>

**Claude Code, per project** (`.mcp.json`; keeps the key out of the file):

```json
{
  "mcpServers": {
    "infralens": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": { "Authorization": "Bearer ${INFRALENS_MCP_API_KEY}" }
    }
  }
}
```

**Cursor** (`.cursor/mcp.json` or `~/.cursor/mcp.json`; the variable must be set in the environment Cursor starts from):

```json
{
  "mcpServers": {
    "infralens": {
      "url": "http://localhost:8080/mcp",
      "headers": { "Authorization": "Bearer ${env:INFRALENS_MCP_API_KEY}" }
    }
  }
}
```

**Claude Desktop**, through [`mcp-remote`](https://www.npmjs.com/package/mcp-remote) (`claude_desktop_config.json`; the header value has no space after the colon because some clients do not escape spaces in `args`):

```json
{
  "mcpServers": {
    "infralens": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://localhost:8080/mcp",
               "--transport", "http-only", "--header", "Authorization:${AUTH_HEADER}"],
      "env": { "AUTH_HEADER": "Bearer <your MCP_API_KEY>" }
    }
  }
}
```

For a plain `http://` URL on another host, use HTTPS instead, or add `--allow-http` to the `args` on a trusted private network.

**Tools** (all read-only): `topology_overview`, `search_services`, `get_service`, `get_dependencies`, `get_blast_radius`, `list_connections`, `diff_topology`, `rank_criticality`, `find_orphans`, `find_stale_services`, `export_mermaid`, `list_drift_events` (while drift detection is on), `get_idle_spend`. Services can be named by ID, name, IP, or DNS name; times accept RFC 3339 or relative values.

**Access:** `/mcp` accepts `MCP_API_KEY` or `API_KEY`, as `Authorization: Bearer <key>` or `X-API-Key: <key>` — never the UI's session cookie. With no key configured it is open only when `ALLOW_NO_AUTH=true` *and* no `UI_PASSWORD` is set; with a UI password and no key it refuses every request. A service's process fingerprint is returned only to key-authenticated callers; command lines and environment variables never are. Browser origins other than loopback need `MCP_ALLOWED_ORIGINS`. `MCP_ENABLED=false` turns the endpoint off. Protocol revisions 2026-07-28, 2025-11-25, and 2025-06-18 are supported.

</details>

## 💸 Idle Spend *(beta)*

The **Idle spend** panel lists cleanup candidates — services nothing talks to — with the network evidence for each (for example, no inbound connection in 9 observed days) and an approximate share of their node's monthly price. They are **candidates to verify with their owners, not recommendations to delete**. InfraLens ships no price list: set `IDLE_COST_NODE_MONTHLY=140` (or per-node/per-service rules) to see costs, or leave it empty to see the evidence alone. Also available as Markdown/CSV and at `GET /api/v1/graph/idle-cost`.

<details>
<summary><b>How it decides, what it can't see, and settings</b></summary>

- **Categories:** no connections at all · no inbound · inbound stopped · reached only by other idle services · not seen at all recently.
- **Silence is observed, not assumed:** it counts only while that node's own agent was reporting, so an agent outage never makes a service look idle. Anything still carrying traffic is never a candidate. Per-node figures never add up to more than the node's price.
- **Confidence:** high needs more than 31 observed days of silence, so with the default 30-day history retention the best you will see is medium.
- **Held back, with the reason shown:** outbound-only workers and cron jobs, monitoring, possible replicas/standbys, UDP services, servers reached by address, services younger than `IDLE_COST_WARMUP`, InfraLens itself, anything in `IDLE_COST_EXCLUDE`, and everything except true orphans when history is off.
- **Blind spots:** servers reached through a differently named Kubernetes Service, hostPort/NodePort traffic to the node's IP, receive-only UDP, and jobs that run less often than the retention window.
- **Per-node prices need per-node agents.** Agents started with `--cluster` (the Helm chart's default `agent.clusterName: "Kubernetes"`) report every node under one name, which allows only a low-confidence cluster-wide split; set `agent.clusterName=""` for per-node attribution.
- **After upgrading from v3.0**, services on nodes without agent heartbeats yet are held back until about `IDLE_COST_QUIET_AFTER` (72h) of heartbeats exist.

```bash
IDLE_COST_NODE_MONTHLY=140                    # default monthly price of one node
IDLE_COST_NODE_PRICES="gpu-*=1800;db-1=420"   # per-node overrides (glob=price)
IDLE_COST_SERVICE_PRICES="billing-db=310"     # per-service prices, carved out of the node's
IDLE_COST_CURRENCY=USD                        # a label; no conversion
IDLE_COST_EXCLUDE="nightly-*;dr-standby"      # never flag these
IDLE_COST_WINDOW=30d                          # history consulted (capped at HISTORY_RETENTION)
IDLE_COST_QUIET_AFTER=72h                     # observed silence before a service is flagged
IDLE_COST_WARMUP=24h                          # ignore younger services (0 disables)
```

`GET /api/v1/graph/idle-cost` takes `format=json|markdown|csv`, `limit` (default 100; totals always cover everything) and `assume_node_monthly` for a labelled what-if price. Reports are cached for 60 seconds. Helm: `backend.idleCost.*`.

</details>

## 🌐 DNS Names for External Destinations *(experimental)*

With `--dns-names` (or `INFRALENS_DNS_NAMES=true`; Helm `--set agent.dnsNames=true`) the agent labels external destinations with the name your workload looked up — `api.stripe.com` instead of `52.1.2.3` — on the map, in search, exports, AI context, the Changes feed, the webhook, MCP, and Idle spend. **Off by default and experimental: it has not yet been run on a real cluster by the maintainers.** Run the smoke test below before enabling it in production.

<details>
<summary><b>How it works, trust model, limits, and smoke test</b></summary>

- **How:** the agent passively reads DNS over UDP on the node with an AF_PACKET socket and a classic-BPF filter — the mechanism `tcpdump` uses. Userspace only (no eBPF changes), no lookups of its own, needs `CAP_NET_RAW` (the Helm agent is privileged). An answer is believed only if it matches a query the same local workload sent — address, port, server, transaction ID, and question — on its own interface within 5 seconds. A name is reported only for a destination that workload then connected to; names for lookups never followed by a connection never leave the node. The backend keeps up to 8 names per connection and drops names not seen for 7 days. Service IDs do not change.
- **Trust model:** names are display labels, not verified facts. Whoever answers a workload's DNS — its resolver, or anyone on the path to it — decides that workload's labels. On bridge-based CNIs, a pod can spoof a query on behalf of a neighbour on the same node.
- **Not visible:** DNS over HTTPS/TLS, DNS over TCP, `/etc/hosts`, lookups made before the agent started, some dual-stack cases (an AAAA lookup followed by a connection over IPv4), and Cilium with eBPF host routing.
- **Tuning:** `--dns-max-pps` (default 5000; excess is dropped by the kernel), `--dns-cache-size` (default 32768 client/address pairs), `--dns-stats-interval` (default 5m; 0 disables the counter log). Helm: `agent.dns.maxPPS`, `cacheSize`, `statsInterval`.

**Smoke test before production:**

```bash
# 1. On a representative node (root): the real capture path, end to end
#    (no Go on the node? build with GOOS=linux on another machine and copy the binary)
go test -c -o dnscache.test ./agent/dnscache
sudo ./dnscache.test -test.run TestCaptureIntegration -test.v

# 2. Enable it on one canary node only (agent flags --dns-names=true --dns-stats-interval=1m),
#    then make a lookup and a connection from a pod on that node, e.g.
kubectl exec <pod> -- wget -qO- https://example.com >/dev/null

# 3. The agent logs "DNS capture: ... learned=N ... kernel_drops=0 ..." every minute:
#    expect learned > 0 and kernel_drops = 0
# 4. The destination shows its name:
curl -s -H "X-API-Key: $API_KEY" http://<backend>/api/v1/topology | grep -o '"hostname":"[^"]*"'
```

</details>

## ⚙️ Configuration

Common settings (see the full reference below):

```bash
API_KEY=$(openssl rand -hex 32)          # Agent authentication (required unless ALLOW_NO_AUTH=true)
UI_PASSWORD=$(openssl rand -base64 24)   # Turn on the UI login (recommended on any shared network)
SESSION_SECRET=$(openssl rand -hex 32)   # Keep sessions across restarts and replicas
MCP_API_KEY=$(openssl rand -hex 32)      # Read-only key for AI assistants on /mcp
DB_DRIVER=postgres                       # sqlite (default) or postgres
CORS_ORIGINS=https://infralens.example.com
DEMO_MODE=true                           # Simulated topology, no agents needed
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
DEMO_MODE=false                # Simulate a live topology (no agents needed). Uses its own
                               # database file, infralens-demo.db, unless DB_DSN is set
READ_TIMEOUT=15s               # HTTP read timeout
WRITE_TIMEOUT=15s              # HTTP write timeout
IDLE_TIMEOUT=60s               # HTTP keep-alive idle timeout
MAX_REQUEST_BYTES=10485760     # Request body size limit (10 MiB)

# ── Database ────────────────────────────────────────────
DB_DRIVER=sqlite               # Database driver: sqlite or postgres
DB_DSN=infralens.db            # SQLite: file path, Postgres: connection string
DB_AUTO_MIGRATE=true           # Run migrations on startup
DB_MAX_OPEN_CONNS=1            # Max open connections (default: 1 for SQLite, 25 for Postgres)
DB_MAX_IDLE_CONNS=1            # Max idle connections (default: 1 for SQLite, 5 for Postgres)
DB_CONN_MAX_LIFETIME=0         # Connection max lifetime (0 = unlimited)

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
HISTORY_RETENTION=720h         # How long history (and the Changes feed) is kept (30 days)
HISTORY_MAX_GAP=5m             # Gap before a re-appearance opens a new interval

# ── Changes feed / drift (needs HISTORY_ENABLED) ────────
DRIFT_ENABLED=true             # Drift detection: the UI's Changes feed and the webhook
                               # (false = both off; /api/v1/drift/* answer 404)
DRIFT_WEBHOOK_URL=             # Also POST changes here (optional; empty = feed only).
                               # Slack/Mattermost incoming webhook URLs work as-is:
                               # the payload has a "text" field plus structured
                               # "appeared"/"disappeared" lists for custom receivers
DRIFT_CHECK_INTERVAL=5m        # How often history is checked for changes
DRIFT_GONE_AFTER=24h           # Unobserved this long = reported as gone (time when
                               # collection was down does not count)
DRIFT_WARMUP=1h                # No "new dependency" reports for the first hour of
                               # history, so a fresh install doesn't announce everything

# ── Security ────────────────────────────────────────────
API_KEY=                       # API key for agents and API clients. Required: the backend
                               # refuses to start without it unless ALLOW_NO_AUTH=true
ALLOW_NO_AUTH=false            # Start without API_KEY (evaluation / trusted networks only)
API_KEY_HEADER=X-API-Key       # Header name for API key (Authorization: Bearer also works)
CORS_ORIGINS=*                 # Comma-separated allowed origins
CORS_CREDENTIALS=false         # Allow credentials in CORS (can't combine with origin *)
CORS_MAX_AGE=3600              # Preflight cache time, in seconds

# ── UI login (opt-in) ───────────────────────────────────
UI_PASSWORD=                   # Empty = no login, reads are public (v3.0 behaviour).
                               # Set = UI and all reads need a session or API_KEY.
                               # At least 8 characters (12+ recommended)
UI_USERNAME=admin              # The single admin account's name
SESSION_SECRET=                # Signs session cookies, 32+ characters. Empty = random
                               # per process: restarts sign everyone out, and sessions
                               # don't work across replicas
SESSION_TTL=12h                # Absolute session lifetime
TRUSTED_PROXIES=               # Whose X-Forwarded-For to believe for login throttling.
                               # Empty = loopback/private peers, one hop; or a comma-
                               # separated CIDR/IP list of every proxy hop; or "none"

# ── MCP server for AI assistants ────────────────────────
MCP_ENABLED=true               # Serve read-only topology tools at /mcp
MCP_API_KEY=                   # Read-only key valid on /mcp only (API_KEY also works there)
MCP_ALLOWED_ORIGINS=           # Extra browser origins allowed to call /mcp (comma-separated)

# ── Idle spend (beta) ───────────────────────────────────
IDLE_COST_NODE_MONTHLY=        # Default monthly price of one node (empty = no costs shown)
IDLE_COST_NODE_PRICES=         # Per-node prices, glob=price, ";"-separated: "gpu-*=1800;db-1=420"
IDLE_COST_SERVICE_PRICES=      # Per-service prices (name/ID glob=price), carved out of the node's
IDLE_COST_CURRENCY=USD         # Display label, no conversion
IDLE_COST_EXCLUDE=             # Globs never flagged: "nightly-*;dr-standby"
IDLE_COST_WINDOW=30d           # History consulted (capped at HISTORY_RETENTION)
IDLE_COST_QUIET_AFTER=72h      # Observed silence before a service is flagged
IDLE_COST_WARMUP=24h           # Ignore services younger than this (0 disables)

# ── AI providers ────────────────────────────────────────
OPENAI_API_KEY=sk-...          # OpenAI API key
OPENAI_MODEL=                  # Empty = default (gpt-6-luna)
ANTHROPIC_API_KEY=sk-ant-...   # Anthropic API key
ANTHROPIC_MODEL=               # Empty = default (claude-haiku-5-5)
GEMINI_API_KEY=AIza...         # Google Gemini API key
GEMINI_MODEL=                  # Empty = default (gemini-3.5-flash-lite)
OLLAMA_URL=http://localhost:11434
OLLAMA_MODEL=                  # Empty = first installed model
LMSTUDIO_URL=http://localhost:1234
LMSTUDIO_MODEL=                # Empty = first loaded model
DEFAULT_LLM_PROVIDER=          # openai, anthropic, gemini, ollama, lmstudio (empty = first configured)
```

**Agent flags** (`infralens-agent --help` for all): `--backend`, `--api-key` / `INFRALENS_API_KEY`, `--node`, `--cluster` (report every node under one name), `--inspect`, `--auto-update`, and the experimental `--dns-names` / `INFRALENS_DNS_NAMES` with `--dns-max-pps`, `--dns-cache-size`, `--dns-stats-interval` (see [DNS names](#-dns-names-for-external-destinations-experimental)).

</details>

<details>
<summary><b>Security details: authentication, CORS, databases</b></summary>

**With `UI_PASSWORD` set** (recommended on any network you don't fully trust), the API is deny-by-default:

- Public: `/health`, `/ready`, and `/api/v1/auth/*` (`POST login`, `POST logout`, `GET status`).
- API key only: agent ingest (`POST /api/v1/events`, `/stats`, `/metrics`, `/inspection`) and Prometheus `/metrics`.
- Everything else — UI, reads, WebSocket, AI, and any endpoint added later — needs a session or the API key (`X-API-Key` or `Authorization: Bearer`). `/mcp` has its own key rules ([MCP](#-connect-your-ai-assistant-mcp)) and never accepts the session cookie.

Sessions are HMAC-signed cookies (HttpOnly, `SameSite=Strict`, `Secure` over HTTPS — directly or per `X-Forwarded-Proto`). Changing the password ends every session; logouts are stored in the database, so they survive restarts, reach other replicas within about 5 seconds, and close that session's WebSockets. Cookie-authenticated requests must be same-origin or come from an exact `CORS_ORIGINS` entry; `*` and wildcard patterns are never trusted with the cookie. Logins are throttled per client (5 attempts, then one per 20 s), per connecting proxy/peer (60, then one per second), and globally (120, then two per second); a client that has signed in before keeps only its own limit. Throttled attempts get `429` with `Retry-After`, even with the right password. Behind proxies, set `TRUSTED_PROXIES` to the real proxy addresses so forwarded client addresses can't be forged. It is a single admin account; there are no roles or SSO yet.

A UI built with `VITE_API_URL` pointing at a backend on another origin works with login only if both are on the same site (e.g. `ui.example.com` and `api.example.com`), `CORS_ORIGINS` lists the UI's origin exactly, and `CORS_CREDENTIALS=true`.

**Without `UI_PASSWORD`** (v3.0 behaviour, with a warning at startup): agent ingest, AI configuration and generation, `/api/v1/version`, and Prometheus `/metrics` need `API_KEY` (with `ALLOW_NO_AUTH=true` and no key, nothing is protected); these are always public:
`GET /api/v1/topology`, `/api/v1/topology/export`, `/api/v1/topology/history/range`, `/stale`, `/diff`, `/api/v1/services` (including `/{id}`), `/api/v1/graph/stats`, `/criticality`, `/orphans`, `/impact`, `/idle-cost`, `/api/v1/drift/events`, `/summary`, `/api/v1/k8s/status`, `/api/v1/ai/status`, `/api/v1/ai/providers`, `/api/v1/ws` (WebSocket), `/api/v1/auth/*`, `/health`, `/ready`. History endpoints expose up to `HISTORY_RETENTION` of past topology (service names, namespaces, nodes, IPs) — don't run without a UI password on an untrusted network.

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

Migrations run automatically at startup (`DB_AUTO_MIGRATE=true`). **Back up before upgrading**: an older InfraLens will not start on a database a newer one has migrated.

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

### Storage & History

Current state (services, connections, node metrics) is kept for `PRUNE_MAX_AGE` and refreshed by the agents; long-lived connections stay on the map as long as traffic flows. History is recorded as intervals — one row per continuous observation, extended in place — so write volume follows how often the architecture changes, not how often agents report. Each agent's host-metrics reports double as a **heartbeat**, recorded per node, so drift and Idle spend can tell "quiet" from "not watched": silence only counts while that node's own agent was reporting. All times are stored in UTC. Schema migrations (`backend/migrations`, currently 000001–000007, mirrored for PostgreSQL) run at startup.

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
│   ├── dnscache/            # DNS names for destinations (experimental)
│   └── updater/             # Auto-update
├── backend/                  # Backend Server
│   ├── api/                 # HTTP handlers, auth middleware
│   ├── mcp/                 # MCP server (/mcp)
│   ├── service/             # Business logic: drift, idle spend (+ demo simulator)
│   ├── storage/             # SQLite/Postgres
│   ├── migrations/          # Schema migrations
│   ├── k8s/                 # K8s watcher
│   └── pkg/llm/             # AI providers
├── frontend/                 # React Dashboard (React Flow + Tailwind)
├── deploy/                   # Helm chart, Docker Compose, Kustomize
└── scripts/                  # Installation scripts, demo GIF recorder
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
| `/api/v1/ws` | WebSocket | Real-time topology updates (snapshot + deltas) and `drift` messages with new Changes (at most 50 events each) |
| `/api/v1/graph/stats` | GET | Graph statistics |
| `/api/v1/graph/criticality` | GET | Services ranked by upstream blast radius - the riskiest single points of failure. `?limit=` caps results (default 20) |
| `/api/v1/graph/orphans` | GET | Services with no connections at all (neither caller nor callee). `?limit=` caps results (default 100) |
| `/api/v1/graph/idle-cost` | GET | Idle spend *(beta)*: cleanup candidates with evidence and estimated monthly cost. `?format=json\|markdown\|csv` (default json), `?limit=` (default 100; totals cover everything), `?assume_node_monthly=` for a labelled what-if price |
| `/api/v1/drift/events` | GET | The Changes feed, newest first. `?since=`/`?until=` (RFC 3339, exclusive), `?kind=appeared\|disappeared`, `?external=true\|false`, `?limit=` (default 50, max 500), `?cursor=` (from `next_cursor`). 404 when `DRIFT_ENABLED=false` |
| `/api/v1/drift/summary` | GET | Event counts since `?since=` plus the detector's state, for an unread badge. 404 when `DRIFT_ENABLED=false` |
| `/api/v1/graph/impact` | GET | Blast radius: the subgraph reachable from `?service=<id>` (required). `?direction=upstream` (default) is what calls it, transitively; `downstream` is what it calls. `?depth=` caps traversal hops (default 5, max 20). `service` is a query parameter rather than `/services/{id}/impact` because service IDs contain literal `/`, which would make an ID ending in the literal substring `/impact` indistinguishable from the action route |
| `/api/v1/k8s/status` | GET | K8s watcher status |
| `/api/v1/ai/*` | GET/POST | AI status, providers and models, config, docs generation, Q&A. `POST /ai/config` keeps fields you leave out and accepts `"clear": [...]`. An unavailable model answers 422 `model_unavailable` with suggestions; a refused or empty answer 502 `response_refused` |
| `/api/v1/auth/login`, `/logout` | POST | UI login and logout (`{"username","password"}`) |
| `/api/v1/auth/status` | GET | Whether login is required and who is signed in |
| `/mcp` | POST | MCP endpoint for AI assistants (Streamable HTTP; see [MCP](#-connect-your-ai-assistant-mcp)) |
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

**No Linux machine?** Run the backend in demo mode — `DEMO_MODE=true ALLOW_NO_AUTH=true ./infralens-backend` (it writes to its own `infralens-demo.db`) — and develop the frontend against simulated data; `npm run dev` proxies `/api` and `/mcp` to `localhost:8080`.

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
- [x] Topology history & time-travel view *(v3.0)*
- [x] Architecture drift notifications, topology diff, blast radius *(v3.0)*
- [x] UI authentication — single admin login *(v3.1)*
- [x] Changes feed, MCP server, Idle spend (beta), DNS names (experimental) *(v3.1)*
- [ ] OIDC/SSO and role-based access
- [ ] Real per-connection latency (TCP smoothed RTT)
- [ ] HTTP-level (L7) request tracing
- [ ] Drift rules (ignore lists, traffic-change alerts)
- [ ] Weekly change digest
- [ ] "Reappeared" marker in the Changes feed
- [ ] DNS names in the decommission list
- [ ] Service mesh integration

## 🤝 Contributing

Contributions are very welcome — this project is young and there's a lot of interesting work to pick up, from eBPF probes to React Flow UX. See [CONTRIBUTING.md](CONTRIBUTING.md) to get started, or open an issue to discuss an idea first.

## Commercial support

<a href="https://opsilla.com">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/opsilla-logo-dark.png">
    <img src="docs/assets/opsilla-logo-light.png" alt="Opsilla" width="160">
  </picture>
</a>

InfraLens is free and open source. If you want help deploying it, extending it, or running Kubernetes and cloud infrastructure more broadly, [Opsilla](https://opsilla.com) offers assessments, project work and ongoing advisory.

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
