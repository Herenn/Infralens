import { CSSProperties } from 'react'
import { History, Radio, X } from 'lucide-react'
import { formatInstant } from '../utils/time'

export interface HistoryRange {
  earliest: string
  latest: string
}

interface TimelineScrubberProps {
  range: HistoryRange
  // The instant being viewed, or null for live (WebSocket-driven) topology.
  value: string | null
  onChange: (iso: string | null) => void
  loading: boolean
  onClose: () => void
  // Where the bar may sit: the map's free width.
  style?: CSSProperties
}

// The first recorded instant itself holds nothing yet (history opens its
// intervals there), so the scrubber starts a second after it.
const START_OFFSET_MS = 1000

export default function TimelineScrubber({ range, value, onChange, loading, onClose, style }: TimelineScrubberProps) {
  const earliestMs = new Date(range.earliest).getTime() + START_OFFSET_MS
  const latestMs = Math.max(new Date(range.latest).getTime(), earliestMs + 1000)
  const currentMs = value ? Math.min(Math.max(new Date(value).getTime(), earliestMs), latestMs) : latestMs

  const handleSlide = (e: React.ChangeEvent<HTMLInputElement>) => {
    const ms = Number(e.target.value)
    // Dragging all the way to the right returns to live rather than pinning
    // to the last recorded instant, which would otherwise drift stale.
    onChange(ms >= latestMs ? null : new Date(ms).toISOString())
  }

  return (
    <div className="absolute bottom-3 z-20 flex justify-center pointer-events-none" style={style}>
      <div
        className="pointer-events-auto w-full max-w-[640px] rounded-xl bg-dark-900/95 backdrop-blur-md border border-dark-700 shadow-2xl shadow-black/50 px-4 py-3"
        data-testid="timeline"
      >
        <div className="flex items-center justify-between gap-3 mb-2">
          <div className="flex items-center gap-2 text-sm min-w-0">
            {value ? (
              <>
                <History size={14} className="shrink-0 text-amber-300" />
                <span className="text-dark-100 truncate">Viewing {formatInstant(value, true)}</span>
              </>
            ) : (
              <>
                <span className="w-2 h-2 shrink-0 rounded-full bg-lens-400 animate-pulse-slow" />
                <span className="text-dark-100">Live</span>
                <span className="text-dark-500 text-xs truncate">drag left to travel back</span>
              </>
            )}
            {loading && <span className="text-dark-500 text-xs shrink-0">Loading…</span>}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            {value && (
              <button
                onClick={() => onChange(null)}
                className="inline-flex items-center gap-1.5 text-xs px-2.5 py-1 rounded-md bg-lens-500/15 text-lens-300 border border-lens-500/30 hover:bg-lens-500/25 transition-colors"
              >
                <Radio size={12} /> Go live
              </button>
            )}
            <button
              onClick={onClose}
              className="p-1 rounded-md text-dark-400 hover:text-white hover:bg-dark-800 transition-colors"
              title="Hide timeline" aria-label="Hide timeline" data-testid="timeline-close"
            >
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
          aria-label="Point in time"
          data-testid="timeline-slider"
        />
        <div className="flex justify-between text-[10px] text-dark-500 mt-1">
          <span>{formatInstant(new Date(earliestMs).toISOString())}</span>
          <span>now</span>
        </div>
      </div>
    </div>
  )
}
