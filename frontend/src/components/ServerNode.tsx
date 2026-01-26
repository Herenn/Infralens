import { memo } from 'react'
import { NodeProps } from '@xyflow/react'
import { Server, Cpu, Network, Globe, MemoryStick } from 'lucide-react'
import { formatBytes } from '../types'

interface ServerNodeData {
  label: string
  serverName: string
  serviceCount: number
  totalConnections: number
  cpuPercent?: number
  memPercent?: number
  memUsed?: number
  memTotal?: number
}

/**
 * Returns color class based on resource usage percentage.
 * Green < 60%, Yellow 60-85%, Red > 85%
 */
function getUsageColor(percent: number | undefined): string {
  if (percent === undefined) return 'bg-slate-600'
  if (percent < 60) return 'bg-emerald-500'
  if (percent < 85) return 'bg-amber-500'
  return 'bg-red-500'
}

function getUsageTextColor(percent: number | undefined): string {
  if (percent === undefined) return 'text-slate-500'
  if (percent < 60) return 'text-emerald-400'
  if (percent < 85) return 'text-amber-400'
  return 'text-red-400'
}

function ServerNode({ data }: NodeProps<ServerNodeData>) {
  const { 
    label, 
    serverName, 
    serviceCount, 
    totalConnections,
    cpuPercent,
    memPercent,
    memUsed,
    memTotal,
  } = data
  
  const isExternal = serverName === 'External Network'
  const hasMetrics = cpuPercent !== undefined || memPercent !== undefined

  return (
    <div
      className={`
        w-full h-full
        rounded-2xl border-2 border-dashed
        ${isExternal 
          ? 'border-amber-700/40 bg-gradient-to-b from-amber-950/30 to-amber-950/10' 
          : 'border-slate-600/50 bg-gradient-to-b from-slate-900/60 to-slate-900/30'
        }
      `}
      style={{
        pointerEvents: 'none',
        padding: '12px',
      }}
    >
      {/* Server Header */}
      <div 
        className={`
          flex items-center gap-3 px-3 py-2 rounded-lg
          ${isExternal 
            ? 'bg-amber-900/20 border border-amber-800/30' 
            : 'bg-slate-800/50 border border-slate-700/30'
          }
        `}
        style={{ pointerEvents: 'auto' }}
      >
        <div className={`
          p-1.5 rounded-md
          ${isExternal 
            ? 'bg-amber-800/40' 
            : 'bg-slate-700/50'
          }
        `}>
          {isExternal 
            ? <Globe size={16} className="text-amber-400" />
            : <Server size={16} className="text-slate-400" />
          }
        </div>
        <div className="flex-1 min-w-0">
          <h3 className={`
            font-bold text-xs uppercase tracking-widest truncate
            ${isExternal ? 'text-amber-300' : 'text-slate-300'}
          `}>
            {serverName || label}
          </h3>
        </div>
        {/* Compact stats in header */}
        <div className="flex items-center gap-2 text-[10px] text-slate-500">
          <span className="flex items-center gap-1">
            <Cpu size={10} className="text-blue-400" />
            {serviceCount}
          </span>
          <span className="flex items-center gap-1">
            <Network size={10} className="text-green-400" />
            {totalConnections}
          </span>
        </div>
      </div>

      {/* Resource Metrics - Only show for non-external nodes with metrics */}
      {!isExternal && (
        <div 
          className="mt-2 px-2 py-2 rounded-lg bg-slate-800/30 border border-slate-700/20"
          style={{ pointerEvents: 'auto' }}
        >
          {/* CPU Bar */}
          <div className="mb-2">
            <div className="flex items-center justify-between mb-1">
              <span className="flex items-center gap-1.5 text-[10px] text-slate-400">
                <Cpu size={10} className="text-blue-400" />
                CPU
              </span>
              <span className={`text-[10px] font-mono ${getUsageTextColor(cpuPercent)}`}>
                {cpuPercent !== undefined ? `${cpuPercent.toFixed(1)}%` : '--'}
              </span>
            </div>
            <div className="h-1.5 bg-slate-700/50 rounded-full overflow-hidden">
              <div 
                className={`h-full rounded-full transition-all duration-500 ${getUsageColor(cpuPercent)}`}
                style={{ width: `${Math.min(cpuPercent ?? 0, 100)}%` }}
              />
            </div>
          </div>

          {/* Memory Bar */}
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="flex items-center gap-1.5 text-[10px] text-slate-400">
                <MemoryStick size={10} className="text-purple-400" />
                MEM
              </span>
              <span className={`text-[10px] font-mono ${getUsageTextColor(memPercent)}`}>
                {memPercent !== undefined 
                  ? `${memPercent.toFixed(1)}%` 
                  : '--'
                }
                {memUsed !== undefined && memTotal !== undefined && (
                  <span className="text-slate-500 ml-1">
                    ({formatBytes(memUsed)})
                  </span>
                )}
              </span>
            </div>
            <div className="h-1.5 bg-slate-700/50 rounded-full overflow-hidden">
              <div 
                className={`h-full rounded-full transition-all duration-500 ${getUsageColor(memPercent)}`}
                style={{ width: `${Math.min(memPercent ?? 0, 100)}%` }}
              />
            </div>
          </div>
        </div>
      )}

      {/* External Network - simple indicator */}
      {isExternal && (
        <div className="mt-2 px-2 py-1.5 rounded-lg bg-amber-900/10 border border-amber-800/10">
          <span className="text-[9px] text-amber-500/60 uppercase tracking-wider">
            External Endpoints
          </span>
        </div>
      )}

      {/* Visual hint for the content area - child nodes render here */}
      <div 
        className={`
          mt-3 flex-1 rounded-lg border border-dashed
          ${isExternal 
            ? 'border-amber-800/20' 
            : 'border-slate-700/20'
          }
        `}
        style={{ 
          minHeight: 'calc(100% - 150px)', // Account for header + metrics bars
          pointerEvents: 'none',
        }}
      />
    </div>
  )
}

export default memo(ServerNode)
