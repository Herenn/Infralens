import { ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, ArrowRight, ChevronDown, Crosshair, Globe, History, Loader2, Plus, Radar, RotateCcw, Unplug, X } from 'lucide-react'
import { DriftEvent, DriftSilentAgent, DriftSummary } from '../types'
import { DOCK_CLASS } from './dock'
import {
  DriftFilter, driftMoment, fetchDriftEvents, fetchDriftSummary, isAfter, matchesFilter, onDriftEvents, onDriftResync,
  prettyName,
} from '../lib/drift'

// The most events the panel holds (and renders). A rollout can add thousands
// in one pass; past this the feed asks for filters instead of growing.
const MAX_FEED_EVENTS = 500

// While open, how often to check the server's newest event against what is
// shown - catches live messages the server dropped for a slow client.
const RESYNC_POLL_INTERVAL = 30000

interface ChangesPanelProps {
  onClose: () => void
  // The last-seen marker when the panel was opened: newer events are "new".
  seenAtOpen: string
  selectedId: number | null
  onSelect: (event: DriftEvent | null) => void
  // Whether the event's endpoints are on the graph currently displayed.
  isOnGraph: (event: DriftEvent) => boolean
  // The instant time travel is showing, or null for live.
  viewingAt: string | null
  onViewAt: (event: DriftEvent | null, at: string | null) => void
}

type KindFilter = 'all' | 'appeared' | 'disappeared'

function dayKey(iso: string): string {
  const d = new Date(iso)
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}

function dayLabel(iso: string, now: number): string {
  const d = new Date(iso)
  const today = new Date(now)
  const yesterday = new Date(now - 86400000)
  if (dayKey(iso) === dayKey(today.toISOString())) return 'Today'
  if (dayKey(iso) === dayKey(yesterday.toISOString())) return 'Yesterday'
  return d.toLocaleDateString(undefined, { weekday: 'long', month: 'short', day: 'numeric' })
}

