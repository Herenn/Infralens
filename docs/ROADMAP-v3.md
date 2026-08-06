# InfraLens v3.0 — Roadmap

**Target: September 1st.** Long-lived work only. Security fixes and bug
patches ship independently as 2.2 / 2.3 / 2.4 / … from `main` and are merged
*into* this branch periodically — never the other way around until v3 lands.

---

## Positioning: not "Coroot+"

This has to be settled before scoping features, because it changes what the
same four features are *for*.

Coroot is a mature, well-resourced adjacent project: 7.9k GitHub stars, 390
forks, 1,416 commits. It ships eBPF service maps, metrics, logs, distributed
tracing, continuous CPU/memory profiling, SLO-based health dashboards, cloud
cost monitoring, and AI root-cause analysis — and it requires Prometheus plus
ClickHouse to run it. InfraLens today has none of logs, traces, or profiling;
it does TCP connection tracing.

If v3 becomes "history + alerting + AI RCA," that is not catching up to
Coroot — it's rebuilding a third of what they already ship, months behind,
against a project with an enterprise tier and a dedicated AI-RCA product
page. That fight is close to unwinnable and doesn't need to be fought.

**What Coroot's own materials never mention: architecture documentation or
explaining what a service is.** Coroot answers "is this healthy, and if not,
why." It does not answer "what does this thing do, and why does it exist."
InfraLens already has the seed of that answer and Coroot doesn't — the AI
docs feature reads a service's README, Dockerfile, and entry-point source and
explains it in plain English from live, auto-discovered topology. Nobody
adjacent is doing that.

**Working identity: the living architecture doc for your infra** — onboarding,
institutional knowledge, drift awareness. Not a Datadog/Coroot alternative.
Same eBPF, zero-instrumentation foundation; different job.

This reframes the four v3 features below. Same engineering work in most
cases, different design target:

| Feature | As "Coroot+" (loses) | As architecture understanding (different game) |
|---|---|---|
| History | SLA/uptime history — Coroot's exact turf, needs ClickHouse-grade infra | "What did my architecture look like last week" — lighter, fits single-binary/SQLite |
| Change detection | Incident/SLA-breach alerting — competing on uptime trust incumbents already own | Architecture drift: "a new external dependency appeared," "this stopped talking to Redis" |
| Auth/RBAC | Table stakes either way | Table stakes either way |
| Latency (v3) / L7 (v4) | "Which endpoint is slow" — APM territory | "What does this service actually talk to and do" — feeds the docs |

The single-binary, SQLite-first, try-it-in-30-seconds posture stops being a
limitation under this framing and becomes the point: a tool an individual
engineer or small team points at their own infra to *understand* it, not a
platform an ops team stands up to *watch* it.

---

## Will anyone actually use this? (the honest risk)

Worth writing down, not just deciding by momentum, because it should drive
engineering priority inside v3, not just marketing copy.

**The real risk isn't that the idea is bad — it's that documentation tools
have a weak habitual-use track record.** Monitoring tools get opened during
incidents: frequent, urgent, sticky. A tool people open once during
onboarding and never again doesn't get maintained, doesn't get issues filed
against it, and stalls as an open-source project regardless of how good the
underlying idea is. Manually-maintained architecture diagrams (C4 model
tooling, hand-drawn wiki diagrams) have failed for exactly this reason for
years — not because diagrams are unwanted, but because nothing forces anyone
to keep opening the tool that draws them. The one adjacent success story,
Spotify's Backstage, largely won through top-down platform-engineering
mandates at large orgs, not organic pull — a distribution path this project
doesn't have.

