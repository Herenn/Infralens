import { useCallback, useEffect, useMemo, useState } from 'react'
import { Edge, Node } from '@xyflow/react'
import { DriftEvent } from '../types'

// Focus on a drift event's edge in whatever graph is displayed (live, or the
// instant time travel is showing): its endpoints join the highlight set the
// graph already dims and zooms to, and the edge itself is emphasised.
export function useDriftFocus(nodes: Node[], enabled: boolean) {
  const [focus, setFocus] = useState<DriftEvent | null>(null)

  // Leaving the Changes panel leaves its focus behind.
  useEffect(() => {
    if (!enabled) setFocus(null)
  }, [enabled])

  const nodeIds = useMemo(() => new Set(nodes.map(n => n.id)), [nodes])

  // Keyed on which endpoints are present rather than on the node array, so
  // live updates (a new array every flush) don't re-trigger zooming.
  const presentKey = focus
    ? [focus.source_id, focus.target_id].filter(id => nodeIds.has(id)).join('\n')
    : ''
  const highlightIds = useMemo(
    () => (presentKey ? new Set(presentKey.split('\n')) : null),
    [presentKey]
  )

  const isOnGraph = useCallback(
    (ev: DriftEvent) => nodeIds.has(ev.source_id) && nodeIds.has(ev.target_id),
    [nodeIds]
  )

  const decorateEdges = useCallback(<E extends Edge>(edges: E[]): E[] => {
    if (!focus || !presentKey) return edges
    return edges.map(edge => {
      const match = edge.id === focus.connection_id ||
        (edge.source === focus.source_id && edge.target === focus.target_id)
      return { ...edge, data: { ...edge.data, highlighted: match ? 'outgoing' : null } }
    })
  }, [focus, presentKey])

  const clear = useCallback(() => setFocus(null), [])

  return { focus, setFocus, clear, highlightIds, isOnGraph, decorateEdges }
}
