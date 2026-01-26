import { memo } from 'react'
import { BaseEdge, getBezierPath, EdgeLabelRenderer, Position } from '@xyflow/react'

export interface ConnectionEdgeData {
  port: number
  count: number
  bytesSent?: number
  bytesRecv?: number
  bytesSentRate?: number  // Bytes/second
  bytesRecvRate?: number  // Bytes/second
  packetsSent?: number
  packetsRecv?: number
  latency?: number
  highlighted?: 'incoming' | 'outgoing' | null  // For connection highlighting
}

interface ConnectionEdgeProps {
  id: string
  sourceX: number
  sourceY: number
  targetX: number
  targetY: number
  sourcePosition: Position
  targetPosition: Position
  data?: ConnectionEdgeData
  selected?: boolean
}

// Format bytes to human readable with /s suffix for rates
function formatRate(bytesPerSec: number): string {
  if (bytesPerSec === 0) return '0 B/s'
  const k = 1024
  const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const i = Math.floor(Math.log(bytesPerSec) / Math.log(k))
  const idx = Math.min(i, sizes.length - 1)
  return parseFloat((bytesPerSec / Math.pow(k, idx)).toFixed(1)) + ' ' + sizes[idx]
}

function ConnectionEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
  selected,
}: ConnectionEdgeProps) {
  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  const hasThroughput = (data?.bytesSentRate && data.bytesSentRate > 0) || 
                        (data?.bytesRecvRate && data.bytesRecvRate > 0)

  const isHighlighted = data?.highlighted

  // Calculate edge color based on highlighting and throughput
  const getEdgeColor = () => {
    // Highlighted connections
    if (isHighlighted === 'outgoing') return '#3b82f6' // Blue for outgoing
    if (isHighlighted === 'incoming') return '#22c55e' // Green for incoming
    
    if (selected) return '#a855f7' // Purple for selected
    
    // Dim non-highlighted edges when something is selected
    if (data?.highlighted === null) return '#374151' // Dimmed gray
    
    // Normal coloring based on throughput
    if (!hasThroughput) return '#4b5563'
    
    const maxRate = Math.max(data?.bytesSentRate || 0, data?.bytesRecvRate || 0)
    if (maxRate > 1024 * 1024) return '#ef4444' // > 1 MB/s = red (high traffic)
    if (maxRate > 100 * 1024) return '#f59e0b'  // > 100 KB/s = amber
    return '#6b7280' // gray (normal)
  }

  const getStrokeWidth = () => {
    if (isHighlighted) return 3.5
    if (selected) return 3
    if (data?.highlighted === null) return 1.5 // Dim other edges
    if (hasThroughput) return 2.5
    return 2
  }

  const getOpacity = () => {
    if (isHighlighted) return 1
    if (data?.highlighted === null) return 0.3 // Dim other edges
    return 1
  }

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: getEdgeColor(),
          strokeWidth: getStrokeWidth(),
          opacity: getOpacity(),
          transition: 'stroke 0.2s, stroke-width 0.2s, opacity 0.2s',
        }}
        className={isHighlighted ? 'animated-edge' : ''}
      />
      
      {/* Edge label */}
      <EdgeLabelRenderer>
        <div
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            pointerEvents: 'all',
            zIndex: isHighlighted ? 2000 : 1000,
            opacity: data?.highlighted === null ? 0.3 : 1,
            transition: 'opacity 0.2s',
          }}
          className={`
            px-2 py-1.5 rounded text-xs font-mono
            bg-dark-800 border shadow-lg
            ${isHighlighted === 'outgoing' ? 'border-blue-500 ring-1 ring-blue-500/30' : ''}
            ${isHighlighted === 'incoming' ? 'border-green-500 ring-1 ring-green-500/30' : ''}
            ${!isHighlighted ? 'border-dark-600' : ''}
            ${selected ? 'border-purple-500' : ''}
            transition-all duration-200
          `}
        >
          {/* Direction indicator when highlighted */}
          {isHighlighted && (
            <div className={`text-[10px] font-semibold mb-1 ${isHighlighted === 'outgoing' ? 'text-blue-400' : 'text-green-400'}`}>
              {isHighlighted === 'outgoing' ? '→ OUTGOING' : '← INCOMING'}
            </div>
          )}

          {/* Port and count */}
          <div className="flex items-center gap-1.5">
            <span className={`${isHighlighted ? 'text-dark-100' : 'text-dark-300'}`}>:{data?.port}</span>
            {data?.count && data.count > 1 && (
              <span className="text-dark-500">×{data.count}</span>
            )}
          </div>

          {/* Throughput rates */}
          {hasThroughput && (
            <div className="flex items-center gap-2 mt-1 pt-1 border-t border-dark-700">
              {data?.bytesSentRate !== undefined && data.bytesSentRate > 0 && (
                <span className="text-blue-400 flex items-center gap-0.5">
                  <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 10l7-7m0 0l7 7m-7-7v18" />
                  </svg>
                  {formatRate(data.bytesSentRate)}
                </span>
              )}
              {data?.bytesRecvRate !== undefined && data.bytesRecvRate > 0 && (
                <span className="text-green-400 flex items-center gap-0.5">
                  <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 14l-7 7m0 0l-7-7m7 7V3" />
                  </svg>
                  {formatRate(data.bytesRecvRate)}
                </span>
              )}
            </div>
          )}

          {data?.latency && (
            <span className="ml-1 text-yellow-500">{data.latency.toFixed(1)}ms</span>
          )}
        </div>
      </EdgeLabelRenderer>
    </>
  )
}

export default memo(ConnectionEdge)
