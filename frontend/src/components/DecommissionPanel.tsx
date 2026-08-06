import { useState, useEffect } from 'react'
import { Trash2, X } from 'lucide-react'
import { StaleService } from '../types'
import { apiUrl } from '../lib/api'

function formatDaysAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  const days = Math.floor(ms / (1000 * 60 * 60 * 24))
  if (days <= 0) return 'today'
  if (days === 1) return '1 day ago'
  return `${days} days ago`
}

export default function DecommissionPanel() {
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [services, setServices] = useState<StaleService[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Fetched only while open - this is a "check when curious" tool, not
  // something that needs to stay live in the background.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch(apiUrl('/api/v1/topology/history/stale'))
      .then(res => {
        if (!res.ok) throw new Error(`Failed to load (${res.status})`)
        return res.json()
      })
      .then((data: StaleService[]) => {
        if (!cancelled) setServices(data)
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }, [open])

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="absolute top-4 right-4 z-10 flex items-center gap-2 px-3 py-2
          rounded-lg bg-dark-800 border border-dark-600 text-dark-300 hover:text-white hover:border-dark-500
          shadow-xl transition-colors"
        title="Services not seen recently - candidates for decommissioning"
      >
        <Trash2 size={16} />
        <span className="text-sm">Decommission candidates</span>
      </button>
    )
  }

  return (
    <div className="absolute top-4 right-4 z-10 w-80 max-h-[70vh] rounded-lg
      bg-dark-800 border border-dark-600 shadow-xl flex flex-col">
      <div className="flex items-center justify-between px-4 py-3 border-b border-dark-700 shrink-0">
        <span className="text-sm font-medium text-dark-200">Decommission candidates</span>
        <button onClick={() => setOpen(false)} className="text-dark-400 hover:text-white transition-colors">
          <X size={16} />
        </button>
      </div>
      <div className="overflow-y-auto flex-1 p-2">
        {loading && <p className="text-xs text-dark-500 px-2 py-1">Loading…</p>}
        {error && <p className="text-xs text-red-400 px-2 py-1">{error}</p>}
        {!loading && !error && services?.length === 0 && (
          <p className="text-xs text-dark-500 px-2 py-1">
            Nothing idle — everything's been seen recently.
          </p>
        )}
        {services?.map(svc => (
          <div key={svc.id} className="px-2 py-2 rounded-md">
            <div className="text-sm text-dark-200 truncate" title={svc.id}>{svc.name || svc.id}</div>
            <div className="text-xs text-dark-500">
              last seen {formatDaysAgo(svc.last_seen)}
              {svc.node && <> · {svc.node}</>}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
