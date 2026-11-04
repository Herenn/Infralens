import { useState } from 'react'
import { History, X } from 'lucide-react'

export interface HistoryRange {
  earliest: string
  latest: string
}

interface TimelineScrubberProps {
  // Null when history is disabled or nothing has been recorded yet - the
  // scrubber has nothing to scrub across and stays hidden entirely.
  range: HistoryRange | null
  // The instant being viewed, or null for live (WebSocket-driven) topology.
  value: string | null
  onChange: (iso: string | null) => void
  loading: boolean
}

function formatTimestamp(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export default function TimelineScrubber({ range, value, onChange, loading }: TimelineScrubberProps) {
  const [open, setOpen] = useState(false)

  if (!range) return null

  const earliestMs = new Date(range.earliest).getTime()
  const latestMs = new Date(range.latest).getTime()
  const currentMs = value ? new Date(value).getTime() : latestMs

  const handleSlide = (e: React.ChangeEvent<HTMLInputElement>) => {
    const ms = Number(e.target.value)
    // Dragging all the way to the right returns to live rather than pinning
    // to the last recorded instant, which would otherwise drift stale.
    onChange(ms >= latestMs ? null : new Date(ms).toISOString())
  }

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="absolute bottom-6 left-1/2 -translate-x-1/2 z-10 flex items-center gap-2 px-3 py-2
          rounded-lg bg-dark-800 border border-dark-600 text-dark-300 hover:text-white hover:border-dark-500
          shadow-xl transition-colors"
        title="View topology history"
      >
        <History size={16} />
        <span className="text-sm">History</span>
        {value && <span className="w-2 h-2 rounded-full bg-amber-400" />}
      </button>
    )
  }

  return (
    <div className="absolute bottom-6 left-1/2 -translate-x-1/2 z-10 w-[min(640px,90vw)] rounded-lg
      bg-dark-800 border border-dark-600 shadow-xl px-4 py-3">
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2 text-sm">
          {value ? (
            <>
              <span className="w-2 h-2 rounded-full bg-amber-400" />
              <span className="text-dark-200">Viewing {formatTimestamp(value)}</span>
            </>
          ) : (
            <>
              <span className="w-2 h-2 rounded-full bg-lens-400 animate-pulse-slow" />
              <span className="text-dark-200">Live</span>
            </>
          )}
          {loading && <span className="text-dark-500 text-xs">Loading…</span>}
        </div>
        <div className="flex items-center gap-2">
          {value && (
            <button
              onClick={() => onChange(null)}
              className="text-xs px-2 py-1 rounded bg-dark-700 text-dark-300 hover:text-white hover:bg-dark-600 transition-colors"
            >
              Go live
            </button>
          )}
          <button onClick={() => setOpen(false)} className="text-dark-400 hover:text-white transition-colors">
            <X size={16} />
          </button>
        </div>
      </div>

      <input
        type="range"
        min={earliestMs}
        max={latestMs}
        step={1000}
        value={currentMs}
        onChange={handleSlide}
        className="w-full accent-lens-500"
      />
      <div className="flex justify-between text-[10px] text-dark-500 mt-1">
        <span>{formatTimestamp(range.earliest)}</span>
        <span>{formatTimestamp(range.latest)}</span>
      </div>
    </div>
  )
}
