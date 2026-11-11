# InfraLens Deployment Guide

## Quick Start Options

### Option 1: Helm (Recommended for Kubernetes)

```bash
# Add InfraLens repo (when published)
# helm repo add infralens https://charts.infralens.io

# Install from local chart
helm install infralens ./helm/infralens \
  --namespace infralens \
  --create-namespace \
  --set backend.auth.apiKey="$(openssl rand -hex 32)" \
  --set backend.auth.uiPassword="$(openssl rand -base64 24)" \
  --set backend.persistence.enabled=true \
  --set ingress.enabled=true
```

The backend refuses to start without `backend.auth.apiKey` (or
`backend.auth.allowNoAuth=true`, for evaluation only). `uiPassword` turns on
the UI login; leave it out to keep reads public as in v3.0. Every setting is
documented in [`helm/infralens/values.yaml`](helm/infralens/values.yaml).

### Option 2: Docker Compose (Single Server)

```bash
cd docker-compose

# Copy and configure environment
cp env.example .env
# Edit .env: API_KEY (or ALLOW_NO_AUTH=true), UI_PASSWORD, AI keys, ...

# Start all services
docker compose up -d

# View logs
docker compose logs -f
```

Every setting is documented in [`docker-compose/env.example`](docker-compose/env.example).
To try InfraLens without agents: `docker compose -f demo.yml up -d`.

### Option 3: Kubernetes (Kustomize)

```bash
cd k8s
# Required first: the API key shared by the backend and the agents
kubectl create namespace infralens
kubectl -n infralens create secret generic infralens-api-key \
  --from-literal=api-key="$(openssl rand -hex 32)"
kubectl apply -k .
```

Optional settings (UI login, MCP key, idle-spend prices, agent DNS names) are
commented in `backend-deployment.yaml` and `agent-daemonset.yaml`, each with
the command that creates its Secret.

---

## Deployment Methods Comparison

| Method | Best For | Features |
|--------|----------|----------|
| **Helm** | Production K8s | Templating, upgrades, rollbacks |
| **Docker Compose** | Single server, dev | Simple, quick setup |
| **Kustomize** | K8s with overlays | Built-in to kubectl |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                      │
│                                                              │
│  ┌─────────────────┐  ┌─────────────────┐                   │
│  │    Frontend     │  │     Backend     │                   │
│  │   (React UI)    │◄─│   (Go API)      │◄──────────────┐   │
│  │   Port 3000     │  │   Port 8080     │               │   │
│  └────────┬────────┘  └────────┬────────┘               │   │
│           │                    │                        │   │
│           │              WebSocket                      │   │
│           │                    │                        │   │
│           └──────► User ◄──────┘                        │   │
│                                                         │   │
│  ┌──────────────────────────────────────────────────┐   │   │
│  │              Agent DaemonSet (eBPF)              │   │   │
│  │                                                  │───┘   │
│  │  ┌────────┐  ┌────────┐  ┌────────┐             │       │
│  │  │ Node 1 │  │ Node 2 │  │ Node N │             │       │
│  │  │ Agent  │  │ Agent  │  │ Agent  │             │       │
│  │  └────────┘  └────────┘  └────────┘             │       │
│  └──────────────────────────────────────────────────┘       │
└─────────────────────────────────────────────────────────────┘
```

The frontend's nginx serves the UI and proxies `/api/` (including the
WebSocket) and `/mcp` to the backend at `INFRALENS_BACKEND_URL` (image
default `http://backend:8080`, the docker-compose service name; Helm and the
raw manifests set it to their backend Service). It forwards
`X-Forwarded-For`, `X-Forwarded-Proto` and `X-Forwarded-Host`, which the
backend uses for login throttling, the cookie's `Secure` flag and the
WebSocket origin check. The Helm ingress routes `/`, `/api` and `/mcp` the
same way.

---

## Configuration

Settings by deployment method:

| Setting | Environment (compose, raw k8s) | Helm value |
|---|---|---|
| Agent API key | `API_KEY` (agents: `INFRALENS_API_KEY`) | `backend.auth.apiKey` |
| Run without a key | `ALLOW_NO_AUTH` | `backend.auth.allowNoAuth` |
| UI login | `UI_PASSWORD`, `UI_USERNAME`, `SESSION_SECRET`, `SESSION_TTL`, `TRUSTED_PROXIES` | `backend.auth.uiPassword`, `uiUsername`, `sessionSecret`, `sessionTTL`, `trustedProxies`, or `existingSecret` |
| History | `HISTORY_ENABLED`, `HISTORY_RETENTION`, `HISTORY_MAX_GAP` | `backend.history.*` |
| Changes feed / drift | `DRIFT_ENABLED`, `DRIFT_WEBHOOK_URL`, `DRIFT_CHECK_INTERVAL`, `DRIFT_GONE_AFTER`, `DRIFT_WARMUP` | `backend.drift.*` |
| Idle spend (beta) | `IDLE_COST_*` | `backend.idleCost.*` |
| MCP server | `MCP_ENABLED`, `MCP_API_KEY`, `MCP_ALLOWED_ORIGINS` | `backend.mcp.*` |
| DNS names (experimental) | agent `--dns-names` / `INFRALENS_DNS_NAMES`, `--dns-max-pps`, `--dns-cache-size`, `--dns-stats-interval` | `agent.dnsNames`, `agent.dns.*` |
| Anything else | any backend variable | `backend.extraEnv` |