function clockTime(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

function fullTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

function relativeTime(iso: string, now: number): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  if (s < 45) return 'just now'
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} h ago`
  const d = Math.round(h / 24)
  return `${d} day${d === 1 ? '' : 's'} ago`
}

function durationLabel(seconds?: number): string | null {
  if (!seconds) return null
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  return `${Math.round(seconds / 3600)}h`
}

export default function ChangesPanel({ onClose, seenAtOpen, selectedId, onSelect, isOnGraph, viewingAt, onViewAt }: ChangesPanelProps) {
  const [kind, setKind] = useState<KindFilter>('all')
  const [externalOnly, setExternalOnly] = useState(false)
  const [events, setEvents] = useState<DriftEvent[]>([])
  const [cursor, setCursor] = useState<string | undefined>()
  const [summary, setSummary] = useState<DriftSummary | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [liveIds, setLiveIds] = useState<Set<number>>(new Set())
  const [now, setNow] = useState(Date.now())
  const [reload, setReload] = useState(0)

  const filter = useMemo<DriftFilter>(() => ({
    kind: kind === 'all' ? undefined : kind,
    externalOnly,
  }), [kind, externalOnly])
  const filtered = kind !== 'all' || externalOnly

  // Relative times stay fresh while the panel is open.
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30000)
    return () => window.clearInterval(timer)
  }, [])

  useEffect(() => {
    const ctrl = new AbortController()
    setLoading(true)
    setError(null)
    Promise.all([fetchDriftEvents(filter, undefined, ctrl.signal), fetchDriftSummary(null, ctrl.signal)])
      .then(([page, sum]) => {
        setEvents(page.events)
        setCursor(page.next_cursor)
        setSummary(sum)
      })
      .catch(err => {
        if (!ctrl.signal.aborted) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })
    return () => ctrl.abort()
  }, [filter, reload])

  // A capped live message or a reconnect: what is shown may be incomplete.
  useEffect(() => onDriftResync(() => setReload(r => r + 1)), [])

  // Catch-up: if the server's newest event is newer than any we know of, a
  // live message was missed - refetch.
  const latestKnownRef = useRef<string | null>(null)
  latestKnownRef.current = summary?.latest ?? null
  useEffect(() => {
    const timer = window.setInterval(() => {
      fetchDriftSummary()
        .then(sum => {
          if (sum.latest && isAfter(sum.latest, latestKnownRef.current)) setReload(r => r + 1)
        })
        .catch(() => {})
    }, RESYNC_POLL_INTERVAL)
    return () => window.clearInterval(timer)
  }, [])

  // Live arrivals: prepend what matches, keep the counters honest.
  useEffect(() => onDriftEvents(incoming => {
    setNow(Date.now())
    setSummary(prev => prev && {
      ...prev,
      total: prev.total + incoming.length,
      appeared: prev.appeared + incoming.filter(e => e.kind === 'appeared').length,
      disappeared: prev.disappeared + incoming.filter(e => e.kind === 'disappeared').length,
      external: prev.external + incoming.filter(e => e.external).length,
      latest: incoming[0]?.detected_at ?? prev.latest,
    })
    const fresh = incoming.filter(e => matchesFilter(e, filter))
    if (fresh.length === 0) return
    setEvents(prev => {
      const known = new Set(prev.map(e => e.id))
      return [...fresh.filter(e => !known.has(e.id)), ...prev].slice(0, MAX_FEED_EVENTS)
    })
    setLiveIds(prev => new Set([...prev, ...fresh.map(e => e.id)]))
  }), [filter])

  const loadMore = useCallback(() => {
    if (!cursor) return
    setLoadingMore(true)
    fetchDriftEvents(filter, cursor)
      .then(page => {
        setEvents(prev => {
          const known = new Set(prev.map(e => e.id))
          return [...prev, ...page.events.filter(e => !known.has(e.id))].slice(0, MAX_FEED_EVENTS)
        })
        setCursor(page.next_cursor)
      })
      .catch(err => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoadingMore(false))
  }, [cursor, filter])

  const groups = useMemo(() => {
    const out: { key: string; label: string; events: DriftEvent[] }[] = []
    for (const ev of events) {
      const key = dayKey(ev.detected_at)
      const last = out[out.length - 1]
      if (last && last.key === key) last.events.push(ev)
      else out.push({ key, label: dayLabel(ev.detected_at, now), events: [ev] })
    }
    return out
  }, [events, now])

  const detector = summary?.detector
  const warmingUntil = detector?.warmup_until && new Date(detector.warmup_until).getTime() > now
    ? detector.warmup_until
    : null

  return (
    <section className={DOCK_CLASS} data-panel="changes" aria-label="Changes">
      {/* Header */}
      <div className="px-4 pt-4 pb-3 border-b border-dark-700/80 shrink-0">
        <div className="flex items-start justify-between">
          <div className="flex items-center gap-3">
            <div className="relative w-9 h-9 rounded-lg bg-gradient-to-br from-lens-500/25 to-lens-700/10 border border-lens-500/30
              flex items-center justify-center">
              <Radar size={18} className="text-lens-400" />
              <span className="absolute -top-0.5 -right-0.5 w-2 h-2 rounded-full bg-lens-400 animate-pulse-slow" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-dark-100 leading-tight">Changes</h2>
              <p className="text-[11px] text-dark-500 leading-tight mt-0.5">Architecture drift, as it happens</p>
            </div>
          </div>
          <button onClick={onClose} className="p-1.5 -m-1 rounded-md text-dark-400 hover:text-white hover:bg-dark-800 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-lens-500/50" title="Close" aria-label="Close Changes" data-testid="panel-close">
            <X size={16} />
          </button>
        </div>

        {/* Counters double as filter shortcuts */}
        {summary && summary.total > 0 && (
          <div className="flex items-center gap-1.5 mt-3">
            <StatPill active={kind === 'appeared'} onClick={() => setKind(kind === 'appeared' ? 'all' : 'appeared')}
              className="text-lens-300" dot="bg-lens-400" value={summary.appeared} label="new" />
            <StatPill active={kind === 'disappeared'} onClick={() => setKind(kind === 'disappeared' ? 'all' : 'disappeared')}
              className="text-rose-300" dot="bg-rose-400" value={summary.disappeared} label="gone" />
            <StatPill active={externalOnly} onClick={() => setExternalOnly(!externalOnly)}
              className="text-amber-300" icon={<Globe size={11} />} value={summary.external} label="internet" />
          </div>
        )}

        {/* Filters */}
        <div className="flex items-center justify-between mt-3">
          <div className="flex p-0.5 rounded-lg bg-dark-800 border border-dark-700">
            {(['all', 'appeared', 'disappeared'] as KindFilter[]).map(k => (
              <button
                key={k}
                onClick={() => setKind(k)}
                className={`px-2.5 py-1 rounded-md text-[11px] font-medium transition-colors ${
                  kind === k ? 'bg-dark-600 text-dark-100 shadow' : 'text-dark-400 hover:text-dark-200'
                }`}
              >
                {k === 'all' ? 'All' : k === 'appeared' ? 'New' : 'Gone'}
              </button>
            ))}
          </div>
          <button
            onClick={() => setExternalOnly(!externalOnly)}
            className={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium border transition-colors ${
              externalOnly
                ? 'bg-amber-500/15 text-amber-300 border-amber-500/40'
                : 'bg-dark-800 text-dark-400 border-dark-700 hover:text-dark-200'
            }`}
            title="Only dependencies on the public internet"
          >
            <Globe size={12} />
            Internet only
          </button>
        </div>
      </div>

      {/* Feed */}
      <div className="flex-1 overflow-y-auto overscroll-contain">
        {loading && <FeedSkeleton />}

        {!loading && error && (
          <div className="p-6 text-center">
            <p className="text-xs text-red-400 mb-3">{error}</p>
            <button onClick={() => setReload(r => r + 1)}
              className="inline-flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-md bg-dark-800 text-dark-300 hover:text-white">
              <RotateCcw size={12} /> Retry
            </button>
          </div>
        )}

        {!loading && !error && events.length === 0 && (
          filtered && (summary?.total ?? 0) > 0
            ? <FilteredEmpty onClear={() => { setKind('all'); setExternalOnly(false) }} />
            : <EmptyState summary={summary} warmingUntil={warmingUntil} now={now} />
        )}

        {!loading && !error && groups.map(group => (
          <section key={group.key}>
            <div className="sticky top-0 z-10 px-4 py-1.5 bg-dark-900/95 backdrop-blur border-b border-dark-800/80
              text-[10px] font-semibold uppercase tracking-wider text-dark-500">
              {group.label}
            </div>
            <ol className="relative py-1">
              {/* the timeline rail */}
              <span className="absolute left-[31px] top-0 bottom-0 w-px bg-dark-700/70" aria-hidden />
              {group.events.map(ev => (
                <ChangeItem
                  key={ev.id}
                  event={ev}
                  now={now}
                  unread={isAfter(ev.detected_at, seenAtOpen)}
                  live={liveIds.has(ev.id)}
                  selected={selectedId === ev.id}
                  onGraph={isOnGraph(ev)}
                  viewing={viewingAt === driftMoment(ev)}
                  onSelect={() => onSelect(selectedId === ev.id ? null : ev)}
                  onViewAt={at => onViewAt(ev, at)}
                />
              ))}
            </ol>
          </section>
        ))}

        {!loading && !error && events.length >= MAX_FEED_EVENTS && (
          <p className="px-4 py-3 text-center text-[11px] text-dark-500">
            Showing the newest {MAX_FEED_EVENTS} changes. Use the filters to narrow it down.
          </p>
        )}

        {!loading && !error && cursor && events.length < MAX_FEED_EVENTS && (
          <div className="p-3 flex justify-center">
            <button onClick={loadMore} disabled={loadingMore}
              className="inline-flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-md bg-dark-800 border border-dark-700
                text-dark-300 hover:text-white hover:border-dark-600 disabled:opacity-60">
              {loadingMore && <Loader2 size={12} className="animate-spin" />}
              Load older changes
            </button>
          </div>
        )}
      </div>

      {/* Agents that stopped reporting: what only they saw is not watched.
          Nothing is rendered while every agent reports. */}
      {detector?.silent_agents && detector.silent_agents.length > 0 && (
        <SilentAgentsLine agents={detector.silent_agents} total={detector.silent_agents_total ?? detector.silent_agents.length} now={now} />
      )}

      {/* Footer: what the detector is doing */}
      {detector && (
        <div className="px-4 py-2 border-t border-dark-700/80 shrink-0 flex items-center gap-2 text-[10px] text-dark-500">
          <span className={`w-1.5 h-1.5 rounded-full ${detector.running ? 'bg-lens-400 animate-pulse-slow' : 'bg-dark-600'}`} />
          {detector.running ? (
            <span>
              Watching
              {durationLabel(detector.check_interval_seconds) && <> · checks every {durationLabel(detector.check_interval_seconds)}</>}
              {durationLabel(detector.gone_after_seconds) && <> · gone after {durationLabel(detector.gone_after_seconds)} unseen</>}
            </span>
          ) : (
            <span>Drift detection is starting…</span>
          )}
        </div>
      )}
    </section>
  )
}

