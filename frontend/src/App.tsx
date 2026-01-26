import { useCallback, useEffect, useState, useRef } from 'react'
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
import { Topology, Service } from './types'
import { useWebSocket } from './hooks/useWebSocket'
import { layoutGraph, updateNodeData, updateEdgeData } from './utils/layout'

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

function App() {
  const [nodes, setNodes, onNodesChange] = useNodesState([])
  const [edges, setEdges, onEdgesChange] = useEdgesState([])
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [selectedNode, setSelectedNode] = useState<SelectedNodeState>({
    type: null,
    service: null,
    serverData: null,
    ports: [],
  })
  const [stats, setStats] = useState({ services: 0, connections: 0 })
  
  // Track node IDs to detect structural changes
  const prevNodeIdsRef = useRef<Set<string>>(new Set())
  const prevEdgeIdsRef = useRef<Set<string>>(new Set())

  const { topology, isConnected } = useWebSocket()

  // Update graph when topology changes - ONLY re-layout on structural changes
  useEffect(() => {
    if (!topology) return

    console.log('🔍 Topology received:', {
      services: topology.services.length,
      connections: topology.connections.length,
    })

    // Get current service IDs and connection IDs
    const currentServiceIds = new Set(topology.services.map(s => s.id))
    const currentConnectionIds = new Set(topology.connections.map(c => c.id))
    
    // Also count unique server nodes
    const currentServerIds = new Set(topology.services.map(s => `server-${s.node || 'External Network'}`))
    const allCurrentNodeIds = new Set([...currentServiceIds, ...currentServerIds])

    // Check if structure changed (new nodes/edges appeared or disappeared)
    const structureChanged = 
      allCurrentNodeIds.size !== prevNodeIdsRef.current.size ||
      currentConnectionIds.size !== prevEdgeIdsRef.current.size ||
      [...allCurrentNodeIds].some(id => !prevNodeIdsRef.current.has(id)) ||
      [...currentConnectionIds].some(id => !prevEdgeIdsRef.current.has(id))

    if (structureChanged) {
      // Structure changed - run full layout
      console.log('🔄 Structure changed, running layout...')
      try {
        const { nodes: newNodes, edges: newEdges } = layoutGraph(topology)
        console.log('✅ Layout result:', { nodes: newNodes.length, edges: newEdges.length })
        console.log('📦 First node:', newNodes[0])
        setNodes(newNodes)
        setEdges(newEdges)
        prevNodeIdsRef.current = allCurrentNodeIds
        prevEdgeIdsRef.current = currentConnectionIds
      } catch (err) {
        console.error('❌ Layout error:', err)
      }
    } else {
      // Only data changed - update node/edge data without changing positions
      setNodes(currentNodes => updateNodeData(currentNodes, topology))
      setEdges(currentEdges => updateEdgeData(currentEdges, topology))
    }

    setStats({
      services: topology.services.length,
      connections: topology.connections.length,
    })
  }, [topology, setNodes, setEdges])

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
      // Service node clicked
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
      <Header isConnected={isConnected} stats={stats} />
      
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
