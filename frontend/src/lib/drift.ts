// Architecture drift feed: API access, the "last seen" marker behind the
// unread badge, and the in-page channel live drift events arrive on.

import { DriftEvent, DriftSummary } from '../types'
import { apiUrl } from './api'

// ---------------------------------------------------------------------------
// Live events
// ---------------------------------------------------------------------------

// useWebSocket receives "drift" messages; the Changes UI listens here. A DOM
// event keeps the WebSocket hook from having to know who consumes them.
const LIVE_EVENT = 'infralens:drift'

export function emitDriftEvents(events: DriftEvent[]) {
  window.dispatchEvent(new CustomEvent<DriftEvent[]>(LIVE_EVENT, { detail: events }))
}

export function onDriftEvents(handler: (events: DriftEvent[]) => void): () => void {
  const listener = (e: Event) => handler((e as CustomEvent<DriftEvent[]>).detail)
  window.addEventListener(LIVE_EVENT, listener)
  return () => window.removeEventListener(LIVE_EVENT, listener)
}

// "What you have may be incomplete - refetch": a live message that was
// capped (a large rollout), or a WebSocket that reconnected and may have
// missed messages meanwhile.
const RESYNC_EVENT = 'infralens:drift-resync'

export function emitDriftResync() {
  window.dispatchEvent(new Event(RESYNC_EVENT))
}

export function onDriftResync(handler: () => void): () => void {
  window.addEventListener(RESYNC_EVENT, handler)
  return () => window.removeEventListener(RESYNC_EVENT, handler)
}

// ---------------------------------------------------------------------------
// Last seen
// ---------------------------------------------------------------------------

// The detected_at of the newest event the user has seen, verbatim as the
// server sent it: handing it back as ?since= (exclusive) then counts exactly
// the events that arrived after it.
const LAST_SEEN_KEY = 'infralens.changes.lastSeen'

// With no marker yet (first visit, or storage cleared) only the last day
// counts as new - not the whole retention window as a "99+" badge.
const FIRST_VISIT_LOOKBACK_MS = 24 * 60 * 60 * 1000

export function getLastSeen(): string {
  try {
    const stored = window.localStorage.getItem(LAST_SEEN_KEY)
    if (stored) return stored
    const baseline = new Date(Date.now() - FIRST_VISIT_LOOKBACK_MS).toISOString()
    window.localStorage.setItem(LAST_SEEN_KEY, baseline)
    return baseline
  } catch {
    // Storage disabled: the same lookback, every time.
    return new Date(Date.now() - FIRST_VISIT_LOOKBACK_MS).toISOString()
  }
}

export function setLastSeen(detectedAt: string) {
  try {
    window.localStorage.setItem(LAST_SEEN_KEY, detectedAt)
  } catch {
    // ignore
  }
}

// An RFC 3339 instant as [epoch milliseconds, sub-millisecond nanoseconds].
// Date keeps only milliseconds; the server sends microseconds, and two
// events can share a millisecond.
function instant(iso: string): [number, number] {
  const m = iso.match(/^(.*T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:?\d{2})$/i)
  if (!m) return [new Date(iso).getTime(), 0]
  const frac = (m[2] || '').padEnd(9, '0').slice(0, 9)
  return [Date.parse(m[1] + m[3]) + Number(frac.slice(0, 3)), Number(frac.slice(3))]
}

// Whether instant a is strictly after instant b (null: the beginning).
export function isAfter(a: string, b: string | null): boolean {
  if (!b) return true
  const [am, an] = instant(a)
  const [bm, bn] = instant(b)
  return am !== bm ? am > bm : an > bn
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

export interface DriftFilter {
  kind?: 'appeared' | 'disappeared'
  externalOnly?: boolean
}

export interface DriftPage {
  events: DriftEvent[]
  next_cursor?: string
}

export async function fetchDriftEvents(filter: DriftFilter, cursor?: string, signal?: AbortSignal): Promise<DriftPage> {
  const params = new URLSearchParams({ limit: '50' })
  if (filter.kind) params.set('kind', filter.kind)
  if (filter.externalOnly) params.set('external', 'true')
  if (cursor) params.set('cursor', cursor)
  const res = await fetch(apiUrl(`/api/v1/drift/events?${params}`), { signal })
  if (!res.ok) throw new Error(`Failed to load changes (${res.status})`)
  return res.json()
}

// An HTTP error from the drift API, so callers can tell "the feed is not
// available here" (4xx: history off, not authorized) from a passing failure.
export class DriftApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message)
  }
}

export async function fetchDriftSummary(since?: string | null, signal?: AbortSignal): Promise<DriftSummary> {
  const q = since ? `?since=${encodeURIComponent(since)}` : ''
  const res = await fetch(apiUrl(`/api/v1/drift/summary${q}`), { signal })
  if (!res.ok) throw new DriftApiError(`Failed to load change summary (${res.status})`, res.status)
  return res.json()
}

export function matchesFilter(ev: DriftEvent, filter: DriftFilter): boolean {
  if (filter.kind && ev.kind !== filter.kind) return false
  if (filter.externalOnly && !ev.external) return false
  return true
}

// ---------------------------------------------------------------------------
// Presentation helpers
// ---------------------------------------------------------------------------

// The instant that shows an event's edge on the graph: a second after it was
// first seen (an endpoint may be recorded a moment after the edge itself), or
// the last moment it was seen. Not detected_at: that can be a full check
// interval later, past the history grace window, and a short-lived edge
// would no longer be on the graph you land on.
export function driftMoment(ev: DriftEvent): string {
  if (ev.kind === 'disappeared') return ev.last_seen
  const [ms] = instant(ev.first_seen)
  return new Date(ms + 1000).toISOString()
}

// Kubernetes-resolved names look like "Pod: default/api-7c9f"; the feed reads
// better with just the workload name, keeping the namespace as context.
export function prettyName(id: string, name?: string): { name: string; context?: string } {
  const raw = name || id
  const k8s = raw.match(/^(\w+):\s*([^/]+)\/(.+)$/)
  if (k8s) return { name: k8s[3], context: k8s[2] }
  return { name: raw }
}
