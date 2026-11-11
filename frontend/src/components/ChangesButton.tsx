import { Radar } from 'lucide-react'

interface ChangesButtonProps {
  open: boolean
  unread: number
  onToggle: () => void
}

// Header entry point for the architecture drift feed, with an unread badge.
export default function ChangesButton({ open, unread, onToggle }: ChangesButtonProps) {
  const label = unread > 99 ? '99+' : String(unread)
  return (
    <button
      onClick={onToggle}
      className={`
        relative flex shrink-0 items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium whitespace-nowrap transition-all
        ${open
          ? 'bg-lens-500/15 text-lens-300 border border-lens-500/40'
          : 'bg-dark-800 text-dark-300 border border-dark-700 hover:text-dark-100 hover:border-dark-600'
        }
      `}
      title={unread > 0 ? `${unread} new architecture change${unread === 1 ? '' : 's'}` : 'Architecture changes'}
      aria-label={unread > 0 ? `Changes, ${unread} unread` : 'Changes'}
    >
      <Radar size={14} className={unread > 0 && !open ? 'text-lens-400' : ''} />
      <span>Changes</span>
      {unread > 0 && !open && (
        <span className="absolute -top-1.5 -right-1.5 min-w-[18px] h-[18px] px-1 rounded-full
          bg-lens-500 text-dark-950 text-[10px] font-bold leading-[18px] text-center
          shadow-[0_0_0_2px_rgba(15,23,42,1),0_0_12px_rgba(34,197,94,0.55)]">
          {label}
        </span>
      )}
    </button>
  )
}