The full list with defaults is in the main README under *Configuration*.

### Secrets and restarts (Helm)

API keys, the UI password, the session secret, the MCP key, the webhook URL
and AI keys are stored in the chart's Secret. A `helm upgrade` that changes
any of them restarts the backend **and the agents** (checksum annotation), so
a rotated `auth.apiKey` reaches both. A Secret you manage yourself
(`backend.auth.existingSecret`, keys `ui-password` and optional
`session-secret`) is not watched: restart the backend after changing it.

### UI login

With `UI_PASSWORD` set, everything except `/health`, `/ready` and
`/api/v1/auth/*` needs a session or the API key, and agent ingest and
Prometheus `/metrics` need the API key. Set `SESSION_SECRET` (32+ characters)
if sessions should survive restarts or you run more than one backend replica.
If clients can reach the backend directly from private addresses (not only
through the frontend or the ingress), set `TRUSTED_PROXIES` to the real proxy
addresses so forwarded client addresses cannot be forged.

### MCP for AI assistants

`/mcp` is on by default but needs a key: give AI clients `MCP_API_KEY` (valid
on `/mcp` only) rather than `API_KEY`. With `UI_PASSWORD` set and no key,
`/mcp` refuses every request; with no UI password and no key, it is open only
under `ALLOW_NO_AUTH=true`. Client setup is in the main README.

### Changes feed and drift webhook

Drift detection runs whenever history is enabled and fills the UI's Changes
feed; `DRIFT_WEBHOOK_URL` additionally posts each change (Slack/Mattermost
incoming webhooks work as-is). `DRIFT_ENABLED=false` turns both off.

### Idle spend (beta)

No prices are built in. Set a node price (`IDLE_COST_NODE_MONTHLY` /
`backend.idleCost.nodeMonthly`) to see costs. Under Helm, the default
`agent.clusterName: "Kubernetes"` reports all nodes under one name, which only
allows a low-confidence cluster-wide split. Silence counts only while every
agent in the group is reporting; an agent silent for more than an hour stops
holding the group back, and while one has gone silent the group's candidates
are low confidence and out of the total. Set `agent.clusterName=""` for
per-node attribution.

### DNS names for external destinations (experimental)

Off by default and not yet run on a real cluster by the maintainers. Read the
trust model and run the smoke test in the main README before enabling it
(`--set agent.dnsNames=true`). The agent needs `CAP_NET_RAW`, which the
privileged Helm and compose agents have. Agents on `hostNetwork` use
`dnsPolicy: ClusterFirstWithHostNet` so they can resolve the backend Service.

### AI Documentation (Optional)

To enable AI-powered documentation, configure at least one provider (keys can
also be entered in the UI):

| Provider | Environment Variable | Example |
|----------|---------------------|---------|
| OpenAI | `OPENAI_API_KEY` | `sk-xxx` |
| Anthropic | `ANTHROPIC_API_KEY` | `sk-ant-xxx` |
| Google | `GEMINI_API_KEY` | `xxx` |
| Ollama | `OLLAMA_URL` | `http://localhost:11434` |
| LM Studio | `LMSTUDIO_URL` | `http://localhost:1234` |

Leave `*_MODEL` empty to use the backend's current default model.

```yaml
backend:
  ai:
    openai:
      apiKey: "sk-your-key"
      model: ""   # empty = backend default
    defaultProvider: "openai"
```

---

## Upgrading

Back up the database (the `/data` volume, or your PostgreSQL database) before
upgrading. Migrations run at startup, and an older InfraLens will not start on
a database a newer one has migrated, so downgrading means restoring that
backup. See [CHANGELOG.md](../CHANGELOG.md) for version-specific notes.

---

## Requirements

### Kubernetes
- Kubernetes 1.21+
- Helm 3.0+ (for Helm deployment)
- Linux nodes with kernel 5.8+ (for eBPF)

### Docker Compose
- Docker 20.10+
- Docker Compose v2+
- Linux host with kernel 5.8+ (for eBPF agent)

---

## Troubleshooting

### Agent not starting
```bash
# Check if eBPF is supported
cat /sys/kernel/btf/vmlinux  # Should exist

# Check agent logs
kubectl logs -l app.kubernetes.io/component=agent -n infralens
```

### No services appearing
```bash
# Check backend logs
kubectl logs -l app.kubernetes.io/component=backend -n infralens

# Verify agent is sending data
kubectl logs -l app.kubernetes.io/component=agent -n infralens | grep "Sent"
```

### Frontend pod not starting, or 502 on /api
The frontend's nginx cannot reach `INFRALENS_BACKEND_URL`. Helm and the raw
manifests set it to the backend Service; with a custom setup, set it on the
frontend container (e.g. `http://my-backend:8080`).

### AI Documentation not working
```bash
# The backend logs each provider it found a key or URL for at startup
kubectl logs -l app.kubernetes.io/component=backend -n infralens | grep "LLM provider configured"
```
