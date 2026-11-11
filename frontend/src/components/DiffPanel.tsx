import { useState, useEffect, useMemo, ReactNode } from 'react'
import { ArrowRight, GitCompare } from 'lucide-react'
import { apiUrl } from '../lib/api'
import { HistoryRange } from './TimelineScrubber'
import DockPanel, { PanelMessage, PanelSkeleton } from './DockPanel'
import { Connection, Service } from '../types'
import { nameFromServiceId, serviceLabel } from '../utils/hostnames'

interface DiffResponse {
  from: string
  to: string
  added_services: Service[]
  removed_services: Service[]
  added_connections: Connection[]
  removed_connections: Connection[]
}

function formatTimestamp(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

interface DiffPanelProps {
  // Only rendered by the caller once this is non-null - see App.tsx.
  range: HistoryRange
  onClose: () => void
  // Names of the services on the live graph, by ID.
  liveServices: Map<string, Service>
}

const SHOWN = 8

export default function DiffPanel({ range, onClose, liveServices }: DiffPanelProps) {
  const earliestMs = new Date(range.earliest).getTime()
  const latestMs = new Date(range.latest).getTime()
  // Default: a day back (or the whole history when there is less).
  const [fromMs, setFromMs] = useState(() => Math.max(earliestMs, latestMs - 24 * 3600 * 1000))
  const [debouncedFrom, setDebouncedFrom] = useState(fromMs)
  const [loading, setLoading] = useState(false)
  const [diff, setDiff] = useState<DiffResponse | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Dragging the slider asks once it settles, not on every pixel.
  useEffect(() => {
    const t = window.setTimeout(() => setDebouncedFrom(fromMs), 200)
    return () => window.clearTimeout(t)
  }, [fromMs])

  // "to" is always now - this answers "what's changed since X", the common
  // case, rather than requiring two independent picks for an arbitrary
  // point-to-point comparison.
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    const from = new Date(debouncedFrom).toISOString()
    const to = new Date().toISOString()
    fetch(apiUrl(`/api/v1/topology/history/diff?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`))
      .then(res => {
        if (!res.ok) throw new Error(`Failed to load (${res.status})`)
        return res.json()
      })
      .then((data: DiffResponse) => {
        if (!cancelled) setDiff(data)
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }, [debouncedFrom])

  // Names as the graph and the Changes feed show them: the diff's own
  // service records first (they include ones no longer live), then the live
  // graph, then whatever the ID says.
  const nameOf = useMemo(() => {
    const known = new Map<string, Service>(liveServices)
    for (const s of [...(diff?.added_services || []), ...(diff?.removed_services || [])]) known.set(s.id, s)
    return (id: string) => {
      const svc = known.get(id)
      return svc ? serviceLabel(svc) : nameFromServiceId(id)
    }
  }, [diff, liveServices])

  const total = diff
    ? diff.added_services.length + diff.removed_services.length + diff.added_connections.length + diff.removed_connections.length
    : 0

  return (
    <DockPanel
      id="compare"
      icon={<GitCompare size={16} />}
      tone="bg-violet-500/15 border-violet-500/30 text-violet-300"
      title="Compare to now"
      subtitle="What appeared or disappeared since an earlier moment"
      onClose={onClose}
      toolbar={
        <div>
          <div className="flex items-center justify-between text-[11px] mb-1.5">
            <span className="text-dark-400">Since <span className="text-dark-100 font-medium">{formatTimestamp(new Date(fromMs).toISOString())}</span></span>
            {diff && !loading && <span className="text-dark-500 tabular-nums">{total} change{total === 1 ? '' : 's'}</span>}
            {loading && <span className="text-dark-500">Comparing…</span>}
          </div>
          <input
            type="range"
            min={earliestMs}
            max={latestMs}
            step={1000}
            value={fromMs}
            onChange={e => setFromMs(Number(e.target.value))}
            className="w-full accent-lens-500"
            aria-label="Compare since"
            data-testid="compare-since"
          />
          <div className="flex justify-between text-[10px] text-dark-500 mt-0.5">
            <span>{formatTimestamp(range.earliest)}</span>
            <span>now</span>
          </div>
        </div>
      }
    >
      {!diff && loading && <PanelSkeleton rows={3} />}
      {error && <PanelMessage tone="error">{error}</PanelMessage>}
      {diff && !error && total === 0 && (
        <PanelMessage>Nothing changed since then: the same services and connections as now.</PanelMessage>
      )}
      {diff && !error && total > 0 && (
        <div className={`p-4 space-y-4 transition-opacity ${loading ? 'opacity-60' : ''}`}>
          <DiffList
            label="Services appeared"
            tone="text-lens-400"
            items={diff.added_services.map(s => ({ key: s.id, title: serviceLabel(s), sub: s.node || s.pod_ip }))}
          />
          <DiffList
            label="Services disappeared"
            tone="text-rose-400"
            items={diff.removed_services.map(s => ({ key: s.id, title: serviceLabel(s), sub: s.node || s.pod_ip }))}
          />
          <DiffList
            label="Connections appeared"
            tone="text-lens-400"
            items={diff.added_connections.map(c => connectionItem(c, nameOf))}
          />
          <DiffList
            label="Connections disappeared"
            tone="text-rose-400"
            items={diff.removed_connections.map(c => connectionItem(c, nameOf))}
          />
        </div>
      )}
    </DockPanel>
  )
}

interface DiffItem {
  key: string
  title: ReactNode
  sub?: string
}

function connectionItem(c: Connection, nameOf: (id: string) => string): DiffItem {
  const target = c.hostnames?.[0] || nameOf(c.target_id)
  const proto = c.protocol && c.protocol !== 'tcp' ? `/${c.protocol}` : ''
  return {
    key: c.id,
    title: (
      <span className="inline-flex items-center gap-1 min-w-0">
        <span className="truncate">{nameOf(c.source_id)}</span>
        <ArrowRight size={11} className="shrink-0 text-dark-500" />
        <span className="truncate">{target}</span>
        <span className="shrink-0 font-mono text-[11px] text-dark-500">:{c.port}{proto}</span>
      </span>
    ),
  }
}

function DiffList({ label, tone, items }: { label: string; tone: string; items: DiffItem[] }) {
  if (items.length === 0) return null
  return (
    <div>
      <div className={`text-xs font-medium ${tone}`}>{label} <span className="text-dark-500 tabular-nums">{items.length}</span></div>
      <ul className="mt-1.5 space-y-1">
        {items.slice(0, SHOWN).map(item => (
          <li key={item.key} className="text-[13px] text-dark-200 min-w-0" title={item.key}>
            <div className="truncate">{item.title}</div>
            {item.sub && <div className="text-[11px] text-dark-500 truncate">{item.sub}</div>}
          </li>
        ))}
      </ul>
      {items.length > SHOWN && <p className="mt-1 text-[11px] text-dark-500">+{items.length - SHOWN} more</p>}
    </div>
  )
}
