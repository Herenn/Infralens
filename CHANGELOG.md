# Changelog

All notable changes to InfraLens are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.0] - 2026-08-04

### Added

- **Demo mode**: `DEMO_MODE=true` runs a built-in topology simulator so InfraLens
  can be tried in 30 seconds without Linux, eBPF, or any agents. Includes
  `deploy/docker-compose/demo.yml` and a `backend.demoMode` Helm value.
- **UDP tracing**: new `udp_sendmsg`/`udpv6_sendmsg` and `udp_recvmsg`/`udpv6_recvmsg`
  eBPF probes discover UDP flows (DNS, StatsD, syslog, ...) and track their
  throughput. Connections carry a `protocol` field end-to-end and UDP edges
  render dashed with a UDP badge in the UI.
- **Agent authentication**: the agent now supports `--api-key` (or the
  `INFRALENS_API_KEY` env var) and sends it as `X-API-Key`, so backend
  `API_KEY` auth finally works with agents. Wired through Helm
  (`backend.auth.apiKey`), Docker Compose (`API_KEY`), and `install-agent.sh`.
- **HTTPS backends**: `--backend` accepts full URLs (e.g.
  `https://infralens.example.com`) for TLS-terminated deployments.
- **Delta-based WebSocket updates**: clients receive one full snapshot on
  connect, then small per-entity delta messages (with a periodic re-sync
  snapshot), replacing the full-topology-every-2s broadcast.
- **Topology search**: header search box (`/` to focus) matching services by
  name, IP, technology, type, or node, with dimming and zoom-to-match.
- **Graph export**: `GET /api/v1/topology/export?format=mermaid|dot` renders
  the live topology as a Mermaid flowchart or Graphviz digraph; both formats
  are available from the UI export menu.

### Changed

- Frontend uses same-origin `/api` URLs and `ws(s)://` derived from the page
  location, so reverse proxies, ingress, and TLS work without configuration
  (`VITE_API_URL`/`VITE_WS_URL` remain as overrides).
- Connections table gained a `protocol` column (migration `000002`, applied
  automatically for SQLite and PostgreSQL).
- Frontend lint is now enforced in CI.

### Fixed

- Topology edges now use the custom edge component, restoring port/throughput
  labels and selection highlighting.
- Docker Compose frontend port mapping (`3000:3000`; nginx listens on 3000).
- Makefile Docker targets now use the same Dockerfiles as CI and releases;
  removed the stale duplicate `docker/` directory.

## [1.0.0] - 2025

Initial stable release: eBPF TCP tracing (IPv4 + IPv6), real-time topology
visualization, throughput monitoring, Kubernetes service discovery,
multi-provider AI documentation, SQLite/PostgreSQL persistence, Prometheus
metrics, and production hardening.
