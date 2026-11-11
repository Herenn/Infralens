import { useCallback, useEffect, useState, useRef, useMemo } from 'react'
import {
  ReactFlow,
  ReactFlowInstance,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  Node,
  Edge,
  NodeChange,
  BackgroundVariant,
  Viewport,
  getNodesBounds,
  getViewportForBounds,
} from '@xyflow/react'
import { toPng } from 'html-to-image'
import { Loader2, Radio } from 'lucide-react'
import '@xyflow/react/dist/style.css'

import ServiceNode from './components/ServiceNode'
import ServerNode from './components/ServerNode'
import ConnectionEdge, { EdgeFocus } from './components/ConnectionEdge'
import { EdgeSettingsContext } from './components/edgeSettings'
import ServiceDrawer from './components/ServiceDrawer'
import Header from './components/Header'
import SideRail from './components/SideRail'
import TimelineScrubber, { HistoryRange } from './components/TimelineScrubber'
import DecommissionPanel from './components/DecommissionPanel'
import OrphansPanel from './components/OrphansPanel'
import CriticalityPanel from './components/CriticalityPanel'
import DiffPanel from './components/DiffPanel'
import ChangesPanel from './components/ChangesPanel'
import IdleSpendPanel from './components/IdleSpendPanel'
import { HistoryBanner, ImpactPill } from './components/MapOverlays'
import { DOCK_WIDTH, PanelId } from './components/dock'
import { Service, Topology } from './types'
import { useWebSocket } from './hooks/useWebSocket'
import { useDriftFeed } from './hooks/useDriftFeed'
import { useDriftFocus } from './hooks/useDriftFocus'
import { usePrefersReducedMotion } from './hooks/usePrefersReducedMotion'
import { apiUrl } from './lib/api'
import {
  CARD_H, CARD_W, assignGroups, layoutGraph, structureKey, updateNodeData, updateEdgeData,
} from './utils/layout'
import { routeEdge } from './utils/edgeRoute'
import { labelWidth, placeLabels, Rect } from './utils/edgeLabels'
import { nameFromServiceId, serviceLabel, unionHostnames } from './utils/hostnames'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AppNode = Node<any>
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AppEdge = Edge<any>

// Traversal depth for the blast-radius view. Must match the backend's
// maxImpactDepth, which is what /graph/criticality always ranks by: the
// endpoint's own default is 5 (tuned for a shallow, readable lookup), so
// leaving it off made the "Most critical" panel say "breaks 9 services" while
// clicking through highlighted 5 - the same question answered twice, at two
// depths, in one session.
const IMPACT_DEPTH = 20

// How often the recorded-history window is refreshed. Slow: it only bounds a
// timeline control, and the data behind it moves on the scale of minutes.
const HISTORY_RANGE_POLL_INTERVAL = 30000

// Live updates that add or remove services re-lay the graph out at most this
// often; in between, only data is updated.
const MIN_LAYOUT_INTERVAL = 5000

// Flow animation on edges costs a repaint per animated edge per frame; past
// this many edges it is off (measured: see the v3.1 UI notes in the PR).
const FLOW_ANIMATION_MAX_EDGES = 250
// Past this many nodes, only what is in the viewport is rendered.
const VISIBLE_ONLY_MIN_NODES = 300
// Most edge labels shown at once (hover or selection of a busy service).
const MAX_LABELS = 32

const DRAWER_WIDTH = 400
const MINIMAP_W = 168
const MINIMAP_H = 108
const EDGE = 12 // margin of the map's floating controls

const nodeTypes = {
  service: ServiceNode,
  server: ServerNode,
}

const edgeTypes = {
  connection: ConnectionEdge,
}

// Types for selected node state
type SelectedNodeType = 'service' | 'server' | null

interface SelectedNodeState {
  type: SelectedNodeType
  service: Service | null
  serverData: {
    serverName: string
    serviceCount: number
    totalConnections: number
    cpuPercent?: number
    memPercent?: number
    memUsed?: number
    memTotal?: number
  } | null
  ports: number[]
}

interface FilterState {
  hideLocalhost: boolean
  minConnections: number
  collapseExternal: boolean
}

// Helper to check if an ID is localhost
function isLocalhostId(id: string): boolean {
  return id.startsWith('127.') || 
         id.startsWith('::1') || 
         id.includes('/127.') ||
         id.includes('localhost')
}

