import { memo } from 'react'
import { Server, Globe, LogIn, CircleDashed } from 'lucide-react'
import type { GroupKind } from '../utils/layout'

export interface ServerNodeData {
  label: string
  serverName: string
  kind?: GroupKind
  serviceCount: number
  totalConnections: number
  cpuPercent?: number
  memPercent?: number
  memUsed?: number
  memTotal?: number
}

interface ServerNodeProps {
  data: ServerNodeData
}

function usageColor(percent: number): string {
  if (percent < 60) return 'bg-emerald-500'
  if (percent < 85) return 'bg-amber-500'
  return 'bg-red-500'
}

function Meter({ label, percent }: { label: string; percent: number }) {
  return (
    <span className="flex items-center gap-1.5" title={`${label} ${percent.toFixed(1)}%`}>
      <span className="text-[10px] font-medium text-dark-500">{label}</span>
      <span className="w-8 h-1 rounded-full bg-dark-700 overflow-hidden">
        <span
          className={`block h-full rounded-full transition-[width] duration-500 ${usageColor(percent)}`}
          style={{ width: `${Math.min(percent, 100)}%` }}
        />
      </span>
      <span className="w-6 text-[10px] font-mono text-dark-400 tabular-nums">{Math.round(percent)}%</span>
    </span>
  )
}

const KIND_STYLE: Record<GroupKind, { box: string; title: string; icon: typeof Server; iconClass: string }> = {
  host: {
    box: 'border-dark-700/80 bg-dark-900/40',
    title: 'text-dark-300',
    icon: Server,
    iconClass: 'text-dark-400',
  },
  clients: {
    box: 'border-sky-800/50 bg-sky-950/20',
    title: 'text-sky-300/90',
    icon: LogIn,
    iconClass: 'text-sky-400',
  },
  external: {
    box: 'border-amber-800/40 bg-amber-950/15',
    title: 'text-amber-300/90',
    icon: Globe,
    iconClass: 'text-amber-400',
  },
  unmonitored: {
    box: 'border-dashed border-dark-600/70 bg-dark-900/20',
    title: 'text-dark-400',
    icon: CircleDashed,
    iconClass: 'text-dark-500',
  },
}

// A group box: a monitored host, or one of the groups of addresses outside
// them (inbound clients, external services, unmonitored hosts). Clicks pass
// through the box to the canvas; only the title bar is interactive.
function ServerNode({ data }: ServerNodeProps) {
  const { label, serverName, serviceCount, cpuPercent, memPercent } = data
  const kind = data.kind || 'host'
  const style = KIND_STYLE[kind]
  const Icon = style.icon
  const hasMetrics = kind === 'host' && cpuPercent !== undefined && memPercent !== undefined

  return (
    <div className={`il-group w-full h-full rounded-xl border ${style.box}`} style={{ pointerEvents: 'none' }}>
      <div className="h-[50px] px-3 pt-2 border-b border-white/[0.04] cursor-pointer" style={{ pointerEvents: 'auto' }}>
        <div className="flex items-center gap-2 h-4">
          <Icon size={13} className={`shrink-0 ${style.iconClass}`} />
          <h3 className={`min-w-0 truncate text-[11px] font-semibold uppercase tracking-[0.08em] ${style.title}`}>
            {serverName || label}
          </h3>
          <span className="shrink-0 ml-auto text-[10px] text-dark-500 tabular-nums" title={`${serviceCount} service${serviceCount === 1 ? '' : 's'}`}>
            {serviceCount}
          </span>
        </div>
        <div className="mt-1.5 h-4 flex items-center gap-2.5 text-[10px] text-dark-500">
          {hasMetrics ? (
            <>
              <Meter label="CPU" percent={cpuPercent!} />
              <Meter label="MEM" percent={memPercent!} />
            </>
          ) : (
            <span className="truncate">{SUBTITLE[kind]}</span>
          )}
        </div>
      </div>
    </div>
  )
}

const SUBTITLE: Record<GroupKind, string> = {
  host: 'No live host metrics',
  clients: 'Call into your services',
  external: 'Called by your services',
  unmonitored: 'No agent reports on these addresses',
}

export default memo(ServerNode)
