import { ReactNode } from 'react'
import { X } from 'lucide-react'
import { DOCK_CLASS, PanelId } from './dock'

interface DockPanelProps {
  id: PanelId
  icon: ReactNode
  // Tailwind classes for the icon chip (background, border, text colour).
  tone: string
  title: string
  subtitle?: ReactNode
  onClose: () => void
  // Controls under the title (e.g. the "since" slider of Compare).
  toolbar?: ReactNode
  children: ReactNode
}

// The shell every docked panel shares: icon chip, title and subtitle, close.
export default function DockPanel({ id, icon, tone, title, subtitle, onClose, toolbar, children }: DockPanelProps) {
  return (
    <section className={DOCK_CLASS} data-panel={id} aria-label={title}>
      <div className="flex items-start gap-3 px-4 pt-4 pb-3 shrink-0 border-b border-dark-700/80">
        <div className={`w-8 h-8 shrink-0 rounded-lg border flex items-center justify-center ${tone}`}>{icon}</div>
        <div className="flex-1 min-w-0">
          <h2 className="text-sm font-semibold text-dark-100 leading-tight">{title}</h2>
          {subtitle && <p className="text-[11px] text-dark-500 leading-snug mt-0.5">{subtitle}</p>}
        </div>
        <button
          onClick={onClose}
          className="p-1.5 -m-1 rounded-md text-dark-400 hover:text-white hover:bg-dark-800 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-lens-500/50"
          title="Close"
          aria-label={`Close ${title}`}
          data-testid="panel-close"
        >
          <X size={16} />
        </button>
      </div>
      {toolbar && <div className="px-4 py-3 border-b border-dark-700/80 shrink-0">{toolbar}</div>}
      <div className="flex-1 min-h-0 overflow-y-auto overscroll-contain">{children}</div>
    </section>
  )
}

// Loading, empty and error states shared by the docked lists.
export function PanelMessage({ tone = 'muted', children }: { tone?: 'muted' | 'error'; children: ReactNode }) {
  return (
    <p className={`px-4 py-6 text-center text-xs leading-relaxed ${tone === 'error' ? 'text-red-300' : 'text-dark-500'}`}>
      {children}
    </p>
  )
}

export function PanelSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div className="p-3 space-y-1" aria-busy>
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="px-2 py-2 animate-pulse">
          <div className="h-3 rounded bg-dark-800 w-2/3" />
          <div className="h-2.5 mt-2 rounded bg-dark-800/70 w-1/2" />
        </div>
      ))}
    </div>
  )
}
