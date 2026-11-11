# Changelog

All notable changes to InfraLens are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [3.1.0] - 2026-11-11

InfraLens tells you what changed, and lets you lock the door. v3.0 could
remember the topology but only a webhook ever said when it moved, and anyone
who could reach the UI could read all of it. v3.1 adds an opt-in UI login, a
persisted Changes feed in the UI, a read-only MCP server so AI assistants can
query the topology, an Idle spend report (beta), and DNS names for external
destinations (experimental, off by default).

### Upgrading from 3.0

- **Back up the database first, and do not downgrade except to that
  backup.** v3.1 adds migrations 000004–000007 (SQLite and PostgreSQL).
  Once v3.1 has run, v3.0 refuses to start on the same database (it does not
  know the schema version); with `DB_AUTO_MIGRATE=false` it would start and
  write local-zone timestamps into a database that is now UTC. From v3.1 on,
  a backend refuses to start on a schema newer than it knows in either
  `DB_AUTO_MIGRATE` mode.
- **SQLite timestamps are rewritten to UTC once, at startup.** No action is
  needed. It runs in batches, resumes where it stopped if interrupted, and
  also runs when migrations are applied out of band (`DB_AUTO_MIGRATE=false`).
  With `DB_AUTO_MIGRATE=false` the backend refuses to start on a schema older
  than it needs and names the missing migrations. Large databases add a few
  seconds to the first start.
- **Drift detection is now on whenever history is** (`DRIFT_ENABLED=true` by
  default), with or without a webhook. It starts from the history you
  already have as a known baseline, so upgrading does not announce existing
  dependencies. `DRIFT_ENABLED=false` switches the feed and the webhook off.
- **Nothing changes for authentication unless you set `UI_PASSWORD`.**
  Without it, reads stay public exactly as in v3.0 and the backend logs a
  warning at startup.
- **`/mcp` is mounted by default** (`MCP_ENABLED=true`) but is never open by
  accident: it needs `MCP_API_KEY` or `API_KEY`; with no key it is open only
  under `ALLOW_NO_AUTH=true` without a UI password.
- **AI model defaults changed** and the bundled docker-compose file and Helm
  chart no longer pin models: an empty `*_MODEL` means the backend default
  (see *Changed*). A model you set explicitly is kept — so if your `.env`
  (copied from the old `env.example`) or your own Helm values still carry
  the old pins (`gpt-3.5-turbo`, `claude-3-haiku-20240307`, `gemini-pro`,
  `llama2`), empty them, or pick a current model in the UI.
- **Idle spend needs a little observation first.** Silence is measured only
  while a node's own agent was reporting (agent heartbeats, new in this
  release), so right after an upgrade services on nodes that have no
  heartbeats yet — in particular Kubernetes services known only by resolved
  name — are held back until about `IDLE_COST_QUIET_AFTER` (72h) of
  heartbeats exist.
- Agents and backends of 3.0 and 3.1 interoperate in either direction. An
  agent needs 3.1 only for `--dns-names`.
- **Helm / raw Kubernetes:** the frontend image now takes its upstream from
  `INFRALENS_BACKEND_URL`; the chart and the manifests set it. The raw
  manifests now need an `infralens-api-key` Secret before `kubectl apply -k`
  (the command is in `deploy/k8s/backend-deployment.yaml`).

### Added

- **UI login (opt-in).** Set `UI_PASSWORD` (at least 8 characters, or the
  backend refuses to start; under 12 logs a warning) and the UI shows a
  login screen for a single admin account (`UI_USERNAME`, default `admin`).
  The API becomes deny-by-default: everything except `/health`, `/ready` and
  `/api/v1/auth/*` needs a session or the API key, so endpoints added later
  are protected without being registered anywhere. Agent ingest and
  Prometheus `/metrics` stay API-key-only. Endpoints:
  `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`,
  `GET /api/v1/auth/status`.
  - Sessions are HMAC-signed cookies: HttpOnly, `SameSite=Strict`, `Secure`
    when the request arrived over HTTPS (directly or per
    `X-Forwarded-Proto`). `SESSION_TTL` (default 12h) is an absolute
    lifetime. Set `SESSION_SECRET` (32+ characters) for sessions to survive
    restarts and work across replicas; without it a random per-process key
    is used. Changing the password ends every session.
  - Logout is persisted in the database: it survives restarts, reaches other
    replicas within about 5 seconds, and closes WebSockets bound to that
    session.
  - Cookie-authenticated requests, reads and WebSocket handshakes included,
    must be same-origin or come from an exact `CORS_ORIGINS` entry; `*` and
    wildcard patterns are never trusted with the cookie.
  - Login throttling at three levels: per client (5 attempts, then one per
    20 s), per directly connected peer (60, then one per second) and
    globally (120, then two per second). A client that has signed in before
    keeps only its own bucket, so neither forged forwarding headers nor a
    distributed attack lock out a known admin. Throttled attempts get 429
    with `Retry-After` and are refused even with the right password.
    `TRUSTED_PROXIES` says whose `X-Forwarded-For` to believe (default:
    loopback and private peers, one hop; a CIDR list; or `none`).
  - Helm: `backend.auth.uiPassword`, `uiUsername`, `sessionSecret`,
    `sessionTTL`, `trustedProxies`, or `existingSecret` to read the
    credentials from a Secret of your own.
