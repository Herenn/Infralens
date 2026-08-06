# InfraLens v3.0 — Roadmap

**Target: September 1st.** Long-lived work only. Security fixes and bug
patches ship independently as 2.2 / 2.3 / 2.4 / … from `main` and are merged
*into* this branch periodically — never the other way around until v3 lands.

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

**Theme: InfraLens remembers.**

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

This is mostly backend, schema, and UI work — all domains this codebase
already handles well, which is exactly why it fits the window.

**Watch out for:** write amplification. The agent reports every second per
node; naive per-sample rows will not survive a real cluster. Design the
bucketing before writing the migration, not after.

### 2. Change detection & alerting (what history makes possible)

Once there is history, the valuable questions become answerable:

- A service started talking to the public internet.
- A new dependency edge appeared that has never been seen before.
- An expected dependency disappeared.
- Traffic to a dependency changed by more than N%.

Delivered as a rules engine plus webhook/Slack output. This is where the
security and compliance value lives, and it is the reason someone keeps
InfraLens open rather than looking at it once.

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

*(If v3 slips, this one can be pulled forward into a 2.x — it needs no schema
change and no API change.)*

---

## Deliberately deferred

### L7 protocol visibility — recommend v4, spike now

HTTP paths and status codes, gRPC methods, SQL statements, Redis commands,
Kafka topics. Turning "service A → service B on 5432" into "service A runs
`SELECT` against the `users` table."

This is the biggest single capability jump available and the strongest
differentiator. It also dramatically improves the AI documentation, which
would be describing a real API surface instead of a port number.

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
  single-binary.
- **Is alerting in-product, or does it delegate** to Prometheus/Alertmanager
  via exported metrics? Delegating is far less code and integrates with what
  people already run.
