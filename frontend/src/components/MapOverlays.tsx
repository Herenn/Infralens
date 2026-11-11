import { History, Loader2, Radar, Radio, X } from 'lucide-react'
import { formatInstant } from '../utils/time'

// The states the map can be in that change what it means: viewing the past,
// and a blast radius. Each is a pill at the top of the map with its way out.

export function HistoryBanner({ at, loading, onGoLive }: { at: string; loading: boolean; onGoLive: () => void }) {
  return (
    <div
      className="pointer-events-auto flex items-center gap-3 pl-3 pr-1.5 py-1.5 rounded-full bg-amber-950/90 backdrop-blur-md
        border border-amber-500/40 shadow-lg shadow-black/40 text-[13px]"
      role="status"
      data-testid="history-banner"
    >
      {loading ? <Loader2 size={14} className="text-amber-300 animate-spin" /> : <History size={14} className="text-amber-300" />}
      <span className="text-amber-100">
        Viewing <span className="font-semibold">{formatInstant(at, true)}</span>
      </span>
      <span className="text-amber-300/70 text-xs hidden min-[1100px]:inline">recorded map, no live rates</span>
      <button
        onClick={onGoLive}
        className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-medium bg-amber-400 text-amber-950
          hover:bg-amber-300 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-200"
      >
        <Radio size={12} /> Back to live
      </button>
    </div>
  )
}

export function ImpactPill({ name, direction, count, loading, onDirection, onClear }: {
  name: string
  direction: 'upstream' | 'downstream'
  count: number | null
  loading: boolean
  onDirection: (d: 'upstream' | 'downstream') => void
  onClear: () => void
}) {
  const n = count ?? 0
  return (
    <div
      className="pointer-events-auto flex items-center gap-3 pl-3 pr-1.5 py-1.5 rounded-full bg-dark-900/95 backdrop-blur-md
        border border-amber-500/50 shadow-[0_8px_30px_rgba(0,0,0,0.45),0_0_0_4px_rgba(245,158,11,0.08)] text-[13px] max-w-full"
      role="status"
      data-testid="impact-pill"
    >
      <Radar size={15} className="shrink-0 text-amber-400" />
      <span className="min-w-0 truncate text-dark-200">
        <span className="font-semibold text-amber-200">{name}</span>
        {loading || count === null ? (
          <span className="text-dark-400"> - tracing…</span>
        ) : direction === 'upstream' ? (
          <> breaks <span className="font-semibold text-dark-50">{n} service{n === 1 ? '' : 's'}</span> if it goes down</>
        ) : (
          <> depends on <span className="font-semibold text-dark-50">{n} service{n === 1 ? '' : 's'}</span></>
        )}
      </span>
      <div className="shrink-0 flex p-0.5 rounded-full bg-dark-800 border border-dark-700 text-[11px]">
        {(['upstream', 'downstream'] as const).map(d => (
          <button
            key={d}
            onClick={() => onDirection(d)}
            aria-pressed={direction === d}
            className={`px-2.5 py-0.5 rounded-full transition-colors ${direction === d ? 'bg-amber-500/20 text-amber-200' : 'text-dark-400 hover:text-dark-200'}`}
          >
            {d === 'upstream' ? 'Breaks' : 'Depends on'}
          </button>
        ))}
      </div>
      <button
        onClick={onClear}
        className="shrink-0 inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-xs text-dark-300 bg-dark-800 border border-dark-700
          hover:text-white hover:border-dark-500 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-400/50"
        title="Clear blast radius (Esc)"
        data-testid="impact-clear"
      >
        <X size={12} /> Clear
      </button>
    </div>
  )
}
