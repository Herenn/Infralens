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
  --set backend.ai.openai.apiKey=sk-your-key
```

### Option 2: Docker Compose (Single Server)

```bash
cd docker-compose

# Copy and configure environment
cp env.example .env
# Edit .env with your API keys

# Start all services
docker-compose up -d

# View logs
docker-compose logs -f
```

### Option 3: Kubernetes (Kustomize)

```bash
cd k8s
kubectl apply -k .
```

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

---

## Configuration

### AI Documentation (Optional)

To enable AI-powered documentation, configure at least one provider:

| Provider | Environment Variable | Example |
|----------|---------------------|---------|
| OpenAI | `OPENAI_API_KEY` | `sk-xxx` |
| Anthropic | `ANTHROPIC_API_KEY` | `sk-ant-xxx` |
| Google | `GEMINI_API_KEY` | `xxx` |
| Ollama | `OLLAMA_URL` | `http://localhost:11434` |

### Helm Values

```yaml
backend:
  ai:
    openai:
      apiKey: "sk-your-key"
      model: "gpt-3.5-turbo"
    defaultProvider: "openai"
```

### Docker Compose

```bash
OPENAI_API_KEY=sk-xxx docker-compose up -d
```

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

### AI Documentation not working
```bash
# Check API key is set
kubectl exec -it deploy/infralens-backend -n infralens -- env | grep API_KEY
```