// "2 agents stopped reporting (node-a 3 h ago, node-b 1 day ago) - changes
// only they could see are not being detected", the full list on expand.
function SilentAgentsLine({ agents, total, now }: { agents: DriftSilentAgent[]; total: number; now: number }) {
  const [open, setOpen] = useState(false)
  const named = agents.slice(0, 2).map(a => `${a.node} ${relativeTime(a.last_seen, now)}`).join(', ')
  const more = total - Math.min(agents.length, 2)
  return (
    <div className="border-t border-orange-500/30 bg-orange-500/10 shrink-0" data-testid="changes-silent-agents">
      <button
        onClick={() => setOpen(o => !o)}
        aria-expanded={open}
        className="w-full flex items-start gap-2 px-4 py-2 text-left text-[11px] leading-snug text-orange-100/90
          hover:bg-orange-500/10 transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-orange-400/50"
        title={agents.map(a => `${a.node}${a.group ? ` (${a.group})` : ''} - last report ${fullTime(a.last_seen)}`).join('\n')}
      >
        <AlertTriangle size={13} className="shrink-0 mt-px text-orange-300" />
        <span className="flex-1 min-w-0">
          <span className="font-semibold text-orange-200">
            {total === 1 ? '1 agent stopped reporting' : `${total} agents stopped reporting`}
          </span>{' '}
          ({named}{more > 0 ? `, +${more} more` : ''}) - changes only {total === 1 ? 'it' : 'they'} could see are not being detected
        </span>
        <ChevronDown size={13} className={`shrink-0 mt-px text-orange-300/80 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <ul className="px-4 pb-2 pl-[37px] space-y-0.5 max-h-32 overflow-y-auto text-[11px]">
          {agents.map(a => (
            <li key={`${a.group ?? ''}/${a.node}`} className="flex justify-between gap-3 text-orange-100/80">
              <span className="truncate">{a.node}{a.group && <span className="text-orange-200/50"> · {a.group}</span>}</span>
              <span className="shrink-0 tabular-nums text-orange-200/60" title={fullTime(a.last_seen)}>{relativeTime(a.last_seen, now)}</span>
            </li>
          ))}
          {total > agents.length && <li className="text-orange-200/50">+{total - agents.length} more</li>}
        </ul>
      )}
    </div>
  )
}

function StatPill({ value, label, className, dot, icon, active, onClick }: {
  value: number
  label: string
  className: string
  dot?: string
  icon?: ReactNode
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className={`flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] border transition-colors ${className} ${
        active ? 'bg-dark-700 border-dark-500' : 'bg-dark-800/60 border-dark-700 hover:border-dark-600'
      }`}
    >
      {dot && <span className={`w-1.5 h-1.5 rounded-full ${dot}`} />}
      {icon}
      <span className="font-semibold tabular-nums">{value}</span>
      <span className="text-dark-400">{label}</span>
    </button>
  )
}

function ChangeItem({ event: ev, now, unread, live, selected, onGraph, viewing, onSelect, onViewAt }: {
  event: DriftEvent
  now: number
  unread: boolean
  live: boolean
  selected: boolean
  onGraph: boolean
  viewing: boolean
  onSelect: () => void
  onViewAt: (at: string | null) => void
}) {
  const appeared = ev.kind === 'appeared'
  const source = prettyName(ev.source_id, ev.source_name)
  const target = prettyName(ev.target_id, ev.target_name)

  const tone = appeared
    ? ev.external
      ? { bubble: 'bg-amber-500/15 border-amber-400/50 text-amber-300 shadow-[0_0_14px_rgba(245,158,11,0.35)]', verb: 'text-amber-300' }
      : { bubble: 'bg-lens-500/15 border-lens-400/50 text-lens-300', verb: 'text-lens-300' }
    : { bubble: 'bg-rose-500/10 border-rose-400/40 text-rose-300', verb: 'text-rose-300' }
  const Icon = appeared ? (ev.external ? Globe : Plus) : Unplug
  const verb = appeared
    ? ev.external ? 'New internet dependency' : 'New dependency'
    : 'No longer observed'
  const protocol = ev.protocol && ev.protocol !== 'tcp' ? `/${ev.protocol}` : ''

  return (
    <li className={`relative ${live ? 'drift-enter' : ''}`}>
      <div
        role="button"
        tabIndex={0}
        onClick={onSelect}
        onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect() } }}
        className={`group mx-2 my-0.5 pl-2 pr-3 py-2.5 rounded-lg flex gap-3 cursor-pointer transition-colors outline-none
          focus-visible:ring-1 focus-visible:ring-lens-500/50
          ${selected ? 'bg-dark-800 ring-1 ring-dark-600' : 'hover:bg-dark-800/60'}`}
      >
        <div className={`relative z-[1] mt-0.5 w-7 h-7 shrink-0 rounded-full border flex items-center justify-center ${tone.bubble}`}>
          <Icon size={14} />
        </div>

        <div className="min-w-0 flex-1">
          <div className="flex items-start justify-between gap-2">
            <div className={`text-[13px] leading-snug min-w-0 ${appeared ? 'text-dark-100' : 'text-dark-300'}`}>
              <span className="font-medium" title={ev.source_id}>{source.name}</span>
              <ArrowRight size={12} className={`inline mx-1 -mt-0.5 ${appeared ? 'text-dark-400' : 'text-dark-600'}`} />
              <span className={`font-medium ${appeared ? '' : 'line-through decoration-rose-400/60 decoration-1'}`} title={ev.target_id}>
                {target.name}
              </span>
            </div>
            <div className="flex items-center gap-1.5 shrink-0 pt-0.5">
              {unread && <span className="w-1.5 h-1.5 rounded-full bg-lens-400" title="New since you last looked" />}
              <span className="text-[11px] text-dark-500 tabular-nums" title={fullTime(ev.detected_at)}>{clockTime(ev.detected_at)}</span>
            </div>
          </div>

          <div className="mt-1 flex items-center gap-1.5 flex-wrap text-[11px]">
            <span className={tone.verb}>{verb}</span>
            <span className="text-dark-600">·</span>
            <span className="font-mono text-dark-400">:{ev.port}{protocol}</span>
            {ev.external && !appeared && (
              <span className="inline-flex items-center gap-1 px-1.5 rounded bg-amber-500/10 text-amber-300/80 text-[10px]">
                <Globe size={9} /> internet
              </span>
            )}
            {(source.context || target.context) && (
              <>
                <span className="text-dark-600">·</span>
                <span className="text-dark-500 truncate">ns/{target.context || source.context}</span>
              </>
            )}
            <span className="text-dark-600">·</span>
            <span className="text-dark-500">{relativeTime(ev.detected_at, now)}</span>
          </div>

          {selected && (
            <div className="mt-2.5 pt-2.5 border-t border-dark-700/80" onClick={e => e.stopPropagation()}>
              <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-[11px] mb-2.5">
                <dt className="text-dark-500">{appeared ? 'First seen' : 'Last seen'}</dt>
                <dd className="text-dark-300 text-right tabular-nums">{fullTime(appeared ? ev.first_seen : ev.last_seen)}</dd>
                <dt className="text-dark-500">Detected</dt>
                <dd className="text-dark-300 text-right tabular-nums">{fullTime(ev.detected_at)}</dd>
              </dl>
              {!onGraph && !viewing && (
                <p className="text-[11px] text-dark-500 mb-2">
                  {appeared ? 'Not on the graph you are viewing.' : 'This dependency is no longer on the live graph.'}
                </p>
              )}
              <div className="flex gap-2">
                {viewing ? (
                  <button onClick={() => onViewAt(null)}
                    className="flex-1 inline-flex items-center justify-center gap-1.5 text-[11px] px-2.5 py-1.5 rounded-md
                      bg-amber-500/15 text-amber-300 border border-amber-500/40 hover:bg-amber-500/25">
                    <History size={12} /> Back to live
                  </button>
                ) : (
                  <button onClick={() => onViewAt(driftMoment(ev))}
                    className="flex-1 inline-flex items-center justify-center gap-1.5 text-[11px] px-2.5 py-1.5 rounded-md
                      bg-dark-700 text-dark-200 border border-dark-600 hover:text-white hover:border-dark-500">
                    <History size={12} /> {appeared ? 'View when it appeared' : 'View when last seen'}
                  </button>
                )}
                {onGraph && (
                  <span className="inline-flex items-center gap-1.5 text-[11px] px-2.5 py-1.5 rounded-md text-lens-300 bg-lens-500/10 border border-lens-500/30">
                    <Crosshair size={12} /> On graph
                  </span>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
    </li>
  )
}

function FeedSkeleton() {
  return (
    <div className="p-3 space-y-2" aria-busy>
      {[0, 1, 2, 3].map(i => (
        <div key={i} className="flex gap-3 p-2 animate-pulse">
          <div className="w-7 h-7 rounded-full bg-dark-800" />
          <div className="flex-1 space-y-2 py-1">
            <div className="h-3 rounded bg-dark-800 w-3/4" />
            <div className="h-2.5 rounded bg-dark-800/70 w-1/2" />
          </div>
        </div>
      ))}
    </div>
  )
}

function FilteredEmpty({ onClear }: { onClear: () => void }) {
  return (
    <div className="px-6 py-10 text-center">
      <p className="text-sm text-dark-300">Nothing matches these filters.</p>
      <button onClick={onClear} className="mt-2 text-xs text-lens-400 hover:text-lens-300">Show all changes</button>
    </div>
  )
}

function EmptyState({ summary, warmingUntil, now }: { summary: DriftSummary | null; warmingUntil: string | null; now: number }) {
  const running = summary?.detector.running
  const minutesLeft = warmingUntil ? Math.max(1, Math.round((new Date(warmingUntil).getTime() - now) / 60000)) : 0

  return (
    <div className="px-6 py-10 text-center">
      <div className="relative w-16 h-16 mx-auto mb-5">
        <span className="absolute inset-0 rounded-full border border-lens-500/20 animate-ping [animation-duration:3s]" />
        <span className="absolute inset-2 rounded-full border border-lens-500/30" />
        <div className="absolute inset-4 rounded-full bg-lens-500/15 flex items-center justify-center">
          <Radar size={16} className="text-lens-400" />
        </div>
      </div>

      {warmingUntil ? (
        <>
          <h3 className="text-sm font-medium text-dark-100">Learning your architecture</h3>
          <p className="mt-2 text-xs leading-relaxed text-dark-400">
            While it warms up, everything InfraLens sees becomes the baseline, so a fresh
            install doesn&apos;t announce your whole cluster as &ldquo;new&rdquo;. From{' '}
            <span className="text-dark-200">{clockTime(warmingUntil)}</span> (in about {minutesLeft} min),
            new dependencies show up here.
          </p>
        </>
      ) : running === false ? (
        <>
          <h3 className="text-sm font-medium text-dark-100">Drift detection is starting</h3>
          <p className="mt-2 text-xs leading-relaxed text-dark-400">
            It is loading what it already knows about your architecture from history. Changes
            show up here as soon as it is watching.
          </p>
        </>
      ) : (
        <>
          <h3 className="text-sm font-medium text-dark-100">No changes yet</h3>
          <p className="mt-2 text-xs leading-relaxed text-dark-400">
            InfraLens is watching. When a service starts talking to something it never has before
            &mdash; especially on the public internet &mdash; or stops talking to something it used to,
            it appears here.
          </p>
        </>
      )}
    </div>
  )
}
