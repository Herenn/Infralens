import { useState, useEffect, ReactNode } from 'react'
import { apiUrl } from '../lib/api'
import DockPanel, { PanelMessage, PanelSkeleton } from './DockPanel'
import { PanelId } from './dock'

interface ServiceListPanelProps<T> {
  id: PanelId
  icon: ReactNode
  tone: string
  title: string
  subtitle: ReactNode
  fetchUrl: string
  emptyMessage: string
  onClose: () => void
  getId: (item: T) => string
  getName: (item: T) => string
  renderSubtitle: (item: T) => ReactNode
  // Something on the right of a row (e.g. a count).
  renderAside?: (item: T) => ReactNode
  // Rows are buttons when given.
  onSelect?: (item: T) => void
  selectedId?: string | null
  // Why a row can be clicked, shown once above the list.
  hint?: ReactNode
}

// A docked panel listing a fetched set of services: decommission
// candidates, orphans, criticality. Fetched when opened - these are "check
// when curious" tools, not things that need to stay live in the background.
export default function ServiceListPanel<T>({
  id, icon, tone, title, subtitle, fetchUrl, emptyMessage, onClose,
  getId, getName, renderSubtitle, renderAside, onSelect, selectedId, hint,
}: ServiceListPanelProps<T>) {
  const [loading, setLoading] = useState(true)
  const [items, setItems] = useState<T[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [reload, setReload] = useState(0)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch(apiUrl(fetchUrl))
      .then(res => {
        if (!res.ok) throw new Error(`Failed to load (${res.status})`)
        return res.json()
      })
      .then((data: T[] | null) => {
        if (!cancelled) setItems(data || [])
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }, [fetchUrl, reload])

  return (
    <DockPanel id={id} icon={icon} tone={tone} title={title} subtitle={subtitle} onClose={onClose}>
      {loading && !items && <PanelSkeleton />}
      {error && (
        <PanelMessage tone="error">
          {error}{' '}
          <button onClick={() => setReload(r => r + 1)} className="ml-1 underline text-dark-300 hover:text-white">Retry</button>
        </PanelMessage>
      )}
      {!error && items?.length === 0 && <PanelMessage>{emptyMessage}</PanelMessage>}
      {!error && items && items.length > 0 && (
        <>
          {hint && <p className="px-4 pt-3 text-[11px] text-dark-500">{hint}</p>}
          <ul className="p-2">
            {items.map(item => {
              const itemId = getId(item)
              const selected = selectedId === itemId
              const body = (
                <>
                  <div className="min-w-0 flex-1">
                    <div className="text-[13px] text-dark-100 truncate" title={itemId}>{getName(item)}</div>
                    <div className="text-[11px] text-dark-500 truncate mt-0.5">{renderSubtitle(item)}</div>
                  </div>
                  {renderAside && <div className="shrink-0">{renderAside(item)}</div>}
                </>
              )
              return (
                <li key={itemId}>
                  {onSelect ? (
                    <button
                      onClick={() => onSelect(item)}
                      aria-pressed={selected}
                      className={`w-full text-left flex items-center gap-3 px-3 py-2 rounded-lg transition-colors
                        focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-lens-500/50
                        ${selected ? 'bg-amber-500/10 ring-1 ring-amber-500/40' : 'hover:bg-dark-800'}`}
                    >
                      {body}
                    </button>
                  ) : (
                    <div className="flex items-center gap-3 px-3 py-2">{body}</div>
                  )}
                </li>
              )
            })}
          </ul>
        </>
      )}
    </DockPanel>
  )
}
