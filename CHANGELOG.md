# Changelog

All notable changes to InfraLens are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.1] - 2026-08-05

### Fixed

- **WebSocket keepalive crashed the backend process.** Pings were written from a
  separate goroutine while the event loop wrote deltas to the same connection.
  `gorilla/websocket` allows only one concurrent writer and panics otherwise,
  and because the panic happened in a bare goroutine the recovery middleware
  could not contain it — the process exited. Pings now come from the same loop
  that writes everything else.
- **WebSocket ping/pong health checking never worked.** Nothing read from the
  connection, so the pong handler never ran and its read deadline was never
  applied; a departed client was only noticed on the next failed write. A read
  pump now handles pongs, caps inbound frame size, and tears the subscription
  down as soon as the client goes away. All writes carry a deadline so one
  stalled client cannot block the loop.
- `POST /api/v1/inspection` without an `inspection` object nil-dereferenced and
  returned 500; it now returns 400.
- The frontend WebSocket hook scheduled a reconnect from its close handler even
  when the close came from unmount cleanup, leaving a socket reconnecting
  forever after unmount.
- The agent's inspected-PID table grew for the lifetime of the process as PIDs
  churned; expired entries are now swept.

### Changed

- `gofmt` applied across the tree (15 files were unformatted).

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
