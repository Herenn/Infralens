import { useState, useEffect } from 'react'
import { GitCompare, X } from 'lucide-react'
import { apiUrl } from '../lib/api'
import { HistoryRange } from './TimelineScrubber'

interface DiffEntity {
  id: string
  name?: string
}

interface DiffResponse {
  from: string
  to: string
  added_services: DiffEntity[]
  removed_services: DiffEntity[]
  added_connections: DiffEntity[]
  removed_connections: DiffEntity[]
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
}

export default function DiffPanel({ range }: DiffPanelProps) {
  const [open, setOpen] = useState(false)
  const earliestMs = new Date(range.earliest).getTime()
  const latestMs = new Date(range.latest).getTime()
  const [fromMs, setFromMs] = useState(earliestMs)
  const [loading, setLoading] = useState(false)
  const [diff, setDiff] = useState<DiffResponse | null>(null)
  const [error, setError] = useState<string | null>(null)

  // "to" is always now - this answers "what's changed since X", the common
  // case, rather than requiring two independent picks for an arbitrary
  // point-to-point comparison.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    setError(null)
    const from = new Date(fromMs).toISOString()
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
  }, [open, fromMs])

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="flex items-center gap-2 px-3 py-2 rounded-lg bg-dark-800 border border-dark-600
          text-dark-300 hover:text-white hover:border-dark-500 shadow-xl transition-colors"
        title="Compare the topology at an earlier point to right now"
      >
        <GitCompare size={16} />
        <span className="text-sm">Compare to now</span>
      </button>
    )
  }

  return (
    <div className="w-80 max-h-[70vh] rounded-lg bg-dark-800 border border-dark-600 shadow-xl flex flex-col">
      <div className="flex items-center justify-between px-4 py-3 border-b border-dark-700 shrink-0">
        <span className="text-sm font-medium text-dark-200">Compare to now</span>
        <button onClick={() => setOpen(false)} className="text-dark-400 hover:text-white transition-colors">
          <X size={16} />
        </button>
      </div>

      <div className="px-4 py-3 border-b border-dark-700 shrink-0">
        <div className="text-xs text-dark-500 mb-1">Since {formatTimestamp(new Date(fromMs).toISOString())}</div>
        <input
          type="range"
          min={earliestMs}
          max={latestMs}
          step={1000}
          value={fromMs}
          onChange={e => setFromMs(Number(e.target.value))}
          className="w-full accent-lens-500"
        />
      </div>

      <div className="overflow-y-auto flex-1 p-4 space-y-3">
        {loading && <p className="text-xs text-dark-500">Loading…</p>}
        {error && <p className="text-xs text-red-400">{error}</p>}
        {diff && !loading && !error && (
          <>
            <DiffStat
              label="Services appeared"
              names={diff.added_services.map(s => s.name || s.id)}
              color="text-lens-400"
            />
            <DiffStat
              label="Services disappeared"
              names={diff.removed_services.map(s => s.name || s.id)}
              color="text-red-400"
            />
            <DiffStat
              label="Connections appeared"
              names={diff.added_connections.map(c => c.id)}
              color="text-lens-400"
            />
            <DiffStat
              label="Connections disappeared"
              names={diff.removed_connections.map(c => c.id)}
              color="text-red-400"
            />
          </>
        )}
      </div>
    </div>
  )
}

function DiffStat({ label, names, color }: { label: string; names: string[]; color: string }) {
  return (
    <div>
      <div className={`text-sm font-medium ${color}`}>{label}: {names.length}</div>
      {names.length > 0 && (
        <div className="text-xs text-dark-500 mt-0.5" title={names.join(', ')}>
          {names.slice(0, 5).join(', ')}{names.length > 5 ? `, +${names.length - 5} more` : ''}
        </div>
      )}
    </div>
  )
}
