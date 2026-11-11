import { ReactNode } from 'react'
import { Radar, PiggyBank, Flame, Unlink, Trash2, GitCompare, History } from 'lucide-react'
import { PanelId } from './dock'

interface SideRailProps {
  open: PanelId | null
  onToggle: (id: PanelId) => void
  // Unread architecture changes; null hides Changes (feed unavailable).
  changesUnread: number | null
  // History-backed panels need recorded history.
  historyAvailable: boolean
  historyOpen: boolean
  viewingPast: boolean
  onToggleHistory: () => void
}

// Every secondary panel opens from here, into the same dock, one at a time,
// and the buttons never move.
export default function SideRail({
  open, onToggle, changesUnread, historyAvailable, historyOpen, viewingPast, onToggleHistory,
}: SideRailProps) {
  return (
    <nav
      className="w-[68px] shrink-0 flex flex-col items-center gap-1 py-3 border-r border-dark-800 bg-dark-900/80 z-30"
      aria-label="Panels"
    >
      {changesUnread !== null && (
        <RailButton
          id="changes" label="Changes" title="Changes: architecture drift as it happens"
          icon={<Radar size={18} />} active={open === 'changes'} onClick={() => onToggle('changes')}
          badge={changesUnread > 0 && open !== 'changes' ? (changesUnread > 99 ? '99+' : String(changesUnread)) : null}
        />
      )}
      <RailButton
        id="idle" label="Idle spend" title="Idle spend (beta): what services nothing talks to are costing"
        icon={<PiggyBank size={18} />} active={open === 'idle'} onClick={() => onToggle('idle')}
      />
      <div className="w-8 h-px bg-dark-800 my-1.5" aria-hidden />
      <RailButton
        id="critical" label="Critical" title="Most critical: ranked by blast radius"
        icon={<Flame size={18} />} active={open === 'critical'} onClick={() => onToggle('critical')}
      />
      <RailButton
        id="orphans" label="Orphans" title="Orphan services: no connections at all"
        icon={<Unlink size={18} />} active={open === 'orphans'} onClick={() => onToggle('orphans')}
      />
      {historyAvailable && (
        <>
          <RailButton
            id="stale" label="Stale" title="Decommission candidates: services not seen recently"
            icon={<Trash2 size={18} />} active={open === 'stale'} onClick={() => onToggle('stale')}
          />
          <RailButton
            id="compare" label="Compare" title="Compare to now: what changed since an earlier moment"
            icon={<GitCompare size={18} />} active={open === 'compare'} onClick={() => onToggle('compare')}
          />
          <div className="w-8 h-px bg-dark-800 my-1.5" aria-hidden />
          <RailButton
            id="history" label="History" title="Time travel: the map at any past moment"
            icon={<History size={18} />} active={historyOpen} onClick={onToggleHistory}
            dot={viewingPast}
          />
        </>
      )}
    </nav>
  )
}

function RailButton({ id, label, title, icon, active, onClick, badge, dot }: {
  id: string
  label: string
  title: string
  icon: ReactNode
  active: boolean
  onClick: () => void
  badge?: string | null
  dot?: boolean
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      aria-label={badge ? `${title} (${badge} new)` : title}
      aria-pressed={active}
      data-rail={id}
      className={`group relative w-14 py-2 rounded-lg flex flex-col items-center gap-1 transition-colors
        focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-lens-500/50
        ${active ? 'bg-lens-500/10 text-lens-300' : 'text-dark-400 hover:text-dark-100 hover:bg-dark-800/70'}`}
    >
      {active && <span className="absolute -left-1.5 top-2 bottom-2 w-0.5 rounded-full bg-lens-400" aria-hidden />}
      <span className="relative">
        {icon}
        {badge && (
          <span className="absolute -top-2 -right-3 min-w-[16px] h-4 px-1 rounded-full bg-lens-500 text-dark-950
            text-[9px] font-bold leading-4 text-center shadow-[0_0_0_2px_rgba(15,23,42,1)]">
            {badge}
          </span>
        )}
        {dot && <span className="absolute -top-1 -right-1.5 w-2 h-2 rounded-full bg-amber-400 shadow-[0_0_0_2px_rgba(15,23,42,1)]" />}
      </span>
      <span className="text-[10px] font-medium leading-none whitespace-nowrap">{label}</span>
    </button>
  )
}
