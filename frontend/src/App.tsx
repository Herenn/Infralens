import { useCallback, useEffect, useState, useRef, useMemo } from 'react'
import {
  ReactFlow,
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
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import ServiceNode from './components/ServiceNode'
import ServerNode from './components/ServerNode'
import ConnectionEdge from './components/ConnectionEdge'
import ServiceDrawer from './components/ServiceDrawer'
import Header from './components/Header'
import { Service, Topology } from './types'
import { useWebSocket } from './hooks/useWebSocket'
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

// Filter topology based on filter settings
function filterTopology(topology: Topology | null, filters: FilterState): Topology | null {
  if (!topology) return null

  let services = [...topology.services]
  let connections = [...topology.connections]

  // 1. Hide localhost traffic
  if (filters.hideLocalhost) {
    // Remove connections between localhost addresses
    connections = connections.filter(conn => {
      const isLocalSource = conn.source_id.startsWith('127.') || conn.source_id.includes('localhost')
      const isLocalTarget = conn.target_id.startsWith('127.') || conn.target_id.includes('localhost')
      return !(isLocalSource && isLocalTarget)
    })
    
    // Remove services that only have localhost IDs (keep if they have external connections)
    const connectedServiceIds = new Set([
      ...connections.map(c => c.source_id),
      ...connections.map(c => c.target_id),
    ])
    
    services = services.filter(svc => {
      const isLocalhost = svc.id.startsWith('127.') || svc.id.includes('localhost')
      if (!isLocalhost) return true
      // Keep localhost services that have non-localhost connections
      return connectedServiceIds.has(svc.id)
    })
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

  // 3. Collapse external endpoints (aggregate by tech type)
  if (filters.collapseExternal) {
    const externalServices = services.filter(s => !s.node || s.node === 'External Network')
    const internalServices = services.filter(s => s.node && s.node !== 'External Network')
    
    // Group external services by tech type
    const externalByTech = new Map<string, Service[]>()
    externalServices.forEach(svc => {
      const key = svc.tech || svc.type || 'external'
      if (!externalByTech.has(key)) {
        externalByTech.set(key, [])
      }
      externalByTech.get(key)!.push(svc)
    })
    
    // Create aggregated external services
    const aggregatedExternal: Service[] = []
    const idMapping = new Map<string, string>() // old id -> new aggregated id
    
    externalByTech.forEach((svcs, tech) => {
      if (svcs.length === 1) {
        // Keep single services as-is
        aggregatedExternal.push(svcs[0])
      } else {
        // Create aggregated service
        const aggregatedId = `external-${tech.toLowerCase().replace(/\s+/g, '-')}`
        const aggregatedSvc: Service = {
          ...svcs[0],
          id: aggregatedId,
          name: `${tech} (${svcs.length})`,
          display_name: `${tech} (${svcs.length})`,
        }
        aggregatedExternal.push(aggregatedSvc)
        
        // Map old IDs to new aggregated ID
        svcs.forEach(s => idMapping.set(s.id, aggregatedId))
      }
    })
    
    services = [...internalServices, ...aggregatedExternal]
    
    // Update connection IDs
    connections = connections.map(conn => ({
      ...conn,
      source_id: idMapping.get(conn.source_id) || conn.source_id,
      target_id: idMapping.get(conn.target_id) || conn.target_id,
    }))
    
    // Deduplicate connections after remapping
    const uniqueConnections = new Map<string, typeof connections[0]>()
    connections.forEach(conn => {
      const key = `${conn.source_id}->${conn.target_id}:${conn.port}`
      const existing = uniqueConnections.get(key)
      if (!existing || conn.count > existing.count) {
        uniqueConnections.set(key, conn)
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
  const [selectedNode, setSelectedNode] = useState<SelectedNodeState>({
    type: null,
    service: null,
    serverData: null,
    ports: [],
  })
  const [stats, setStats] = useState({ services: 0, connections: 0 })
  const [filters, setFilters] = useState<FilterState>({
    hideLocalhost: false,
    minConnections: 1,
    collapseExternal: false,
  })
  
  // Track node IDs to detect structural changes
  const prevNodeIdsRef = useRef<Set<string>>(new Set())
  const prevEdgeIdsRef = useRef<Set<string>>(new Set())
  const prevFiltersRef = useRef<FilterState>(filters)

  const { topology, isConnected } = useWebSocket()

  // Apply filters to topology
  const filteredTopology = useMemo(() => 
    filterTopology(topology, filters), 
    [topology, filters]
  )

  // Calculate filtered stats
  const filteredStats = useMemo(() => ({
    services: filteredTopology?.services.length || 0,
    connections: filteredTopology?.connections.length || 0,
  }), [filteredTopology])

  // Update graph when topology changes - ONLY re-layout on structural changes
  useEffect(() => {
    if (!filteredTopology) return

    console.log('🔍 Topology received:', {
      services: filteredTopology.services.length,
      connections: filteredTopology.connections.length,
    })

    // Get current service IDs and connection IDs
    const currentServiceIds = new Set(filteredTopology.services.map(s => s.id))
    const currentConnectionIds = new Set(filteredTopology.connections.map(c => c.id))
    
    // Also count unique server nodes
    const currentServerIds = new Set(filteredTopology.services.map(s => `server-${s.node || 'External Network'}`))
    const allCurrentNodeIds = new Set([...currentServiceIds, ...currentServerIds])

    // Check if structure changed (new nodes/edges appeared or disappeared)
    const filtersChanged = JSON.stringify(filters) !== JSON.stringify(prevFiltersRef.current)
    const structureChanged = 
      filtersChanged ||
      allCurrentNodeIds.size !== prevNodeIdsRef.current.size ||
      currentConnectionIds.size !== prevEdgeIdsRef.current.size ||
      [...allCurrentNodeIds].some(id => !prevNodeIdsRef.current.has(id)) ||
      [...currentConnectionIds].some(id => !prevEdgeIdsRef.current.has(id))

    if (structureChanged) {
      // Structure changed - run full layout
      console.log('🔄 Structure changed, running layout...')
      try {
        const { nodes: newNodes, edges: newEdges } = layoutGraph(filteredTopology)
        console.log('✅ Layout result:', { nodes: newNodes.length, edges: newEdges.length })
        setNodes(newNodes)
        setEdges(newEdges)
        prevNodeIdsRef.current = allCurrentNodeIds
        prevEdgeIdsRef.current = currentConnectionIds
        prevFiltersRef.current = filters
      } catch (err) {
        console.error('❌ Layout error:', err)
      }
    } else {
      // Only data changed - update node/edge data without changing positions
      setNodes(currentNodes => updateNodeData(currentNodes, filteredTopology))
      setEdges(currentEdges => updateEdgeData(currentEdges, filteredTopology))
    }

    // Update raw stats (unfiltered)
    if (topology) {
      setStats({
        services: topology.services.length,
        connections: topology.connections.length,
      })
    }
  }, [filteredTopology, filters, topology, setNodes, setEdges])

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
      setDrawerOpen(true)
    } else if (node.type === 'service') {
      // Service node clicked - find from original topology for full data
      const service = topology?.services.find((s) => s.id === node.id)
      const ports = (node.data as { ports?: number[] })?.ports || []
      setSelectedNode({
        type: 'service',
        service: service || null,
        serverData: null,
        ports,
      })
      setDrawerOpen(true)
    }
  }, [topology])

  // Handle pane click - close drawer
  const onPaneClick = useCallback(() => {
    setDrawerOpen(false)
  }, [])

  return (
    <div className="h-screen w-screen flex flex-col bg-dark-950">
      <Header 
        isConnected={isConnected} 
        stats={stats} 
        filters={filters}
        onFiltersChange={setFilters}
        filteredStats={filteredStats}
      />
      
      <div className="flex-1 flex overflow-hidden">
        <div className="flex-1 relative">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            onPaneClick={onPaneClick}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            fitView
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
        </div>
      </div>

      {/* Service Details Drawer */}
      <ServiceDrawer
        isOpen={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        service={selectedNode.service}
        connections={topology?.connections || []}
        ports={selectedNode.ports}
        serverData={selectedNode.serverData}
        nodeType={selectedNode.type}
      />
    </div>
  )
}

export default App