// Filter topology based on filter settings
function filterTopology(topology: Topology | null, filters: FilterState): Topology | null {
  if (!topology) return null

  let services = [...topology.services]
  let connections = [...topology.connections]

  // 1. Hide localhost traffic - remove ALL localhost services and connections
  if (filters.hideLocalhost) {
    // Remove all localhost services
    services = services.filter(svc => !isLocalhostId(svc.id))
    
    // Remove connections involving localhost
    const validServiceIds = new Set(services.map(s => s.id))
    connections = connections.filter(conn => 
      !isLocalhostId(conn.source_id) && 
      !isLocalhostId(conn.target_id) &&
      validServiceIds.has(conn.source_id) &&
      validServiceIds.has(conn.target_id)
    )
  }

  // 2. Minimum connections filter
  if (filters.minConnections > 1) {
    // Count connections per service
    const connectionCounts = new Map<string, number>()
    connections.forEach(conn => {
      connectionCounts.set(conn.source_id, (connectionCounts.get(conn.source_id) || 0) + 1)
      connectionCounts.set(conn.target_id, (connectionCounts.get(conn.target_id) || 0) + 1)
    })
    
    // Filter services with enough connections
    services = services.filter(svc => {
      const count = connectionCounts.get(svc.id) || 0
      return count >= filters.minConnections
    })
    
    // Filter connections to only include filtered services
    const filteredServiceIds = new Set(services.map(s => s.id))
    connections = connections.filter(conn => 
      filteredServiceIds.has(conn.source_id) && filteredServiceIds.has(conn.target_id)
    )
  }

  // 3. Collapse external endpoints (aggregate by tech type) - STABLE version
  if (filters.collapseExternal) {
    const externalServices = services.filter(s => !s.node || s.node === 'External Network')
    const internalServices = services.filter(s => s.node && s.node !== 'External Network')
    
    // Group external services by tech type (use stable key)
    const externalByTech = new Map<string, Service[]>()
    externalServices.forEach(svc => {
      // Use tech or type, normalized to lowercase
      const key = (svc.tech || svc.type || 'external').toLowerCase().replace(/\s+/g, '-')
      if (!externalByTech.has(key)) {
        externalByTech.set(key, [])
      }
      externalByTech.get(key)!.push(svc)
    })
    
    // Create aggregated external services with STABLE IDs
    const aggregatedExternal: Service[] = []
    const idMapping = new Map<string, string>() // old id -> new aggregated id
    
    // Sort keys for stable ordering
    const sortedKeys = Array.from(externalByTech.keys()).sort()
    
    sortedKeys.forEach(key => {
      const svcs = externalByTech.get(key)!
      if (svcs.length === 1) {
        // Keep single services as-is
        aggregatedExternal.push(svcs[0])
      } else {
        // Create aggregated service with STABLE id (don't include count in id)
        const aggregatedId = `agg-external-${key}`
        const displayTech = svcs[0].tech || svcs[0].type || 'External'
        const aggregatedSvc: Service = {
          ...svcs[0],
          id: aggregatedId,
          name: `${displayTech} (${svcs.length})`,
          display_name: `${displayTech} (${svcs.length})`,
          original_id: svcs[0].id,  // Keep first service's ID for backend lookups
          aggregated_count: svcs.length,
          // Not one destination's name: keep every name searchable, label none.
          hostname: undefined,
          hostnames: unionHostnames(svcs),
        }
        aggregatedExternal.push(aggregatedSvc)
        
        // Map old IDs to new aggregated ID
        svcs.forEach(s => idMapping.set(s.id, aggregatedId))
      }
    })
    
    services = [...internalServices, ...aggregatedExternal]
    
    // Update connection source/target IDs
    connections = connections.map(conn => {
      const newSourceId = idMapping.get(conn.source_id) || conn.source_id
      const newTargetId = idMapping.get(conn.target_id) || conn.target_id
      // Create stable connection ID
      const newConnId = `${newSourceId}->${newTargetId}:${conn.port}`
      return {
        ...conn,
        id: newConnId,
        source_id: newSourceId,
        target_id: newTargetId,
      }
    })
    
    // Deduplicate connections after remapping (aggregate counts/bytes)
    const uniqueConnections = new Map<string, typeof connections[0]>()
    connections.forEach(conn => {
      const key = conn.id
      const existing = uniqueConnections.get(key)
      if (!existing) {
        uniqueConnections.set(key, { ...conn })
      } else {
        // Merge stats
        existing.count += conn.count
        existing.bytes_sent = (existing.bytes_sent || 0) + (conn.bytes_sent || 0)
        existing.bytes_recv = (existing.bytes_recv || 0) + (conn.bytes_recv || 0)
        existing.bytes_sent_rate = Math.max(existing.bytes_sent_rate || 0, conn.bytes_sent_rate || 0)
        existing.bytes_recv_rate = Math.max(existing.bytes_recv_rate || 0, conn.bytes_recv_rate || 0)
      }
    })
    connections = Array.from(uniqueConnections.values())
  }

  return {
    ...topology,
    services,
    connections,
  }
}

function formatRate(bytesPerSec: number): string {
  const k = 1024
  const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const i = Math.min(Math.floor(Math.log(bytesPerSec) / Math.log(k)), sizes.length - 1)
  const v = bytesPerSec / Math.pow(k, i)
  return `${v >= 100 ? Math.round(v) : parseFloat(v.toFixed(1))} ${sizes[i]}`
}

type ImpactDirection = 'upstream' | 'downstream'

