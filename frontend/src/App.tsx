import { useCallback, useEffect, useState, useRef, useMemo } from 'react'
import {
  ReactFlow,
  ReactFlowInstance,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  addEdge,
  Connection,
  Node,
  Edge,
  BackgroundVariant,
  getNodesBounds,
  getViewportForBounds,
} from '@xyflow/react'
import { toPng } from 'html-to-image'
import '@xyflow/react/dist/style.css'

import ServiceNode from './components/ServiceNode'
import ServerNode from './components/ServerNode'
import ConnectionEdge from './components/ConnectionEdge'
import ServiceDrawer from './components/ServiceDrawer'
import Header from './components/Header'
import TimelineScrubber, { HistoryRange } from './components/TimelineScrubber'
import { Service, Topology } from './types'
import { useWebSocket } from './hooks/useWebSocket'
import { apiUrl } from './lib/api'
import { layoutGraph, updateNodeData, updateEdgeData } from './utils/layout'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AppNode = Node<any>
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AppEdge = Edge<any>

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
  const [filters, setFilters] = useState<FilterState>({
    hideLocalhost: false,
    minConnections: 1,
    collapseExternal: false,
  })
  const reactFlowInstanceRef = useRef<ReactFlowInstance<AppNode, AppEdge> | null>(null)
  
  // Track node IDs to detect structural changes (only services, not connections)
  const prevNodeIdsRef = useRef<Set<string>>(new Set())
  const prevFiltersRef = useRef<FilterState>(filters)
  const lastLayoutTimeRef = useRef<number>(0)
  const storedPositionsRef = useRef<Map<string, { x: number; y: number }>>(new Map())
  const MIN_LAYOUT_INTERVAL = 5000 // Minimum 5 seconds between layouts

  const { topology, isConnected } = useWebSocket()

  // Topology history: viewing a past instant (timelineAt set) replaces the
  // live WebSocket-driven topology with a one-off fetch. historyRange is
  // null (hiding the scrubber entirely) unless the backend has both history
  // enabled and at least one recorded interval.
  const [timelineAt, setTimelineAt] = useState<string | null>(null)
  const [historicalTopology, setHistoricalTopology] = useState<Topology | null>(null)
  const [historyRange, setHistoryRange] = useState<HistoryRange | null>(null)
  const [historyLoading, setHistoryLoading] = useState(false)

  const activeTopology = timelineAt ? historicalTopology : topology

  useEffect(() => {
    let cancelled = false
    fetch(apiUrl('/api/v1/topology/history/range'))
      .then(res => (res.ok ? res.json() : null))
      .then((data: { earliest: string | null; latest: string | null } | null) => {
        if (cancelled || !data?.earliest || !data?.latest) return
        setHistoryRange({ earliest: data.earliest, latest: data.latest })
      })
      .catch(() => {
        // History disabled or unreachable - the scrubber just stays hidden.
      })
    return () => { cancelled = true }
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
      const haystack = [svc.id, svc.name, svc.display_name, svc.resolved_name, svc.tech, svc.type, svc.pod_ip, svc.node]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
      if (haystack.includes(query)) {
        matches.add(svc.id)
      }
    })
    return matches
  }, [searchQuery, filteredTopology])

  // Apply search highlighting: dim non-matching services and their edges
  const displayNodes = useMemo(() => {
    if (!searchMatchIds) return nodes
    return nodes.map(node => {
      if (node.type !== 'service') return node
      const matched = searchMatchIds.has(node.id)
      return {
        ...node,
        style: { ...node.style, opacity: matched ? 1 : 0.2 },
      }
    })
  }, [nodes, searchMatchIds])

  const displayEdges = useMemo(() => {
    if (!searchMatchIds) return edges
    return edges.map(edge => {
      const matched = searchMatchIds.has(edge.source) || searchMatchIds.has(edge.target)
      return {
        ...edge,
        style: { ...edge.style, opacity: matched ? 1 : 0.1 },
      }
    })
  }, [edges, searchMatchIds])

  // Zoom to search matches
  useEffect(() => {
    if (!searchMatchIds || searchMatchIds.size === 0) return
    const instance = reactFlowInstanceRef.current
    if (!instance) return

    const timeout = window.setTimeout(() => {
      instance.fitView({
        nodes: [...searchMatchIds].map(id => ({ id })),
        duration: 400,
        padding: 0.4,
        maxZoom: 1.2,
      })
    }, 250) // debounce while typing
    return () => window.clearTimeout(timeout)
  }, [searchMatchIds])

  // Update graph when topology changes - ONLY re-layout on structural changes
  useEffect(() => {
    if (!filteredTopology) return

    console.log('🔍 Topology received:', {
      services: filteredTopology.services.length,
      connections: filteredTopology.connections.length,
    })

    // Get current service IDs (only services, NOT connections - connections change too often)
    const currentServiceIds = new Set(filteredTopology.services.map(s => s.id))
    
    // Also count unique server nodes
    const currentServerIds = new Set(filteredTopology.services.map(s => `server-${s.node || 'External Network'}`))
    const allCurrentNodeIds = new Set([...currentServiceIds, ...currentServerIds])

    // Check if SERVICE structure changed (ignore connection changes - they're too volatile)
    const filtersChanged = JSON.stringify(filters) !== JSON.stringify(prevFiltersRef.current)
    const nodesAdded = [...allCurrentNodeIds].some(id => !prevNodeIdsRef.current.has(id))
    const nodesRemoved = [...prevNodeIdsRef.current].some(id => !allCurrentNodeIds.has(id))
    const structureChanged = filtersChanged || nodesAdded || nodesRemoved
    
    // Also check minimum time since last layout (debounce)
    const now = Date.now()
    const canLayout = now - lastLayoutTimeRef.current > MIN_LAYOUT_INTERVAL

    if (structureChanged && canLayout) {
      // Structure changed - run full layout
      console.log('🔄 Structure changed, running layout...', { 
        filtersChanged, 
        nodesAdded, 
        nodesRemoved,
        nodeCount: allCurrentNodeIds.size
      })
      try {
        const { nodes: newNodes, edges: newEdges } = layoutGraph(filteredTopology)
        
        // Restore saved positions for existing nodes to minimize jumping
        const restoredNodes = newNodes.map(node => {
          const savedPos = storedPositionsRef.current.get(node.id)
          if (savedPos && !nodesAdded) {
            // Only restore positions if no new nodes were added
            // (new nodes need fresh layout to find good spots)
            return { ...node, position: savedPos }
          }
          return node
        })
        
        console.log('✅ Layout result:', { nodes: restoredNodes.length, edges: newEdges.length })
        setNodes(restoredNodes)
        setEdges(newEdges)
        prevNodeIdsRef.current = allCurrentNodeIds
        prevFiltersRef.current = filters
        lastLayoutTimeRef.current = now
        
        // Save new positions
        restoredNodes.forEach(node => {
          storedPositionsRef.current.set(node.id, node.position)
        })
      } catch (err) {
        console.error('❌ Layout error:', err)
      }
    } else if (structureChanged && !canLayout) {
      // Structure changed but too soon - just update data, layout will happen later
      console.log('⏳ Structure changed but debouncing...')
      setNodes(currentNodes => updateNodeData(currentNodes, filteredTopology))
      setEdges(currentEdges => updateEdgeData(currentEdges, filteredTopology))
    } else {
      // Only data changed - update node/edge data without changing positions
      setNodes(currentNodes => {
        const updated = updateNodeData(currentNodes, filteredTopology)
        // Save current positions
        updated.forEach(node => {
          storedPositionsRef.current.set(node.id, node.position)
        })
        return updated
      })
      setEdges(currentEdges => updateEdgeData(currentEdges, filteredTopology))
    }

    // Update raw stats (unfiltered)
    if (activeTopology) {
      setStats({
        services: activeTopology.services.length,
        connections: activeTopology.connections.length,
      })
    }
  }, [filteredTopology, filters, activeTopology, setNodes, setEdges])

  // Custom nodes change handler that saves positions when dragged
  const handleNodesChange = useCallback((changes: Parameters<typeof onNodesChange>[0]) => {
    onNodesChange(changes)
    
    // Save positions for position changes (from dragging)
    changes.forEach(change => {
      if (change.type === 'position' && change.position) {
        storedPositionsRef.current.set(change.id, change.position)
      }
    })
  }, [onNodesChange])

  const onConnect = useCallback(
    (params: Connection) => setEdges((eds) => addEdge(params, eds)),
    [setEdges]
  )

  // Handle node click - open drawer with appropriate data
  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    if (node.type === 'server') {
      // Server/Group node clicked
      const data = node.data as {
        serverName: string
        serviceCount: number
        totalConnections: number
        cpuPercent?: number
        memPercent?: number
        memUsed?: number
        memTotal?: number
      }
      setSelectedNode({
        type: 'server',
        service: null,
        serverData: data,
        ports: [],
      })
      setSelectedServiceId(null) // Clear edge highlighting
      setDrawerOpen(true)
    } else if (node.type === 'service') {
      // Service node clicked - find from original topology first, then filtered (for aggregated services)
      let service = activeTopology?.services.find((s) => s.id === node.id)

      // If not found in original, check filtered topology (for collapsed/aggregated services)
      if (!service && filteredTopology) {
        service = filteredTopology.services.find((s) => s.id === node.id)
      }

      const ports = (node.data as { ports?: number[] })?.ports || []
      setSelectedNode({
        type: 'service',
        service: service || null,
        serverData: null,
        ports,
      })
      setSelectedServiceId(node.id) // Highlight connected edges
      setDrawerOpen(true)
    }
  }, [activeTopology, filteredTopology])

  // Handle edge click - highlight source and target services
  const onEdgeClick = useCallback((_: React.MouseEvent, edge: Edge) => {
    // Find the source service for the edge
    const sourceService = activeTopology?.services.find((s) => s.id === edge.source) ||
                          filteredTopology?.services.find((s) => s.id === edge.source)
    
    if (sourceService) {
      const edgePort = edge.data?.port as number | undefined
      setSelectedNode({
        type: 'service',
        service: sourceService,
        serverData: null,
        ports: edgePort ? [edgePort] : [],
      })
      setSelectedServiceId(edge.source) // Highlight edges connected to source
      setDrawerOpen(true)
    }
  }, [activeTopology, filteredTopology])

  // Handle pane click - close drawer and clear selection
  const onPaneClick = useCallback(() => {
    setDrawerOpen(false)
    setSelectedServiceId(null)
  }, [])

  // Update edge highlighting when selection changes
  useEffect(() => {
    setEdges(currentEdges => currentEdges.map(edge => ({
      ...edge,
      data: {
        ...edge.data,
        highlighted: selectedServiceId ? 
          (edge.source === selectedServiceId ? 'outgoing' : 
           edge.target === selectedServiceId ? 'incoming' : null) 
          : null,
      },
    })))
  }, [selectedServiceId, setEdges])

  // Export topology as high-quality PNG
  const handleExportPng = useCallback(() => {
    if (nodes.length === 0) return

    const flowElement = document.querySelector('.react-flow') as HTMLElement
    if (!flowElement) return

    // Calculate bounds of all nodes
    const nodesBounds = getNodesBounds(nodes)
    const padding = 100
    const imageWidth = nodesBounds.width + padding * 2
    const imageHeight = nodesBounds.height + padding * 2

    // Get viewport to fit all nodes
    const viewport = getViewportForBounds(
      nodesBounds,
      imageWidth,
      imageHeight,
      0.5,  // minZoom
      2,    // maxZoom
      padding
    )

    // Hide controls and minimap during export
    const controls = flowElement.querySelector('.react-flow__controls') as HTMLElement
    const minimap = flowElement.querySelector('.react-flow__minimap') as HTMLElement
    const attribution = flowElement.querySelector('.react-flow__attribution') as HTMLElement
    
    if (controls) controls.style.display = 'none'
    if (minimap) minimap.style.display = 'none'
    if (attribution) attribution.style.display = 'none'

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
      pixelRatio: 3, // 3x resolution for high quality
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
        // Restore everything
        if (viewportElement) {
          viewportElement.style.transform = originalTransform
        }
        if (controls) controls.style.display = ''
        if (minimap) minimap.style.display = ''
        if (attribution) attribution.style.display = ''
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
      const resp = await fetch(apiUrl(`/api/v1/topology/export?format=${format}`))
      if (!resp.ok) {
        throw new Error(`Export failed: ${resp.status}`)
      }
      const text = await resp.text()
      const ext = format === 'mermaid' ? 'mmd' : 'dot'
      const blob = new Blob([text], { type: 'text/plain' })
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.download = `infralens-topology-${new Date().toISOString().slice(0, 10)}.${ext}`
      link.href = url
      link.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('Graph export failed:', err)
    }
  }, [])

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
      />
      
      <div className="flex-1 flex overflow-hidden">
        <div className="flex-1 relative">
          <ReactFlow
            nodes={displayNodes}
            edges={displayEdges}
            onInit={(instance) => { reactFlowInstanceRef.current = instance }}
            onNodesChange={handleNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            onEdgeClick={onEdgeClick}
            onPaneClick={onPaneClick}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            fitView
            edgesFocusable
            selectNodesOnDrag={false}
            attributionPosition="bottom-left"
            className="grid-bg"
            defaultEdgeOptions={{
              type: 'smoothstep',
              animated: false,
              style: {
                stroke: '#475569',
                strokeWidth: 1.5,
              },
            }}
          >
            <Background 
              variant={BackgroundVariant.Dots} 
              gap={24} 
              size={1} 
              color="#374151"
            />
            <Controls 
              className="!bg-dark-800 !border-dark-600 !rounded-lg !shadow-xl"
              showInteractive={false}
            />
            <MiniMap
              className="!bg-dark-800 !border-dark-600 !rounded-lg"
              nodeColor={(node) => {
                if (node.type === 'server') return '#475569'
                return node.data?.healthy ? '#22c55e' : '#ef4444'
              }}
              maskColor="rgba(0, 0, 0, 0.8)"
              pannable
              zoomable
            />
          </ReactFlow>

          {/* Empty state */}
          {nodes.length === 0 && (
            <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
              <div className="text-center">
                <div className="w-24 h-24 mx-auto mb-6 rounded-full bg-dark-800 flex items-center justify-center">
                  <svg className="w-12 h-12 text-dark-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} 
                      d="M21 12a9 9 0 01-9 9m9-9a9 9 0 00-9-9m9 9H3m9 9a9 9 0 01-9-9m9 9c1.657 0 3-4.03 3-9s-1.343-9-3-9m0 18c-1.657 0-3-4.03-3-9s1.343-9 3-9m-9 9a9 9 0 019-9" />
                  </svg>
                </div>
                <h3 className="text-xl font-medium text-dark-300 mb-2">
                  Waiting for traffic data...
                </h3>
                <p className="text-dark-500 max-w-md">
                  The topology map will populate automatically as the eBPF agents
                  detect service-to-service communication.
                </p>
              </div>
            </div>
          )}

          <TimelineScrubber
            range={historyRange}
            value={timelineAt}
            onChange={setTimelineAt}
            loading={historyLoading}
          />
        </div>
      </div>

      {/* Service Details Drawer */}
      <ServiceDrawer
        isOpen={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        service={selectedNode.service}
        connections={filteredTopology?.connections || activeTopology?.connections || []}
        ports={selectedNode.ports}
        serverData={selectedNode.serverData}
        nodeType={selectedNode.type}
      />
    </div>
  )
}

export default App
