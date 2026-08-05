# Changelog

All notable changes to InfraLens are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.1.0] - 2026-08-05

Security release. Three of these changes will stop
an existing deployment from starting or working until it is reconfigured, so read
**Breaking changes** below before upgrading.

### Security

- **The agent no longer installs updates it cannot verify.** It previously read
  a download location out of the backend's version response and installed
  whatever it found there, with no signature or checksum, over its own
  executable. Since a bare `--backend=host:port` is contacted over plain HTTP,
  anyone able to answer that request could run code as root on every node — the
  agent runs privileged with `hostPID`. Downloads now come only from a pinned
  HTTPS release URL, the reported version must parse as a version before it is
  used in a URL, and a published SHA-256 digest is verified before installation.
  Releases now publish a `.sha256` alongside each agent binary.
- **The AI configuration endpoint no longer accepts local-LLM URLs.**
  `ollama_url` and `lmstudio_url` named hosts the backend would then send
  requests to and surface the responses from, which made the endpoint a
  server-side request forgery primitive against cloud metadata and internal
  services. Those endpoints are environment configuration now
  (`OLLAMA_URL`, `LMSTUDIO_URL`).
- **The backend refuses to start with no API key** unless `ALLOW_NO_AUTH=true`
  is set. Previously an unset `API_KEY` silently disabled authentication on
  every ingest and AI endpoint.
- **AI documentation output is HTML-escaped before rendering.** It is injected
  as HTML, and its content derives from agent-collected data (process names,
  command lines, README and Dockerfile contents), so anyone able to influence a
  monitored workload could get script into an operator's browser.
- **Request bodies are capped** (`MAX_REQUEST_BYTES`, default 10 MiB). Handlers
  decoded JSON off the request body with no limit, so one request could exhaust
  memory.
- **Install scripts verify the Go toolchain download** against the published
  SHA-256 instead of extracting whatever was served.
- **The authentication skip list is matched exactly** instead of by prefix.
  A public path such as `/api/v1/topology` previously exempted anything routed
  beneath it, so a future endpoint could lose authentication purely by virtue
  of where it was mounted. Sub-path access for `/api/v1/services/{id}` is now
  an explicitly declared prefix.
- **The authentication check now cleans the request path before matching it**
  against that skip list. `net/url.Parse` does not collapse `.`/`..`
  segments, so a request for `/api/v1/services/../events` arrives with that
  literal, uncleaned path; matching it against the skip list without
  normalizing first would treat it as starting with the exempted
  `/api/v1/services/` prefix and let it through to a protected route with no
  credential. The router's own default behavior happened to intercept this
  before any handler ran, but that was incidental to a downstream library's
  default, not something this middleware guaranteed on its own - verified
  live against a real binary with a raw, unencoded request, both before and
  after the fix.
- **A live third-party API key could leak into logs and into the caller's own
  response** on a plain network failure talking to Gemini - not an auth
  failure, any transport-level failure (refused connection, DNS failure,
  timeout). Gemini's key is sent as a URL query parameter, and Go's HTTP
  client embeds the full request URL in the error it returns when a request
  never gets a response; that error was logged and returned to whoever called
  the AI documentation endpoint verbatim. Reproduced live: a failed request
  against an unreachable address rendered the operator's (fake, test) key
  directly in the resulting error string. Every provider's request path now
  strips the URL from a transport-level error before it is used, whether or
  not that provider currently puts anything sensitive in its URL.
- **Source code sent to AI providers is scanned for hardcoded credentials
  before it leaves the prompt.** The README, Dockerfile, and entry-point file
  content the agent collects (for the AI documentation feature) were inserted
  into the LLM prompt verbatim, with no redaction, and by default that prompt
  goes to a cloud provider (OpenAI/Anthropic/Gemini) rather than a local one.
  Hardcoded credentials in an entry-point file or Dockerfile `ENV`/`ARG` line —
  a common real-world occurrence — would have been sent to that third party.
  Recognisable secret shapes (cloud provider keys, private key blocks, and
  generic `password=`/`token=`/`secret=`-style assignments) are now redacted
  first. This is a best-effort net on top of the agent's existing behavior of
  only ever collecting environment variable *names*, never values.
- **The database DSN is no longer logged in cleartext at startup.** A Postgres
  DSN commonly carries its password directly
  (`postgres://user:pass@host/db`), and the startup log line printed
  `cfg.Storage.DSN` verbatim regardless of driver — putting the database
  password in every log aggregator that ingested the process's output. The
  password is now masked before logging; SQLite's file-path DSN is unaffected.

### Fixed

- **Inbound connections never showed any throughput.** The agent reports each
  sample from the local socket's point of view, so for a connection this host
  accepted, the sample reads server -> client on the client's ephemeral port,
  while the accept event recorded client -> server on the listening port. The
  two never matched and every inbound update silently applied to no rows.
  Samples are now resolved against both directions.
- **Throughput samples sharing a topology edge overwrote each other.** Every
  client on a listening port collapses onto one inbound edge, and the update
  writes absolute values, so the edge showed one arbitrary client's traffic
  rather than the total. Samples are now summed per edge before being written.

- A data race on the LLM provider map crashed the whole backend. `POST
  /api/v1/ai/config` rebuilt the map while other requests read it, and
  concurrent map access is a Go runtime fatal error, not a panic — the recovery
  middleware could not contain it. All access is now mutex-guarded and the
  provider set is swapped atomically.
- Agent auto-update was silently dead whenever authentication was enabled: the
  version check never sent the API key, so it received 401 and failed with the
  error logged only at debug level. The key is sent now, and an auth rejection
  is reported clearly.
- CORS no longer advertises `Access-Control-Allow-Origin: *` together with
  `Access-Control-Allow-Credentials: true`. Browsers reject that pair, so
  credentialed cross-origin requests failed silently; credentials are now
  dropped for a wildcard origin, with a warning.

### Breaking changes

- **`API_KEY` is now required.** Set it, or set `ALLOW_NO_AUTH=true` to keep the
  old behaviour deliberately. The demo Compose file sets `ALLOW_NO_AUTH=true`;
  Helm exposes `backend.auth.allowNoAuth`.
- **`ollama_url` / `lmstudio_url` are rejected in `POST /api/v1/ai/config`.**
  Move them to the `OLLAMA_URL` / `LMSTUDIO_URL` environment variables.
- **`CORS_CREDENTIALS` now defaults to `false`.** With the default wildcard
  origin it never worked anyway; set explicit `CORS_ORIGINS` to use credentials.
- Agents older than 2.1.0 will not self-update to 2.1.0 and later releases
  without the published checksum being reachable; upgrade them with the install
  script if their update check reports a verification failure.

### Known limitations (not changed in this release)

- **Read endpoints, including the full topology (`/api/v1/topology`,
  `/api/v1/services`) and the WebSocket feed, are unauthenticated by design**
  even with `API_KEY` set — they are on the auth skip list deliberately,
  because the shipped frontend has no login flow and never sends a credential.
  Anyone with network access to the backend can read service names, IPs,
  Kubernetes namespaces/pod names, and node metrics with no credential. Put
  the backend behind an authenticating reverse proxy or network policy if that
  exposure is not acceptable; this was true before 2.1.0 as well and is not a
  new regression, just not yet fixed.

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
