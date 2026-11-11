import { memo, useContext } from 'react'
import { BaseEdge, EdgeLabelRenderer } from '@xyflow/react'
import { routeEdge } from '../utils/edgeRoute'
import { rateStep } from '../utils/layout'
import { EdgeSettingsContext } from './edgeSettings'

export type EdgeFocus = 'outgoing' | 'incoming' | 'impact' | 'match' | null

export interface ConnectionEdgeData {
  port: number
  protocol?: string // "tcp" (default) or "udp"
  count: number
  bytesSent?: number
  bytesRecv?: number
  bytesSentRate?: number  // Bytes/second
  bytesRecvRate?: number  // Bytes/second
  latency?: number
  waypoints?: [number, number][]
  // Set by App for edges that stand out (hover, selection, blast radius).
  highlighted?: EdgeFocus
  // Set by App when this edge's label is shown, at the spot placed for it.
  label?: { x: number; y: number; text: string }
}

interface ConnectionEdgeProps {
  id: string
  sourceX: number
  sourceY: number
  targetX: number
  targetY: number
  data?: ConnectionEdgeData
}

const BASE_WIDTH = [1.25, 1.5, 1.9, 2.4, 3]
// Flow speed by traffic step: busier edges flow faster.
const FLOW_SECONDS = [0, 2.6, 2, 1.5, 1.1]

const TONES: Record<Exclude<EdgeFocus, null> | 'base', { stroke: string; marker: string; label: string }> = {
  base: { stroke: '#526077', marker: 'url(#il-arrow-base)', label: 'border-dark-600 text-dark-300' },
  outgoing: { stroke: '#60a5fa', marker: 'url(#il-arrow-out)', label: 'border-blue-500/60 text-blue-200' },
  incoming: { stroke: '#4ade80', marker: 'url(#il-arrow-in)', label: 'border-lens-500/60 text-lens-200' },
  impact: { stroke: '#f59e0b', marker: 'url(#il-arrow-impact)', label: 'border-amber-500/60 text-amber-200' },
  match: { stroke: '#94a3b8', marker: 'url(#il-arrow-match)', label: 'border-dark-500 text-dark-200' },
}

function ConnectionEdge({ id, sourceX, sourceY, targetX, targetY, data }: ConnectionEdgeProps) {
  const { animate } = useContext(EdgeSettingsContext)
  const { path } = routeEdge(sourceX, sourceY, targetX, targetY, data?.waypoints)

  const step = rateStep(Math.max(data?.bytesSentRate || 0, data?.bytesRecvRate || 0))
  const focus = data?.highlighted || null
  const tone = TONES[focus || 'base']
  const isUdp = data?.protocol === 'udp'
  const width = BASE_WIDTH[step] + (focus ? 0.75 : 0)

  return (
    <>
      <BaseEdge
        id={id}
        path={path}
        markerEnd={tone.marker}
        style={{
          stroke: tone.stroke,
          strokeWidth: width,
          strokeDasharray: isUdp ? '5 4' : undefined,
        }}
      />
      {animate && step > 0 && (
        <path
          d={path}
          fill="none"
          className="il-edge-flow"
          style={{
            stroke: focus ? tone.stroke : '#94a3b8',
            strokeWidth: width + 0.6,
            animationDuration: `${FLOW_SECONDS[step]}s`,
          }}
        />
      )}

      {data?.label && (
        <EdgeLabelRenderer>
          <div
            className={`il-edge-label nodrag nopan absolute w-max px-1.5 py-0.5 rounded-md border bg-dark-900/95
              font-mono text-[10.5px] leading-[15px] whitespace-pre text-center shadow-md shadow-black/40 pointer-events-none ${tone.label}`}
            style={{ transform: `translate(-50%, -50%) translate(${data.label.x}px, ${data.label.y}px)`, zIndex: 1000 }}
          >
            {data.label.text}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

export default memo(ConnectionEdge)