**This means change detection (v3 feature #2) is not a peer feature — it's
the one that determines whether this succeeds.** A live diagram that's always
accurate but requires you to remember to look at it will get bookmarked and
forgotten. A diagram that *pings you* — "a new external dependency appeared,"
"this service stopped talking to what it used to" — creates the same
recurring, reactive engagement loop that makes monitoring tools sticky,
without requiring InfraLens to compete on uptime/incident trust. If v3 ships
history and auth on schedule but change-detection slips, the release still
looks complete on a roadmap and still risks being a tool people install once
and never reopen. Prioritize accordingly if the schedule gets tight.

**Where the pull is real, concretely — design around these moments rather
than "general purpose docs":**
- **Onboarding.** A new hire's first days are the sharpest, most recurring
  version of "what does this actually do" — recurring across any growing
  team, not a one-off.
- **Incident postmortems.** "Did we know this dependency existed?" is a
  question every postmortem asks; a topology history answers it directly.
- **Compliance/audit.** SOC2 and similar audits are painful and infrequent,
  but teams remember whatever made the last one faster.

None of this guarantees adoption. But the downside of trying is low — v3 is
incremental on the eBPF/topology core that already exists either way — and
the upside, if the change-detection hook lands well, is a real and largely
uncontested niche rather than a losing race against a better-funded
incumbent. "Worth it" here doesn't require beating Coroot; it requires real
teams reaching for it at these three moments and the project staying alive
past launch week.

---

## Where v2.1 actually stands

Worth stating plainly, because it determines what v3 should be.

InfraLens today is a **live viewer**. It answers "what is talking to what,
right now" extremely well, with zero instrumentation, on any cluster, without
requiring a particular CNI or a service mesh. That combination is genuinely
rare and it is why the project gets attention.

What it cannot do:

| Question a user will ask | Can v2.1 answer it? |
|---|---|
| What is talking to what right now? | Yes — this is the core strength |
| What did this look like an hour ago? | **No.** Pruned and deleted after 30 min |
| Did a new external dependency appear this week? | **No.** No history to compare against |
| *What* is service A asking service B for? | **No.** L3/L4 only — an IP and a port |
| How slow is this dependency? | **No.** `latency_ms` exists end-to-end but nothing populates it |
| Who is allowed to see this topology? | **No.** Read endpoints are open by design |

Three of those six are variations on the same root cause: **there is no
history**. `PRUNE_MAX_AGE` defaults to 30 minutes and pruning is a hard
`DELETE`. There are three tables — `services`, `connections`, `node_metrics` —
each holding only current state.

That is the single highest-leverage thing to change, because change detection,
trend analysis, alerting, and "show me the topology at time T" are all
downstream of it.

---

## Recommended v3.0 scope

**Theme: InfraLens remembers — and explains.**

Three weeks and change is not enough time to do historical storage *and* L7
protocol parsing *and* an auth system properly. Attempting all three produces
three half-features. The recommendation is to pick the one that unlocks the
most downstream capability, ship it well, and let the flashier one have its own
release cycle.

### 1. Historical topology (the spine)

Store topology over time instead of overwriting current state.

- Time-bucketed connection/service observations rather than a single mutable
  row per entity, with rollups (1m → 5m → 1h) so retention is bounded without
  losing shape.
- Retention policy that is a *policy*, not a `DELETE` — configurable window,
  downsampling rather than deletion.
- `GET /api/v1/topology?at=<timestamp>` and `?from=&to=`.
- A timeline scrubber in the UI. Dragging it re-renders the graph at that
  moment. This is the demo that sells the release.

Framed as architecture history, not SLA history: the goal is "what did the
diagram look like," not high-frequency metric time series. That keeps this
buildable on SQLite rather than requiring ClickHouse-grade infrastructure —
which is also a real product difference from Coroot's stack, not just a
convenience.

This is mostly backend, schema, and UI work — all domains this codebase
already handles well, which is exactly why it fits the window.

**Watch out for:** write amplification. The agent reports every second per
node; naive per-sample rows will not survive a real cluster. Design the
bucketing before writing the migration, not after.

### 2. Change detection & alerting — the feature that determines adoption

Once there is history, the valuable questions become answerable:

- A service started talking to the public internet.
- A new dependency edge appeared that has never been seen before.
- An expected dependency disappeared.
- Traffic to a dependency changed by more than N%.

Delivered as a rules engine plus webhook/Slack output. Framed as *architecture
drift*, not incident/threshold alerting — "something changed that you should
know about," not "something is down." That's a smaller, calmer scope than
SLA-breach alerting, and it doesn't require competing on the uptime-critical
trust that incumbents already own.

This is also, per the adoption discussion above, the single highest-priority
item if the schedule gets tight. It's what turns InfraLens from a page that
gets bookmarked once into one that pings people back.

### 3. Authentication, sessions, RBAC

Already earmarked as "future version" — this is that version.

Read endpoints are currently unauthenticated *by design*, because the shipped
frontend has no login flow and cannot send a credential. That is documented as
a known limitation in v2.1 and it is the single biggest blocker to anyone
running this on a real company network.

- Login, sessions, and a credential the frontend actually sends.
- Per-namespace / per-cluster visibility scoping, so not everyone sees
  everything.
- Remove the read endpoints from the auth skip list once the UI can
  authenticate — this is the change that closes the v2.1 known limitation.

### 4. Real latency measurement (small, high visible value)

`latency_ms` is already threaded through storage, the API response, and the
frontend types. **Nothing anywhere populates it.** It is a dead field that
always returns zero.

eBPF can read smoothed RTT directly off the socket (`tcp_sock->srtt_us`) at
the points where the agent already has the socket in hand. The plumbing exists;
this is the cheapest real capability on the list.

Framed as a dependency-health signal for the documentation ("this dependency
is typically fast/slow"), not an APM percentile dashboard — same data, but
the UI and narrative should support understanding a service, not paging
someone.

*(If v3 slips, this one can be pulled forward into a 2.x — it needs no schema
change and no API change.)*

---

## Deliberately deferred

### L7 protocol visibility — recommend v4, spike now

HTTP paths and status codes, gRPC methods, SQL statements, Redis commands,
Kafka topics. Turning "service A → service B on 5432" into "service A runs
`SELECT` against the `users` table."

This is the biggest single capability jump available and the strongest
differentiator. Under the architecture-understanding framing it's especially
valuable: it feeds the AI documentation a real API surface to describe
instead of a port number, which is a docs improvement, not just a monitoring
one.

It is deferred **because it is the riskiest item, not the least valuable one**:

- Plaintext parsing needs new probes and per-protocol parsers.
- Encrypted traffic needs uprobes on TLS libraries — and that means OpenSSL
  version variance, statically linked Go binaries with no dynamic symbols,
  musl vs glibc, and BoringSSL. This is where projects of this kind lose
  months.
- Getting it *half* right is worse than not shipping it, because wrong L7 data
  looks authoritative.

**Suggested approach:** start a timeboxed spike on this branch in parallel —
plaintext HTTP only, one protocol, one kernel version. If it comes together
faster than expected it joins v3. If it does not, it becomes v4's headline
with real prototype knowledge behind it instead of a guess.

### Also deferred

- **Multi-cluster federation.** `cluster_name` exists as a grouping string
  today; real federation (cross-cluster topology, per-cluster drill-down) is a
  v4 concern once history and auth exist to build on.
- **OpenTelemetry / Prometheus service-graph export.** Cheap and good for
  adoption — makes InfraLens a data source rather than a silo. Reasonable to
  slot into a 2.x rather than holding for v3.
- **Topology-as-code drift detection.** Declare expected topology, fail CI on
  drift. Novel and security-relevant, but it depends on history and change
  detection landing first.

---

## Branch discipline

This branch will live for roughly a month while 2.x releases continue from
`main`. Two things follow from that:

1. **Merge `main` into `features` regularly** — at minimum after every 2.x
   release. A month of drift on a branch touching storage schema and the auth
   middleware will not merge cleanly if left alone.
2. **Nothing security- or bug-related lands here first.** Those go to `main`,
   ship in a 2.x, and arrive here via merge. If a fix is made here first it is
   invisible to users until September.

---

## Open questions

- **Retention target** — how much history is the product promising? 7 days
  changes the storage design very differently from 90 days.
- **Does SQLite remain a supported backend for historical data,** or does
  history require Postgres? This affects whether the demo path still works
  single-binary, and whether "lighter than Coroot's ClickHouse+Prometheus
  stack" stays true.
- **Is alerting in-product, or does it delegate** to Prometheus/Alertmanager
  via exported metrics? Delegating is far less code and integrates with what
  people already run — but the change-detection notifications (new
  dependency, disappeared dependency) are architecture events, not metrics,
  so they likely need to stay in-product regardless of this answer.
- **What's the first-week experience for the three wedge moments**
  (onboarding, postmortem, audit)? Worth prototyping the UI for these
  specifically rather than a generic "browse history" screen.