- **Changes feed.** Every drift event is persisted (kept for
  `HISTORY_RETENTION`) and shown in a Changes panel with an unread badge,
  filters, click-to-focus on the map and a jump to that moment in time
  travel. New events are pushed live over the WebSocket (`drift` messages,
  at most 50 events each; larger passes are marked truncated). API:
  `GET /api/v1/drift/events` (`since`/`until` exclusive, `kind`,
  `external`, `limit` up to 500, `cursor`) and `GET /api/v1/drift/summary`.
  The webhook payload is unchanged.
- **MCP server for AI assistants** at `/mcp` (Streamable HTTP, served by
  the backend and routed by the frontend's nginx and the Helm ingress).
  Read-only, 13 tools: `topology_overview`, `search_services`,
  `get_service`, `get_dependencies`, `get_blast_radius`,
  `list_connections`, `diff_topology`, `rank_criticality`, `find_orphans`,
  `find_stale_services`, `export_mermaid`, `list_drift_events` (only while
  drift detection is on) and `get_idle_spend`. Speaks MCP 2026-07-28,
  2025-11-25 and 2025-06-18, with no SDK dependency. `MCP_API_KEY` is a
  read-only key valid on `/mcp` only — give it to AI clients instead of the
  agents' `API_KEY`, which `/mcp` also accepts. Session cookies never
  authenticate `/mcp`. A service's process fingerprint is returned only to
  key-authenticated callers; command lines and environment never are.
  `MCP_ENABLED`, `MCP_ALLOWED_ORIGINS`; Helm `backend.mcp.*`.
- **Idle spend (beta).** A panel and `GET /api/v1/graph/idle-cost`
  (`format=json|markdown|csv`, `assume_node_monthly` for a labelled what-if,
  `limit`) listing cleanup candidates — services nothing talks to — with
  the network evidence for each and an approximate share of their node's
  price. Candidates to verify with their owners, not deletion
  recommendations.
  - Categories: no connections at all, no inbound, inbound stopped, reached
    only by other idle services, not seen at all recently.
  - Silence counts only while that node's own agent was reporting. Anything
    still carrying traffic is never a candidate, and the figures for a node
    never add up to more than its price. High idle confidence needs more
    than 31 observed days, so with the default 30-day retention the best is
    medium.
  - Held back, with the reason shown: outbound-only workers and cron jobs,
    monitoring, possible replicas and standbys, UDP services, servers
    reached by address, new services, InfraLens itself, your excludes, and
    everything but true orphans when history is off.
  - No built-in prices: `IDLE_COST_NODE_MONTHLY`, `IDLE_COST_NODE_PRICES`
    and `IDLE_COST_SERVICE_PRICES` (glob=price; a service price is carved
    out of its node's), `IDLE_COST_CURRENCY` (a label, default USD),
    `IDLE_COST_EXCLUDE`, `IDLE_COST_WINDOW` (30d), `IDLE_COST_QUIET_AFTER`
    (72h), `IDLE_COST_WARMUP` (24h). Durations accept a `d` suffix;
    malformed values are ignored with a warning shown in every report.
    Reports are cached for 60 s. Helm `backend.idleCost.*`.
  - Agents started with `--cluster` (Helm's default
    `agent.clusterName: "Kubernetes"`) report every node under one name, so
    only a low-confidence cluster-wide split is possible there. Silence
    counts only while every member agent is reporting; a member silent for
    more than an hour (scale-in, a replaced or crashed node) stops holding
    the group back, and if any member was out like that (or flapping) at
    any time within the evidence window, even if it is reporting again, the
    group's candidates are low confidence, left out of the total
    (`not_in_total_reason: "silent_agent"`) and listed under a note naming
    those agents (`silent_agents`).
    Set `agent.clusterName: ""` for per-node attribution.
- **DNS names for external destinations (experimental, off by default).**
  The agent can label external destinations with the name the workload
  resolved (`api.stripe.com` instead of an IP): `--dns-names` /
  `INFRALENS_DNS_NAMES` (Helm `agent.dnsNames`), tuned with
  `--dns-max-pps` (5000), `--dns-cache-size` (32768) and
  `--dns-stats-interval` (5m; Helm `agent.dns.*`). It passively reads DNS
  over UDP with an AF_PACKET socket and a classic-BPF filter — userspace
  only, no eBPF changes, no reverse lookups, needs `CAP_NET_RAW` — and
  believes an answer only if it matches a query the same local workload
  sent (address, port, server, transaction ID, question) on its own
  interface within 5 s. Names are reported only for destinations that
  workload then connected to. The backend keeps up to 8 names per
  connection, drops names unseen for 7 days, and uses them in the map,
  search, exports, AI context, Changes feed, webhook, MCP and Idle spend.
  Service IDs do not change. See *Known limitations*.
- Demo mode tells one story across the new features: 14 days of history, a
  seeded Changes feed, scripted live changes, idle candidates and named SaaS
  dependencies. It uses its own database file, `infralens-demo.db`, unless
  `DB_DSN` is set, so trying the demo never leaves fake history in a real
  database.
- `GET /api/v1/graph/criticality` returns `hostname` for external services,
  like `/topology`.
- `scripts/demo-gif`: a scripted, reproducible recorder for the README GIF.
- Agent heartbeats: the backend records when each agent was reporting, per
  real node and `--cluster` group (migration 000007), which is what Idle
  spend and drift use to tell "quiet" from "not watched". A group counts as
  watched only while every current member agent reports: a member is current
  from its first heartbeat until it has been silent for an hour, except while
  it has reported for less than half of the last two hours (a crash loop). A
  group with no member heartbeats yet is not watched. A database whose
  `node_intervals` table was created by a pre-release build is refused at
  startup with recovery steps. The drift status
  (`/api/v1/drift/summary`, `detector.silent_agents`) lists agents that
  stopped reporting recently.

### Changed

- **AI documentation.** Defaults are OpenAI `gpt-6-luna`, Anthropic
  `claude-haiku-5-5` and Gemini `gemini-3.5-flash-lite`; Ollama and LM
  Studio use the first installed or loaded model. Model lists are read live
  from each provider (cached 15 minutes, static fallback). Requests follow
  the current provider APIs (low reasoning effort with output headroom). A
  model the provider rejects returns HTTP 422 `model_unavailable` with
  suggestions; a refused, blocked or empty answer returns HTTP 502
  `response_refused` and is never cached. `POST /api/v1/ai/config` keeps
  fields you leave out and accepts `"clear": [...]` (Remove key / Reset to
  default in the UI).
- **UI.** The map is laid out in flow order (callers left, external
  services in their own group on the right) and stays put on live updates.
  Edge labels appear for the hovered or selected service. Secondary panels
  (Changes, Idle spend, Most critical, Orphans, Decommission, Compare) open
  one at a time from a left rail. Blast radius stays on the map until
  cleared. A banner marks historical views. PNG export renders at 1:1.
- Drift "gone" is measured in observed time: an outage of collection — the
  whole backend or the agent of the node a dependency belongs to (the node of
  its process, else the node recorded for its source, else its target, so
  also for Kubernetes-named services) — does not count towards
  `DRIFT_GONE_AFTER`; only dependencies with no node at either end are
  measured against the whole installation. In a `--cluster` group only the
  first hour of a member's silence does not count. A node whose agent stays
  silent for three times `DRIFT_GONE_AFTER` (at least 6h) while the rest
  keeps reporting is treated as retired, and its dependencies are then
  reported gone once. The detector's memory grows with distinct dependencies
  and nodes, not with history intervals. Detection resumes from a checkpoint
  after a restart, replicas sharing a database do not notify the same change
  twice, and failed feed writes are retried. A
  dependency reported gone that later returns — also one of a retired node
  that comes back — is re-armed silently, without a "reappeared" event.
- The frontend's nginx forwards `X-Forwarded-Proto` and `X-Forwarded-Host`
  and routes `/mcp` to the backend. The Vite dev proxy keeps the browser's
  `Host` header (needed for the WebSocket same-origin check under login).
- Helm drift timings (`backend.drift.checkInterval`, `goneAfter`, `warmup`)
  apply without a webhook and default to empty, so the backend defaults
  apply. Explicit zeros in Helm values are passed through rather than
  dropped.

### Fixed

- **Time travel, diff and blast radius on non-UTC hosts.** SQLite stored
  times in the host's local zone and compared them as text, so results were
  wrong whenever the server was not on UTC, across DST changes, after a
  host's time zone changed, and at exact instants. All times are now stored
  in UTC (existing rows are converted once at startup). PostgreSQL results
  are returned in UTC too.
- Endpoints of long-lived connections no longer age out of the live
  topology while traffic flows over them.
- Mermaid export escapes line breaks and control characters in labels, so a
  service name can no longer inject Mermaid syntax.
- **Helm and raw Kubernetes.** The frontend could not start under either:
  its nginx proxied to `http://backend:8080`, a host that exists only under
  docker-compose. It now uses `INFRALENS_BACKEND_URL` (default
  `http://backend:8080`), set by the chart and the manifests. Agents on
  `hostNetwork` use `dnsPolicy: ClusterFirstWithHostNet`, so they can
  resolve the backend Service. Helm rolls the agents as well as the backend
  when the chart's Secret changes, so rotating `auth.apiKey` no longer
  leaves agents sending the old key. The raw manifests are fixed: frontend
  port, an ingress rewrite that broke every `/api` call, an agent flag that
  does not exist, the agent's backend URL, and a required API key Secret.

### Security

- UI login, the separate read-only `MCP_API_KEY`, and the cookie
  same-origin policy above.
- The Gemini API key is sent in a header rather than the URL. Cloud AI
  clients no longer follow redirects. Upstream error text is redacted
  (including newer OpenAI key formats) and size-capped.

### Known limitations

- **DNS names are experimental and have not yet been run on a real cluster
  by the maintainers.** CI runs the capture path's integration test as root
  on a Linux runner. Before enabling it in production, run that test on a
  representative node
  (`go test -c -o dnscache.test ./agent/dnscache && sudo ./dnscache.test -test.run TestCaptureIntegration -test.v`),
  then enable it on one canary node with `--dns-stats-interval=1m`, make a
  lookup and a connection from a pod, and check that the agent logs
  `learned>0` and `kernel_drops=0` and that the destination's `hostname`
  appears in `GET /api/v1/topology`.
- DNS names are display labels, not verified facts: whoever answers a
  workload's DNS (its resolver, or anyone on the path) decides its labels,
  and on bridge-based CNIs a pod can spoof a query for a neighbour on the
  same node. Not visible: DNS over HTTPS/TLS, DNS over TCP, `/etc/hosts`,
  lookups made before the agent started, and on dual-stack pods,
  connections made over IPv6 when the lookup itself went over IPv4 (the
  usual case: the answer is filed under the pod's IPv4 address). Cilium
  with eBPF host routing may bypass the point where the agent captures DNS.
- Idle spend cannot see a server reached through a differently named
  Kubernetes Service, hostPort/NodePort traffic to the node's IP,
  receive-only UDP, or jobs that run less often than the history retention.
- Kubernetes-named services with replicas on several nodes are attributed
  to the node last recorded on the source's service row. If only one
  replica's node uses a dependency and that node's agent stops, the absence
  is measured against the other node, and the Changes feed can report the
  dependency gone after `DRIFT_GONE_AFTER`.
- A `--cluster` member that reports only part of the time (between half and
  almost all of it) is still counted as current: its gaps slow Idle spend
  and drift detection in its group without the agent being listed as
  silent.
- The PostgreSQL code added in 3.1 runs in the CI PostgreSQL job but was not
  exercised against PostgreSQL during local development. Likewise the
  frontend image's new nginx template was reviewed and its Helm rendering
  checked, but the image was not run in a container during development.
- With several backend replicas, live Changes pushes come only from the
  replica that recorded them; the other replicas' UIs catch up on their next
  poll (30 s with the Changes panel open, 60 s for the unread badge).
- One admin account; no OIDC or roles.
- The decommission list shows stale external destinations by IP, not by DNS
  name.
- The Changes feed has no "reappeared" marker for a dependency that comes
  back after being reported gone.
- Real per-connection latency is still not shipped.

## [3.0.0] - 2026-11-04

InfraLens remembers. Until now it could only answer "what is talking to what
right now" — current state was hard-deleted after 30 minutes. v3 records
topology history and builds on it: time travel, diffs, drift notifications,
blast radius, and cleanup candidates.

### Upgrading from 2.x

- **Mount a volume at `/data` if you want history to survive restarts.**
  Current state is repopulated by agents within seconds; history cannot be
  re-derived. Helm: `--set backend.persistence.enabled=true` (off by default
  to keep the one-command quick start; turning it on also switches the
  Deployment to the `Recreate` strategy). docker-compose and the raw k8s
  manifests mount one by default.
- **History is on by default** (`HISTORY_ENABLED=true`, 30-day
  `HISTORY_RETENTION`). Recording costs roughly 30–40% more backend time per
  ingested event; set `HISTORY_ENABLED=false` to opt out.
- **The new read endpoints are public**, like `/api/v1/topology` already was,
  and the history endpoints expose up to `HISTORY_RETENTION` of past topology.
  The UI has no login flow yet, so on an untrusted network put InfraLens
  behind an authenticating reverse proxy. UI authentication is the first item
  after this release.

### Added

- **Topology history.** Services and connections are recorded as intervals
  (`[first_seen, last_seen]`, extended in place while observed), so write
  volume tracks how often the architecture changes rather than how often
  agents report — which is what keeps this viable on SQLite. Configured with
  `HISTORY_ENABLED`, `HISTORY_RETENTION`, `HISTORY_MAX_GAP`; retention is
  independent of `PRUNE_MAX_AGE`.
- **Time travel.** `GET /api/v1/topology?at=<RFC3339>` reconstructs the graph
  at any instant in the retention window; a timeline scrubber in the UI drives
  it. Blast radius and graph export follow the instant being viewed.
- **Architecture drift notifications.** Set `DRIFT_WEBHOOK_URL` and InfraLens
  POSTs when a dependency appears that has never been seen before (flagging
  ones that reach the public internet) or an established one stops being
  observed for `DRIFT_GONE_AFTER` (default 24h). The payload has a
  Slack-compatible `text` field plus structured `appeared`/`disappeared`
  lists. Built to stay quiet: a warmup window (`DRIFT_WARMUP`, 1h) on fresh
  installs, no re-announcing after restarts, no "everything vanished" when
  agents stop reporting, and inbound internet clients are ignored as churn.
  Helm: `backend.drift.webhookUrl` (stored in the chart's Secret).
- **Topology diff** — `GET /api/v1/topology/history/diff?from=&to=` and a diff
  panel: what appeared and disappeared between two instants.
- **Blast radius** — `GET /api/v1/graph/impact?service=<id>&direction=upstream|downstream&depth=`:
  what transitively breaks if a service goes down, or what it depends on.
  Highlighted in the graph from the service drawer.
- **Criticality ranking** — `GET /api/v1/graph/criticality`: services ranked by
  upstream blast radius, the riskiest single points of failure.
- **Orphan detection** — `GET /api/v1/graph/orphans`: services with no
  connections at all.
- **Decommission candidates** — `GET /api/v1/topology/history/stale`: services
  not observed for a while (default 7 days, or half the retention when that is
  shorter).
- `GET /api/v1/topology/history/range` — the time span history covers.
- Helm `backend.extraEnv`, so new backend settings don't need a chart release.
- CI runs the Postgres storage suite and lints new backend code with
  golangci-lint.

### Changed

- The backend image's working directory is `/data`, owned by the nonroot
  user. The default `DB_DSN` (`infralens.db`) previously resolved to an
  unwritable `/infralens.db`, so the backend failed at startup unless
  `DB_DSN` was overridden.
- Responses reconstructed from history (`?at=`, diffs) omit `healthy` rather
  than claiming every past service was healthy — intervals don't record
  health.

### Fixed

- Long-lived connections (database pools, gRPC channels, Kafka consumers)
  are kept alive in history by their throughput reports, not only by
  connect/accept events. Previously history showed such a dependency, and
  often both of its endpoints, as gone a few minutes after it was opened.
- Connections whose endpoint services had already been pruned ("phantom
  endpoints") no longer appear in blast radius, criticality, orphans, or
  historical topology, so results never reference nodes they don't contain.
- History writes stay one interval per continuous observation under
  concurrent ingestion, and `?at=now` includes currently live entities.
- The timeline scrubber and history panels no longer stay hidden forever on a
  fresh install, the graph re-lays out when switching between live and
  historical views, and the blast-radius highlight can be cleared.

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