function App() {
  const [nodes, setNodes, onNodesChange] = useNodesState<AppNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<AppEdge>([])
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [selectedServiceId, setSelectedServiceId] = useState<string | null>(null)
  const [selectedNode, setSelectedNode] = useState<SelectedNodeState>({
    type: null,
    service: null,
    serverData: null,
    ports: [],
  })
  const [stats, setStats] = useState({ services: 0, connections: 0 })
  const [searchQuery, setSearchQuery] = useState('')
  const [hoverNodeId, setHoverNodeId] = useState<string | null>(null)
  const [hoverEdgeId, setHoverEdgeId] = useState<string | null>(null)

  // Blast radius: which service is the current root, and the resulting set
  // of service IDs to highlight (null = no impact view active). It stays on
  // until cleared explicitly (the pill's Clear, or Esc) - closing the drawer
  // or clicking the canvas leaves it, so it can be looked at.
  const [impactRoot, setImpactRoot] = useState<{ id: string; direction: ImpactDirection } | null>(null)
  const [impactIds, setImpactIds] = useState<Set<string> | null>(null)
  const [filters, setFilters] = useState<FilterState>({
    hideLocalhost: false,
    minConnections: 1,
    collapseExternal: false,
  })
  const reactFlowInstanceRef = useRef<ReactFlowInstance<AppNode, AppEdge> | null>(null)
  const reducedMotion = usePrefersReducedMotion()

  // The one secondary panel open in the dock (see components/dock.ts), and
  // the service it focused on the graph, if any.
  const [panel, setPanel] = useState<PanelId | null>(null)
  const [panelFocusId, setPanelFocusId] = useState<string | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)

  const { topology, isConnected } = useWebSocket()

  // Topology history: viewing a past instant (timelineAt set) replaces the
  // live WebSocket-driven topology with a one-off fetch. historyRange is
  // null (hiding every history control) unless the backend has both history
  // enabled and at least one recorded interval.
  const [timelineAt, setTimelineAt] = useState<string | null>(null)
  const [historicalTopology, setHistoricalTopology] = useState<Topology | null>(null)
  const [historyRange, setHistoryRange] = useState<HistoryRange | null>(null)
  const [historyLoading, setHistoryLoading] = useState(false)

  const activeTopology = timelineAt ? historicalTopology : topology

  // Polled, not fetched once. On a fresh deployment history is empty at page
  // load, so a single fetch leaves `historyRange` null forever - and every
  // history control is gated on it, so the whole feature would stay invisible
  // until someone happened to reload. Polling also keeps `latest` from going
  // stale as new history accumulates during a long-lived session.
  useEffect(() => {
    let cancelled = false

    const loadRange = () => {
      fetch(apiUrl('/api/v1/topology/history/range'))
        .then(res => (res.ok ? res.json() : null))
        .then((data: { earliest: string | null; latest: string | null } | null) => {
          if (cancelled || !data?.earliest || !data?.latest) return
          // Only replace the object when a bound actually moved. A fresh
          // object every poll would re-render the timeline controls on a 30s
          // pulse for no reason, since the value is usually unchanged.
          setHistoryRange(prev =>
            prev && prev.earliest === data.earliest && prev.latest === data.latest
              ? prev
              : { earliest: data.earliest!, latest: data.latest! }
          )
        })
        .catch(() => {
          // History disabled or unreachable - the scrubber just stays hidden.
        })
    }

    loadRange()
    const timer = window.setInterval(loadRange, HISTORY_RANGE_POLL_INTERVAL)
    return () => { cancelled = true; window.clearInterval(timer) }
  }, [])

  useEffect(() => {
    if (!timelineAt) {
      setHistoricalTopology(null)
      return
    }
    let cancelled = false
    setHistoryLoading(true)
    fetch(apiUrl(`/api/v1/topology?at=${encodeURIComponent(timelineAt)}`))
      .then(res => {
        if (!res.ok) throw new Error(`Failed to load historical topology: ${res.status}`)
        return res.json()
      })
      .then((data: Topology) => {
        if (!cancelled) setHistoricalTopology(data)
      })
      .catch(err => {
        console.error('Failed to load historical topology:', err)
      })
      .finally(() => {
        if (!cancelled) setHistoryLoading(false)
      })
    return () => { cancelled = true }
  }, [timelineAt])

  // Apply filters to topology
  const filteredTopology = useMemo(() =>
    filterTopology(activeTopology, filters),
    [activeTopology, filters]
  )

  const serviceById = useMemo(
    () => new Map((filteredTopology?.services || []).map(s => [s.id, s])),
    [filteredTopology]
  )
  const liveServiceById = useMemo(
    () => new Map((topology?.services || []).map(s => [s.id, s])),
    [topology]
  )

  // Calculate filtered stats
  const filteredStats = useMemo(() => ({
    services: filteredTopology?.services.length || 0,
    connections: filteredTopology?.connections.length || 0,
  }), [filteredTopology])

  // Service IDs matching the search query (name, IP, tech, type, node)
  const searchMatchIds = useMemo(() => {
    const query = searchQuery.trim().toLowerCase()
    if (!query || !filteredTopology) return null

    const matches = new Set<string>()
    filteredTopology.services.forEach(svc => {
      const haystack = [svc.id, svc.name, svc.display_name, svc.resolved_name, svc.tech, svc.type, svc.pod_ip, svc.node, ...(svc.hostnames || []), svc.hostname]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
      if (haystack.includes(query)) {
        matches.add(svc.id)
      }
    })
    return matches
  }, [searchQuery, filteredTopology])

  // Fetch the blast radius whenever the root/direction changes.
  const [impactLoading, setImpactLoading] = useState(false)
  useEffect(() => {
    if (!impactRoot) {
      setImpactIds(null)
      return
    }
    let cancelled = false
    setImpactLoading(true)
    // Carry the viewed instant through: a blast radius asked for while scrubbed
    // to a past moment must be traversed over that graph, not today's.
    const at = timelineAt ? `&at=${encodeURIComponent(timelineAt)}` : ''
    fetch(apiUrl(`/api/v1/graph/impact?service=${encodeURIComponent(impactRoot.id)}&direction=${impactRoot.direction}&depth=${IMPACT_DEPTH}${at}`))
      .then(res => {
        if (!res.ok) throw new Error(`Failed to load impact: ${res.status}`)
        return res.json()
      })
      .then((data: Topology) => {
        if (cancelled) return
        const ids = new Set(data.services.map(s => s.id))
        ids.add(impactRoot.id)
        setImpactIds(ids)
      })
      .catch(err => {
        console.error('Failed to load impact:', err)
        if (!cancelled) setImpactRoot(null)
      })
      .finally(() => {
        if (!cancelled) setImpactLoading(false)
      })
    return () => { cancelled = true }
  }, [impactRoot, timelineAt])

  const handleShowImpact = useCallback((serviceId: string, direction: ImpactDirection) => {
    setImpactRoot({ id: serviceId, direction })
  }, [])

  const handleClearImpact = useCallback(() => {
    setImpactRoot(null)
  }, [])

  // Architecture drift feed ("Changes"): unread badge, and focusing a change
  // on the graph.
  const changesOpen = panel === 'changes'
  const driftFeed = useDriftFeed(changesOpen)
  const driftFocus = useDriftFocus(nodes, changesOpen)
  const { clear: clearDriftFocus } = driftFocus

  const panelFocusIds = useMemo(() => (panelFocusId ? new Set([panelFocusId]) : null), [panelFocusId])

  // What the graph lights up, most specific intent first: a service picked in
  // a panel, a blast radius, a focused change, search matches.
  const highlightIds = panelFocusIds || impactIds || driftFocus.highlightIds || searchMatchIds
  const highlightKind: 'panel' | 'impact' | 'drift' | 'search' | null =
    panelFocusIds ? 'panel' : impactIds ? 'impact' : driftFocus.highlightIds ? 'drift' : searchMatchIds ? 'search' : null

  const togglePanel = useCallback((id: PanelId) => {
    setPanel(cur => (cur === id ? null : id))
    setPanelFocusId(null)
  }, [])
  const closePanel = useCallback(() => {
    setPanel(null)
    setPanelFocusId(null)
  }, [])

  // ------------------------------------------------------------------------
  // Viewport: the map frames what it shows in the space panels leave free.
  // ------------------------------------------------------------------------

  const leftInset = panel ? DOCK_WIDTH + EDGE : 0
  const rightInset = drawerOpen ? DRAWER_WIDTH : 0
  const topInset = (timelineAt ? 52 : 0) + (impactRoot ? 52 : 0)
  const bottomInset = historyOpen && historyRange ? 110 : 0
  const padding = useMemo(() => ({
    top: `${24 + topInset}px`,
    left: `${24 + leftInset}px`,
    right: `${24 + rightInset}px`,
    bottom: `${24 + bottomInset}px`,
  }), [topInset, leftInset, rightInset, bottomInset]) as { top: `${number}px`; left: `${number}px`; right: `${number}px`; bottom: `${number}px` }
  const paddingRef = useRef(padding)
  paddingRef.current = padding

  // Whether the user has panned or zoomed since the map was last framed
  // automatically: then the map stops reframing itself on live changes.
  const userMovedRef = useRef(false)
  const [mapReady, setMapReady] = useState(false)

  const fitAll = useCallback((duration = 350) => {
    const instance = reactFlowInstanceRef.current
    if (!instance) return
    instance.fitView({ padding: paddingRef.current, duration, maxZoom: 1, minZoom: 0.05 })
    userMovedRef.current = false
  }, [])

  // ------------------------------------------------------------------------
  // Layout
  // ------------------------------------------------------------------------

  const prevStructureRef = useRef<string | null>(null)
  const prevFiltersRef = useRef<FilterState>(filters)
  const lastLayoutTimeRef = useRef<number>(0)
  // Which instant the last layout was built from, so a switch between live
  // and historical topology can bypass the debounce.
  const prevTimelineAtRef = useRef<string | null>(null)
  // Cards the user dragged: kept where they were put across re-layouts, as
  // long as they stay in the same group.
  const draggedRef = useRef<Map<string, { parentId?: string; position: { x: number; y: number } }>>(new Map())
  const [fitRequest, setFitRequest] = useState(0)

  useEffect(() => {
    if (!filteredTopology) return

    const groups = assignGroups(filteredTopology)
    const structure = structureKey(filteredTopology, groups)
    const filtersChanged = filters !== prevFiltersRef.current
    const structureChanged = structure !== prevStructureRef.current
    const now = Date.now()
    // The debounce exists to stop live WebSocket churn re-laying out the graph
    // constantly. It must not apply when the topology *source* changes (live
    // <-> historical, or a different instant): those produce a wholly
    // different node set exactly once, and nothing further arrives after a
    // historical fetch to trigger a later layout.
    const sourceChanged = timelineAt !== prevTimelineAtRef.current
    const firstLayout = prevStructureRef.current === null
    const canLayout = firstLayout || sourceChanged || filtersChanged || now - lastLayoutTimeRef.current > MIN_LAYOUT_INTERVAL

    if (structureChanged && canLayout) {
      const { nodes: newNodes, edges: newEdges } = layoutGraph(filteredTopology, groups)
      const restored = newNodes.map(node => {
        const dragged = draggedRef.current.get(node.id)
        return dragged && dragged.parentId === node.parentId ? { ...node, position: dragged.position } : node
      })
      setNodes(restored)
      setEdges(newEdges)
      prevStructureRef.current = structure
      prevFiltersRef.current = filters
      prevTimelineAtRef.current = timelineAt
      lastLayoutTimeRef.current = now
      // Reframe: always on a new source or filter; on live changes only if
      // the user has not moved the map themselves.
      if (firstLayout || sourceChanged || filtersChanged || !userMovedRef.current) setFitRequest(r => r + 1)
    } else {
      setNodes(currentNodes => updateNodeData(currentNodes, filteredTopology))
      setEdges(currentEdges => updateEdgeData(currentEdges, filteredTopology))
      if (!structureChanged) {
        prevTimelineAtRef.current = timelineAt
        prevFiltersRef.current = filters
      }
    }

    if (activeTopology) {
      setStats({
        services: activeTopology.services.length,
        connections: activeTopology.connections.length,
      })
    }
  }, [filteredTopology, filters, activeTopology, timelineAt, setNodes, setEdges])

  // Frame the graph once React Flow has the new nodes. The very first frame
  // is hidden until then, so the page never shows an unframed map.
  const mapReadyRef = useRef(false)
  useEffect(() => {
    if (fitRequest === 0) return
    const first = !mapReadyRef.current
    const t = window.setTimeout(() => {
      fitAll(first ? 0 : 350)
      if (first) {
        mapReadyRef.current = true
        window.requestAnimationFrame(() => setMapReady(true))
      }
    }, first ? 30 : 60)
    return () => window.clearTimeout(t)
  }, [fitRequest, fitAll])

  // Opening or closing a panel reframes the overview into the space left
  // free - unless the user is looking at something particular.
  const layoutKey = `${panel ? 'p' : ''}${historyOpen ? 'h' : ''}${topInset}`
  const prevLayoutKeyRef = useRef(layoutKey)
  useEffect(() => {
    if (prevLayoutKeyRef.current === layoutKey) return
    prevLayoutKeyRef.current = layoutKey
    if (!highlightIds && !userMovedRef.current && mapReady) fitAll()
  }, [layoutKey, highlightIds, fitAll, mapReady])

  // Zoom to the active highlight set (impact view, search matches, a focused
  // change or panel pick), and back to where the map was when it clears.
  const savedViewportRef = useRef<Viewport | null>(null)
  const highlightKey = highlightIds ? [...highlightIds].sort().join('\n') : ''
  useEffect(() => {
    const instance = reactFlowInstanceRef.current
    if (!instance) return
    if (!highlightKey) {
      const saved = savedViewportRef.current
      savedViewportRef.current = null
      if (saved) {
        const t = window.setTimeout(() => instance.setViewport(saved, { duration: 350 }), 150)
        return () => window.clearTimeout(t)
      }
      return
    }
    if (!savedViewportRef.current) savedViewportRef.current = instance.getViewport()
    const ids = highlightKey.split('\n')
    const timeout = window.setTimeout(() => {
      instance.fitView({
        nodes: ids.map(id => ({ id })),
        duration: 400,
        padding: {
          ...paddingRef.current,
          // A lone match keeps some of its surroundings in view.
          top: `${parseInt(paddingRef.current.top) + 40}px`,
          bottom: `${parseInt(paddingRef.current.bottom) + 40}px`,
        },
        maxZoom: ids.length === 1 ? 0.95 : 1,
        minZoom: 0.05,
      })
    }, 250) // debounce while typing
    return () => window.clearTimeout(timeout)
  }, [highlightKey, padding])

  // ------------------------------------------------------------------------
  // Selection and interaction
  // ------------------------------------------------------------------------

  const handleNodesChange = useCallback((changes: NodeChange<AppNode>[]) => {
    onNodesChange(changes)
    const ended: string[] = []
    changes.forEach(change => {
      if (change.type === 'position' && change.position) {
        draggedRef.current.set(change.id, { parentId: nodesRef.current.get(change.id)?.parentId, position: change.position })
        if (change.dragging === false) ended.push(change.id)
      }
    })
    // A moved card's edges no longer follow the layout's routes.
    if (ended.length) {
      const moved = new Set(ended)
      setEdges(eds => eds.map(e => (moved.has(e.source) || moved.has(e.target)) && e.data?.waypoints
        ? { ...e, data: { ...e.data, waypoints: undefined } }
        : e))
    }
  }, [onNodesChange, setEdges])

  const nodesRef = useRef<Map<string, AppNode>>(new Map())
  nodesRef.current = useMemo(() => new Map(nodes.map(n => [n.id, n])), [nodes])

  // Where the map was before the drawer moved it, to go back on close.
  const drawerViewportRef = useRef<Viewport | null>(null)

  // Keep a clicked card visible beside the drawer.
  const revealBesideDrawer = useCallback((node: Node) => {
    const instance = reactFlowInstanceRef.current
    if (!instance) return
    const parent = node.parentId ? nodesRef.current.get(node.parentId) : undefined
    const abs = { x: (parent?.position.x || 0) + node.position.x, y: (parent?.position.y || 0) + node.position.y }
    const vp = instance.getViewport()
    const flowEl = document.querySelector('.react-flow') as HTMLElement | null
    const width = flowEl?.clientWidth || window.innerWidth
    const right = vp.x + (abs.x + CARD_W) * vp.zoom
    const limit = width - DRAWER_WIDTH - 32
    if (right > limit) {
      if (!drawerViewportRef.current) drawerViewportRef.current = vp
      instance.setViewport({ ...vp, x: vp.x - (right - limit) }, { duration: 300 })
    }
  }, [])

  const closeDrawer = useCallback(() => {
    setDrawerOpen(false)
    setSelectedServiceId(null)
    const saved = drawerViewportRef.current
    drawerViewportRef.current = null
    if (saved && !savedViewportRef.current) reactFlowInstanceRef.current?.setViewport(saved, { duration: 300 })
  }, [])

  // Handle node click - open drawer with appropriate data
  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    if (node.type === 'server') {
      const data = node.data as SelectedNodeState['serverData'] & { kind?: string }
      if (!data || data.kind !== 'host') return
      setSelectedNode({ type: 'server', service: null, serverData: data, ports: [] })
      setSelectedServiceId(null)
      setDrawerOpen(true)
    } else if (node.type === 'service') {
      // From the original topology first, then the filtered one (aggregated services).
      const service = activeTopology?.services.find((s) => s.id === node.id) ||
        filteredTopology?.services.find((s) => s.id === node.id)
      const ports = (node.data as { ports?: number[] })?.ports || []
      setSelectedNode({ type: 'service', service: service || null, serverData: null, ports })
      setSelectedServiceId(node.id)
      setDrawerOpen(true)
      revealBesideDrawer(node)
    }
  }, [activeTopology, filteredTopology, revealBesideDrawer])

  // Handle edge click - show the source service, with this edge's port
  const onEdgeClick = useCallback((_: React.MouseEvent, edge: Edge) => {
    const sourceService = activeTopology?.services.find((s) => s.id === edge.source) ||
      filteredTopology?.services.find((s) => s.id === edge.source)
    if (sourceService) {
      const edgePort = edge.data?.port as number | undefined
      setSelectedNode({ type: 'service', service: sourceService, serverData: null, ports: edgePort ? [edgePort] : [] })
      setSelectedServiceId(edge.source)
      setDrawerOpen(true)
    }
  }, [activeTopology, filteredTopology])

  // Canvas click: close the drawer and clear transient focus. A blast radius
  // and a search stay: they have their own way out.
  const onPaneClick = useCallback(() => {
    closeDrawer()
    clearDriftFocus()
    setPanelFocusId(null)
  }, [clearDriftFocus, closeDrawer])

  // Esc steps back out: blast radius first, then the drawer, then a panel.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      const t = e.target as HTMLElement
      if (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA') return
      if (impactRoot) setImpactRoot(null)
      else if (drawerOpen) closeDrawer()
      else if (panel) closePanel()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [impactRoot, drawerOpen, panel, closeDrawer, closePanel])

  // ------------------------------------------------------------------------
  // What the graph draws: highlight classes, lit edges and their labels
  // ------------------------------------------------------------------------

  const focusNodeId = hoverNodeId || (drawerOpen ? selectedServiceId : null) || panelFocusId

  const displayNodes = useMemo(() => {
    if (!highlightIds) return nodes
    return nodes.map(node => {
      if (node.type !== 'service' || !highlightIds.has(node.id)) return node
      const cls = highlightKind === 'impact'
        ? (node.id === impactRoot?.id ? 'il-lit il-impact-root' : 'il-lit il-impact')
        : 'il-lit'
      return { ...node, className: cls }
    })
  }, [nodes, highlightIds, highlightKind, impactRoot])

  // Card and group-title rectangles, for placing labels clear of them.
  const obstacles = useMemo(() => {
    const rects: Rect[] = []
    const groupsById = new Map(nodes.filter(n => n.type === 'server').map(n => [n.id, n]))
    for (const n of nodes) {
      if (n.type === 'server') {
        rects.push({ x: n.position.x, y: n.position.y, w: Number(n.style?.width) || CARD_W, h: 50 })
      } else {
        const parent = n.parentId ? groupsById.get(n.parentId) : undefined
        rects.push({ x: (parent?.position.x || 0) + n.position.x - 4, y: (parent?.position.y || 0) + n.position.y - 4, w: CARD_W + 8, h: CARD_H + 8 })
      }
    }
    return rects
  }, [nodes])

  const connectionById = useMemo(
    () => new Map((filteredTopology?.connections || []).map(c => [c.id, c])),
    [filteredTopology]
  )

  const displayEdges = useMemo(() => {
    const lit = new Map<string, EdgeFocus>()
    if (highlightIds) {
      for (const e of edges) {
        const both = highlightIds.has(e.source) && highlightIds.has(e.target)
        if (highlightKind === 'impact' ? both : both || (highlightIds.size === 1 && (highlightIds.has(e.source) || highlightIds.has(e.target)))) {
          lit.set(e.id, highlightKind === 'impact' ? 'impact' : 'match')
        }
      }
    }
    const drift = driftFocus.focus
    if (drift && driftFocus.highlightIds) {
      for (const e of edges) {
        if (e.id === drift.connection_id || (e.source === drift.source_id && e.target === drift.target_id)) lit.set(e.id, 'outgoing')
      }
    }
    const labelled: string[] = []
    if (focusNodeId) {
      const touching = edges.filter(e => e.source === focusNodeId || e.target === focusNodeId)
      for (const e of touching) lit.set(e.id, e.source === focusNodeId ? 'outgoing' : 'incoming')
      // The busiest first, when there are too many to label.
      const rate = (id: string) => {
        const c = connectionById.get(id)
        return Math.max(c?.bytes_sent_rate || 0, c?.bytes_recv_rate || 0)
      }
      labelled.push(...touching.map(e => e.id).sort((a, b) => rate(b) - rate(a)).slice(0, MAX_LABELS))
    }
    if (hoverEdgeId) {
      if (!lit.has(hoverEdgeId)) lit.set(hoverEdgeId, 'outgoing')
      if (!labelled.includes(hoverEdgeId)) labelled.unshift(hoverEdgeId)
    }
    if (lit.size === 0) return edges

    // Labels: port, and live rates (a past instant has no rates to show).
    const byId = new Map(edges.map(e => [e.id, e]))
    const requests = labelled.flatMap(id => {
      const e = byId.get(id)
      const s = e && nodesRef.current.get(e.source)
      const t = e && nodesRef.current.get(e.target)
      if (!e || !s || !t) return []
      const sp = s.parentId ? nodesRef.current.get(s.parentId) : undefined
      const tp = t.parentId ? nodesRef.current.get(t.parentId) : undefined
      const sx = (sp?.position.x || 0) + s.position.x + CARD_W
      const sy = (sp?.position.y || 0) + s.position.y + CARD_H / 2
      const tx = (tp?.position.x || 0) + t.position.x
      const ty = (tp?.position.y || 0) + t.position.y + CARD_H / 2
      const c = connectionById.get(id)
      let text = `:${e.data?.port}${e.data?.protocol === 'udp' ? '/udp' : ''}`
      if (!timelineAt && c && ((c.bytes_sent_rate || 0) > 0 || (c.bytes_recv_rate || 0) > 0)) {
        text += `\n↑${formatRate(c.bytes_sent_rate || 0.001)} ↓${formatRate(c.bytes_recv_rate || 0.001)}`
      }
      const lines = text.split('\n')
      return [{
        id, text, samples: routeEdge(sx, sy, tx, ty, e.data?.waypoints).samples,
        width: Math.max(...lines.map(labelWidth)), height: lines.length * 15 + 4,
      }]
    })
    const placed = placeLabels(requests, obstacles)
    const texts = new Map(requests.map(r => [r.id, r.text]))

    return edges.map(e => {
      const focus = lit.get(e.id)
      if (!focus) return e
      const at = placed.get(e.id)
      return {
        ...e,
        className: 'il-lit',
        zIndex: 1,
        data: { ...e.data, highlighted: focus, label: at ? { ...at, text: texts.get(e.id)! } : undefined },
      }
    })
  }, [edges, highlightIds, highlightKind, driftFocus.focus, driftFocus.highlightIds, focusNodeId, hoverEdgeId, connectionById, obstacles, timelineAt])

  const animate = !reducedMotion && !timelineAt && edges.length <= FLOW_ANIMATION_MAX_EDGES
  const edgeSettings = useMemo(() => ({ animate }), [animate])

  const wrapperClass = [
    'h-full w-full transition-opacity duration-200',
    mapReady || nodes.length === 0 ? 'opacity-100' : 'opacity-0',
    highlightIds ? 'il-dim-nodes il-dim-edges' : '',
    focusNodeId || hoverEdgeId ? 'il-dim-edges' : '',
  ].join(' ')

  // ------------------------------------------------------------------------
  // Export
  // ------------------------------------------------------------------------

  // Export topology as high-quality PNG
  const handleExportPng = useCallback(() => {
    if (nodes.length === 0) return

    const flowElement = document.querySelector('.react-flow') as HTMLElement
    if (!flowElement) return

    // Calculate bounds of all nodes
    const nodesBounds = getNodesBounds(nodes)
    const pad = 100
    const imageWidth = nodesBounds.width + pad * 2
    const imageHeight = nodesBounds.height + pad * 2

    // Get viewport to fit all nodes
    const viewport = getViewportForBounds(nodesBounds, imageWidth, imageHeight, 0.5, 2, pad)

    // Hide controls, minimap and the flow animation during export
    const controls = flowElement.querySelector('.react-flow__controls') as HTMLElement
    const minimap = flowElement.querySelector('.react-flow__minimap') as HTMLElement
    const attribution = flowElement.querySelector('.react-flow__attribution') as HTMLElement
    if (controls) controls.style.display = 'none'
    if (minimap) minimap.style.display = 'none'
    if (attribution) attribution.style.display = 'none'
    flowElement.classList.add('il-exporting')

    // Get the viewport element and apply transform for full capture
    const viewportElement = flowElement.querySelector('.react-flow__viewport') as HTMLElement
    const originalTransform = viewportElement?.style.transform || ''
    if (viewportElement) {
      viewportElement.style.transform = `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.zoom})`
    }

    // High quality export with 3x pixel ratio
    toPng(flowElement, {
      backgroundColor: '#0a0f1a',
      width: Math.max(imageWidth, flowElement.offsetWidth),
      height: Math.max(imageHeight, flowElement.offsetHeight),
      pixelRatio: 3,
      style: {
        width: `${Math.max(imageWidth, flowElement.offsetWidth)}px`,
        height: `${Math.max(imageHeight, flowElement.offsetHeight)}px`,
      },
    })
      .then((dataUrl: string) => {
        const link = document.createElement('a')
        link.download = `infralens-topology-${new Date().toISOString().slice(0, 10)}.png`
        link.href = dataUrl
        link.click()
      })
      .catch((err: Error) => {
        console.error('Export failed:', err)
      })
      .finally(() => {
        if (viewportElement) viewportElement.style.transform = originalTransform
        if (controls) controls.style.display = ''
        if (minimap) minimap.style.display = ''
        if (attribution) attribution.style.display = ''
        flowElement.classList.remove('il-exporting')
      })
  }, [nodes])

  // Export topology as JSON
  const handleExportJson = useCallback(() => {
    if (!filteredTopology && !activeTopology) return

    const data = filteredTopology || activeTopology
    const exportData = {
      exportedAt: new Date().toISOString(),
      filters: filters,
      stats: {
        services: data?.services.length || 0,
        connections: data?.connections.length || 0,
      },
      services: data?.services || [],
      connections: data?.connections || [],
      nodeMetrics: data?.node_metrics || {},
    }

    const blob = new Blob([JSON.stringify(exportData, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.download = `infralens-topology-${new Date().toISOString().slice(0, 10)}.json`
    link.href = url
    link.click()
    URL.revokeObjectURL(url)
  }, [filteredTopology, activeTopology, filters])

  // Export topology as Mermaid or DOT graph (generated by the backend)
  const handleExportGraph = useCallback(async (format: 'mermaid' | 'dot') => {
    try {
      // Carry the viewed instant through, so exporting while scrubbed to a past
      // moment renders that topology rather than silently rendering today's.
      const at = timelineAt ? `&at=${encodeURIComponent(timelineAt)}` : ''
      const resp = await fetch(apiUrl(`/api/v1/topology/export?format=${format}${at}`))
      if (!resp.ok) {
        throw new Error(`Export failed: ${resp.status}`)
      }
      const text = await resp.text()
      const ext = format === 'mermaid' ? 'mmd' : 'dot'
      const blob = new Blob([text], { type: 'text/plain' })
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      const stamp = (timelineAt ? new Date(timelineAt) : new Date()).toISOString().slice(0, 10)
      link.download = `infralens-topology-${stamp}.${ext}`
      link.href = url
      link.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('Graph export failed:', err)
    }
  }, [timelineAt])

  const impactName = impactRoot
    ? (serviceById.get(impactRoot.id) ? serviceLabel(serviceById.get(impactRoot.id)!) : nameFromServiceId(impactRoot.id))
    : ''

  const emptyState = nodes.length === 0 && (
    timelineAt
      ? historyLoading ? 'loading-past' : 'empty-past'
      : topology === null ? 'connecting' : 'waiting'
  )

  return (
    <div className="h-screen w-screen flex flex-col bg-dark-950">
      <Header
        isConnected={isConnected}
        stats={stats}
        filters={filters}
        onFiltersChange={setFilters}
        filteredStats={filteredStats}
        searchQuery={searchQuery}
        onSearchChange={setSearchQuery}
        searchMatchCount={searchMatchIds?.size}
        onExportPng={handleExportPng}
        onExportJson={handleExportJson}
        onExportMermaid={() => handleExportGraph('mermaid')}
        onExportDot={() => handleExportGraph('dot')}
        viewingPast={!!timelineAt}
      />

      <div className="flex-1 flex overflow-hidden">
        <SideRail
          open={panel}
          onToggle={togglePanel}
          changesUnread={driftFeed.available ? driftFeed.unread : null}
          historyAvailable={!!historyRange}
          historyOpen={historyOpen}
          viewingPast={!!timelineAt}
          onToggleHistory={() => setHistoryOpen(o => !o)}
        />

        <div className="flex-1 relative min-w-0">
          <EdgeSettingsContext.Provider value={edgeSettings}>
            <ReactFlow
              nodes={displayNodes}
              edges={displayEdges}
              onInit={(instance) => { reactFlowInstanceRef.current = instance }}
              onNodesChange={handleNodesChange}
              onEdgesChange={onEdgesChange}
              onNodeClick={onNodeClick}
              onEdgeClick={onEdgeClick}
              onPaneClick={onPaneClick}
              onNodeMouseEnter={(_, node) => { if (node.type === 'service') setHoverNodeId(node.id) }}
              onNodeMouseLeave={() => setHoverNodeId(null)}
              onEdgeMouseEnter={(_, edge) => setHoverEdgeId(edge.id)}
              onEdgeMouseLeave={() => setHoverEdgeId(null)}
              onMoveStart={(event) => { if (event) userMovedRef.current = true }}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              minZoom={0.05}
              maxZoom={2.5}
              nodesConnectable={false}
              edgesFocusable
              selectNodesOnDrag={false}
              onlyRenderVisibleElements={nodes.length > VISIBLE_ONLY_MIN_NODES}
              attributionPosition="bottom-right"
              className={wrapperClass}
              style={{ ['--il-right-inset' as string]: `${rightInset}px` }}
            >
              <svg className="absolute w-0 h-0" aria-hidden>
                <defs>
                  {([['base', '#475569'], ['out', '#60a5fa'], ['in', '#4ade80'], ['impact', '#f59e0b'], ['match', '#94a3b8']] as const).map(([id, color]) => (
                    <marker key={id} id={`il-arrow-${id}`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse" markerUnits="userSpaceOnUse">
                      <path d="M 0 1 L 9 5 L 0 9 z" fill={color} />
                    </marker>
                  ))}
                </defs>
              </svg>
              <Background
                variant={BackgroundVariant.Dots}
                gap={24}
                size={1}
                color="#1f2937"
              />
              <Controls
                position="bottom-right"
                showInteractive={false}
                onFitView={() => { userMovedRef.current = false }}
                fitViewOptions={{ padding, maxZoom: 1, minZoom: 0.05, duration: 350 }}
                style={{ right: rightInset + EDGE + MINIMAP_W + 8, bottom: EDGE, margin: 0 }}
              />
              <MiniMap
                nodeColor={(node) => {
                  if (node.type === 'server') return '#1e293b'
                  return node.data?.healthy ? '#22c55e' : '#ef4444'
                }}
                nodeStrokeWidth={0}
                maskColor="rgba(2, 6, 23, 0.72)"
                pannable
                zoomable
                ariaLabel="Mini map"
                style={{ width: MINIMAP_W, height: MINIMAP_H, right: rightInset + EDGE, bottom: EDGE + 14, margin: 0 }}
              />
            </ReactFlow>
          </EdgeSettingsContext.Provider>

          {/* Empty and loading states */}
          {emptyState && (
            <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
              <div className="text-center max-w-md px-6">
                {emptyState === 'connecting' || emptyState === 'loading-past' ? (
                  <>
                    <Loader2 size={28} className="mx-auto mb-4 text-dark-500 animate-spin" />
                    <h3 className="text-base font-medium text-dark-300">
                      {emptyState === 'connecting' ? 'Loading the map…' : 'Loading the recorded map…'}
                    </h3>
                  </>
                ) : emptyState === 'empty-past' ? (
                  <>
                    <h3 className="text-lg font-medium text-dark-200 mb-2">Nothing was recorded at this moment</h3>
                    <p className="text-sm text-dark-500 mb-4">History starts when InfraLens first saw traffic. Move the timeline later, or go back to the live map.</p>
                    <button
                      onClick={() => setTimelineAt(null)}
                      className="pointer-events-auto inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm bg-lens-500/15 text-lens-300 border border-lens-500/30 hover:bg-lens-500/25"
                    >
                      <Radio size={14} /> Back to live
                    </button>
                  </>
                ) : (
                  <>
                    <div className="w-20 h-20 mx-auto mb-5 rounded-full bg-dark-800/80 flex items-center justify-center">
                      <svg className="w-10 h-10 text-dark-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5}
                          d="M21 12a9 9 0 01-9 9m9-9a9 9 0 00-9-9m9 9H3m9 9a9 9 0 01-9-9m9 9c1.657 0 3-4.03 3-9s-1.343-9-3-9m0 18c-1.657 0-3-4.03-3-9s1.343-9 3-9m-9 9a9 9 0 019-9" />
                      </svg>
                    </div>
                    <h3 className="text-lg font-medium text-dark-200 mb-2">Waiting for traffic data…</h3>
                    <p className="text-sm text-dark-500">
                      The topology map will populate automatically as the eBPF agents
                      detect service-to-service communication.
                    </p>
                  </>
                )}
              </div>
            </div>
          )}

          {/* Map states: viewing the past, blast radius */}
          {(timelineAt || impactRoot) && (
            <div
              className="absolute top-3 z-20 flex flex-col items-center gap-2 pointer-events-none px-3"
              style={{ left: leftInset, right: rightInset }}
            >
              {timelineAt && <HistoryBanner at={timelineAt} loading={historyLoading} onGoLive={() => setTimelineAt(null)} />}
              {impactRoot && (
                <ImpactPill
                  name={impactName}
                  direction={impactRoot.direction}
                  count={impactIds ? impactIds.size - 1 : null}
                  loading={impactLoading}
                  onDirection={d => setImpactRoot({ id: impactRoot.id, direction: d })}
                  onClear={handleClearImpact}
                />
              )}
            </div>
          )}

          {historyOpen && historyRange && (
            <TimelineScrubber
              range={historyRange}
              value={timelineAt}
              onChange={setTimelineAt}
              loading={historyLoading}
              onClose={() => setHistoryOpen(false)}
              style={{ left: leftInset + EDGE, right: rightInset + EDGE + MINIMAP_W + 8 + 32 + 12 }}
            />
          )}

          {panel === 'changes' && driftFeed.available && (
            <ChangesPanel
              onClose={closePanel}
              seenAtOpen={driftFeed.seenAtOpen}
              selectedId={driftFocus.focus?.id ?? null}
              onSelect={driftFocus.setFocus}
              isOnGraph={driftFocus.isOnGraph}
              viewingAt={timelineAt}
              onViewAt={(ev, at) => {
                setTimelineAt(at)
                if (ev && at) driftFocus.setFocus(ev)
              }}
            />
          )}

          {panel === 'idle' && (
            <IdleSpendPanel
              onClose={closePanel}
              onFocusService={id => setPanelFocusId(id)}
              focusedId={panelFocusId}
            />
          )}

          {panel === 'critical' && (
            <CriticalityPanel
              onClose={closePanel}
              onSelect={id => setImpactRoot(cur => (cur && cur.id === id ? null : { id, direction: 'upstream' }))}
              selectedId={impactRoot?.direction === 'upstream' ? impactRoot.id : null}
            />
          )}
          {panel === 'orphans' && (
            <OrphansPanel
              onClose={closePanel}
              onSelect={id => setPanelFocusId(cur => (cur === id ? null : id))}
              selectedId={panelFocusId}
            />
          )}
          {panel === 'stale' && historyRange && <DecommissionPanel onClose={closePanel} />}
          {panel === 'compare' && historyRange && (
            <DiffPanel range={historyRange} onClose={closePanel} liveServices={liveServiceById} />
          )}
        </div>
      </div>

      {/* Service Details Drawer */}
      <ServiceDrawer
        isOpen={drawerOpen}
        onClose={closeDrawer}
        service={selectedNode.service}
        connections={filteredTopology?.connections || activeTopology?.connections || []}
        ports={selectedNode.ports}
        serverData={selectedNode.serverData}
        nodeType={selectedNode.type}
        onShowImpact={handleShowImpact}
        onClearImpact={handleClearImpact}
        activeImpactDirection={impactRoot && selectedNode.service?.id === impactRoot.id ? impactRoot.direction : null}
        services={serviceById}
        viewingAt={timelineAt}
      />
    </div>
  )
}

export default App
