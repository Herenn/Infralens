import { useState, useEffect, ReactNode } from 'react'
import { X } from 'lucide-react'
import { apiUrl } from '../lib/api'

interface ServiceListPanelProps<T> {
  icon: ReactNode
  label: string
  tooltip: string
  fetchUrl: string
  emptyMessage: string
  getId: (item: T) => string
  getName: (item: T) => string
  renderSubtitle: (item: T) => ReactNode
}

// Generic "button that expands into a fetched list" panel, shared by every
// read-only graph/history query that's just a title, a subtitle line, and
// nothing to click through to (decommission candidates, orphans,
// criticality). Structurally identical enough across all three that a
// fourth would just be another set of props, not another component.
export default function ServiceListPanel<T>({
  icon,
  label,
  tooltip,
  fetchUrl,
  emptyMessage,
  getId,
  getName,
  renderSubtitle,
}: ServiceListPanelProps<T>) {
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [items, setItems] = useState<T[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Fetched only while open - these are "check when curious" tools, not
  // things that need to stay live in the background.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch(apiUrl(fetchUrl))
      .then(res => {
        if (!res.ok) throw new Error(`Failed to load (${res.status})`)
        return res.json()
      })
      .then((data: T[]) => {
        if (!cancelled) setItems(data)
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }, [open, fetchUrl])

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="flex items-center gap-2 px-3 py-2 rounded-lg bg-dark-800 border border-dark-600
          text-dark-300 hover:text-white hover:border-dark-500 shadow-xl transition-colors"
        title={tooltip}
      >
        {icon}
        <span className="text-sm">{label}</span>
      </button>
    )
  }

  return (
    <div className="w-80 max-h-[70vh] rounded-lg bg-dark-800 border border-dark-600 shadow-xl flex flex-col">
      <div className="flex items-center justify-between px-4 py-3 border-b border-dark-700 shrink-0">
        <span className="text-sm font-medium text-dark-200">{label}</span>
        <button onClick={() => setOpen(false)} className="text-dark-400 hover:text-white transition-colors">
          <X size={16} />
        </button>
      </div>
      <div className="overflow-y-auto flex-1 p-2">
        {loading && <p className="text-xs text-dark-500 px-2 py-1">Loading…</p>}
        {error && <p className="text-xs text-red-400 px-2 py-1">{error}</p>}
        {!loading && !error && items?.length === 0 && (
          <p className="text-xs text-dark-500 px-2 py-1">{emptyMessage}</p>
        )}
        {items?.map(item => (
          <div key={getId(item)} className="px-2 py-2 rounded-md">
            <div className="text-sm text-dark-200 truncate" title={getId(item)}>{getName(item)}</div>
            <div className="text-xs text-dark-500">{renderSubtitle(item)}</div>
          </div>
        ))}
      </div>
    </div>
  )
}
