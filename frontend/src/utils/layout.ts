import { Node, Edge } from '@xyflow/react'
import { Topology, Service, Connection as TopologyConnection } from '../types'

// Node dimensions
const NODE_WIDTH = 200
const NODE_HEIGHT = 110

// Internal layout - vertical stacking with generous spacing
const INTERNAL_PADDING = 30           // Padding inside server container
const INTERNAL_VERTICAL_SPACING = 100 // Space between stacked nodes
const SERVER_HEADER_HEIGHT = 160      // Height reserved for server header (includes CPU/RAM bars)

// Server group container
const SERVER_PADDING = 50             // Padding around the entire server group
const SERVER_HORIZONTAL_SPACING = 120 // Space between server groups

interface LayoutResult {
  nodes: Node[]
  edges: Edge[]
}

/**
 * Maps raw server name to display name.
 * "Unknown" or empty -> "External Network"
 */
function getServerDisplayName(serverName: string | undefined): string {
  if (!serverName || serverName === 'Unknown' || serverName === '') {
    return 'External Network'
  }
  return serverName
}

/**
 * Converts topology data to React Flow nodes and edges with automatic layout.
 * Groups services by their server/node and creates parent containers.
 */
export function layoutGraph(topology: Topology): LayoutResult {
  const { services, connections } = topology

  // If no services, return empty
  if (services.length === 0) {
    return { nodes: [], edges: [] }
  }

  // Build adjacency lists for layout
  const outgoing = new Map<string, string[]>()
  const incoming = new Map<string, string[]>()
  
  services.forEach(s => {
    outgoing.set(s.id, [])
    incoming.set(s.id, [])
  })

  connections.forEach(c => {
    const out = outgoing.get(c.source_id) || []
    out.push(c.target_id)
    outgoing.set(c.source_id, out)

    const inc = incoming.get(c.target_id) || []
    inc.push(c.source_id)
    incoming.set(c.target_id, inc)
  })

  // Group services by server node (using display name)
  const serverGroups = new Map<string, typeof services>()
  services.forEach(service => {
    const serverName = getServerDisplayName(service.node)
    const group = serverGroups.get(serverName) || []
    group.push(service)
    serverGroups.set(serverName, group)
  })

  const allNodes: Node[] = []
  const serverPositions = new Map<string, { x: number; y: number; width: number; height: number }>()

  // Calculate positions for each server group
  let serverX = 0
  let maxServerHeight = 0
  const serverNames = Array.from(serverGroups.keys())

  serverNames.forEach((serverName, _serverIndex) => {
    const serverServices = serverGroups.get(serverName) || []
    
    // Calculate layout for services within this server
    const { positions, width, height } = layoutServicesInServer(
      serverServices, 
      outgoing, 
      incoming
    )

    // Server node dimensions - generous padding for clean appearance
    const serverWidth = Math.max(width + SERVER_PADDING * 2, 340)
    const serverHeight = height + SERVER_HEADER_HEIGHT + SERVER_PADDING * 2

    // Position this server
    serverPositions.set(serverName, {
      x: serverX,
      y: 0,
      width: serverWidth,
      height: serverHeight,
    })

    // Create server (group) node
    const serverNodeId = `server-${serverName}`
    
    // Get node metrics if available (use original node name from first service)
    const originalNodeName = serverServices[0]?.node || serverName
    const nodeMetrics = topology.node_metrics?.[originalNodeName]

    allNodes.push({
      id: serverNodeId,
      type: 'server',
      position: { x: serverX, y: 0 },
      data: {
        label: serverName,
        serverName: serverName,
        serviceCount: serverServices.length,
        totalConnections: Math.floor(serverServices.reduce((sum, s) => {
          return sum + (incoming.get(s.id)?.length || 0) + (outgoing.get(s.id)?.length || 0)
        }, 0) / 2), // Divide by 2 to avoid double counting
        // Host resource metrics
        cpuPercent: nodeMetrics?.cpu_percent,
        memPercent: nodeMetrics?.mem_percent,
        memUsed: nodeMetrics?.mem_used,
        memTotal: nodeMetrics?.mem_total,
      },
      style: {
        width: serverWidth,
        height: serverHeight,
        // Allow clicks to pass through to children
        pointerEvents: 'none',
      },
      // Group nodes: above edges (zIndex 0) but below service nodes
      zIndex: 5,
      // Prevent selection/dragging of group node
      selectable: false,
      draggable: false,
    })

    // Create service nodes within this server - positioned in vertical stack
    serverServices.forEach(service => {
      const pos = positions.get(service.id) || { x: 0, y: 0 }
      const incomingCount = (incoming.get(service.id) || []).length
      const outgoingCount = (outgoing.get(service.id) || []).length

      // Get listening ports (ports this service receives connections on)
      const listeningPorts = connections
        .filter(c => c.target_id === service.id)
        .map(c => c.port)
        .filter((v, i, a) => a.indexOf(v) === i) // unique
        .sort((a, b) => a - b)

      allNodes.push({
        id: service.id,
        type: 'service',
        position: {
          x: pos.x + SERVER_PADDING,
          y: pos.y + SERVER_HEADER_HEIGHT,
        },
        parentId: serverNodeId,
        extent: 'parent',
        expandParent: true,
        data: {
          label: service.display_name || service.name || service.id,
          service,
          incomingCount,
          outgoingCount,
          healthy: service.healthy !== false,
          ports: listeningPorts, // Listening ports
        },
        style: {
          width: NODE_WIDTH,
        },
        // Service nodes: HIGHEST z-index - always on top of edges and group
        zIndex: 100,
      })
    })

    // Move to next server position
    serverX += serverWidth + SERVER_HORIZONTAL_SPACING
    maxServerHeight = Math.max(maxServerHeight, serverHeight)
  })

  // Create React Flow edges - using smoothstep for clean orthogonal routing
  // Edges have LOW z-index so they render BEHIND nodes
  const edges: Edge[] = connections.map(conn => ({
    id: conn.id,
    source: conn.source_id,
    target: conn.target_id,
    type: 'smoothstep', // Orthogonal routing - cleaner than bezier
    animated: false,    // Disable animation for cleaner look
    zIndex: 0,          // BEHIND all nodes
    style: {
      stroke: '#475569',      // Slate-600 - subtle color
      strokeWidth: 1.5,       // Thin lines
      opacity: 0.7,           // Slightly transparent
    },
    data: {
      port: conn.port,
      count: conn.count,
      bytesSent: conn.bytes_sent,
      bytesRecv: conn.bytes_recv,
      bytesSentRate: conn.bytes_sent_rate,
      bytesRecvRate: conn.bytes_recv_rate,
      packetsSent: conn.packets_sent,
      packetsRecv: conn.packets_recv,
      latency: conn.latency_ms,
    },
  }))

  return { nodes: allNodes, edges }
}

/**
 * Layout services within a server group using VERTICAL STACKING (single column).
 * This creates a clean, list-like layout with edges running behind nodes.
 */
function layoutServicesInServer(
  services: Service[],
  _outgoing: Map<string, string[]>,
  _incoming: Map<string, string[]>
): { positions: Map<string, { x: number; y: number }>; width: number; height: number } {
  const positions = new Map<string, { x: number; y: number }>()

  if (services.length === 0) {
    return { positions, width: 0, height: 0 }
  }

  // Simple vertical stacking - all nodes in a single centered column
  // Sort services by name for consistent ordering
  const sortedServices = [...services].sort((a, b) => {
    const nameA = a.display_name || a.name || a.id
    const nameB = b.display_name || b.name || b.id
    return nameA.localeCompare(nameB)
  })

  // Calculate positions - all centered horizontally, stacked vertically
  const centerX = INTERNAL_PADDING // Left-aligned with padding
  let currentY = INTERNAL_PADDING

  sortedServices.forEach((service) => {
    positions.set(service.id, { 
      x: centerX, 
      y: currentY 
    })
    currentY += NODE_HEIGHT + INTERNAL_VERTICAL_SPACING
  })

  // Calculate total dimensions
  const totalHeight = services.length > 0 
    ? INTERNAL_PADDING + (services.length * NODE_HEIGHT) + ((services.length - 1) * INTERNAL_VERTICAL_SPACING) + INTERNAL_PADDING
    : INTERNAL_PADDING * 2

  return { 
    positions, 
    width: NODE_WIDTH + INTERNAL_PADDING * 2, 
    height: totalHeight
  }
}

/**
 * Updates node data without changing positions.
 * Used when only metrics/throughput data changes (not structure).
 */
export function updateNodeData(currentNodes: Node[], topology: Topology): Node[] {
  const serviceMap = new Map<string, Service>()
  topology.services.forEach(s => serviceMap.set(s.id, s))

  // Build connection counts
  const incoming = new Map<string, number>()
  const outgoing = new Map<string, number>()
  
  topology.services.forEach(s => {
    incoming.set(s.id, 0)
    outgoing.set(s.id, 0)
  })

  topology.connections.forEach(c => {
    outgoing.set(c.source_id, (outgoing.get(c.source_id) || 0) + 1)
    incoming.set(c.target_id, (incoming.get(c.target_id) || 0) + 1)
  })

  // Group services by server for server node updates
  const serverStats = new Map<string, { serviceCount: number; totalConnections: number }>()
  topology.services.forEach(service => {
    const serverName = getServerDisplayName(service.node)
    const stats = serverStats.get(serverName) || { serviceCount: 0, totalConnections: 0 }
    stats.serviceCount++
    stats.totalConnections += (incoming.get(service.id) || 0) + (outgoing.get(service.id) || 0)
    serverStats.set(serverName, stats)
  })

  return currentNodes.map(node => {
    if (node.type === 'server') {
      // Update server node data
      const serverName = node.data.serverName as string
      const stats = serverStats.get(serverName)
      
      // Find original node name for metrics lookup
      const servicesInNode = topology.services.filter(s => 
        getServerDisplayName(s.node) === serverName
      )
      const originalNodeName = servicesInNode[0]?.node || serverName
      const nodeMetrics = topology.node_metrics?.[originalNodeName]

      return {
        ...node,
        data: {
          ...node.data,
          serviceCount: stats?.serviceCount ?? node.data.serviceCount,
          totalConnections: stats ? Math.floor(stats.totalConnections / 2) : node.data.totalConnections,
          // Update host resource metrics
          cpuPercent: nodeMetrics?.cpu_percent,
          memPercent: nodeMetrics?.mem_percent,
          memUsed: nodeMetrics?.mem_used,
          memTotal: nodeMetrics?.mem_total,
        },
      }
    }

    // Update service node data
    const service = serviceMap.get(node.id)
    if (!service) return node

    // Get listening ports
    const listeningPorts = topology.connections
      .filter(c => c.target_id === service.id)
      .map(c => c.port)
      .filter((v, i, a) => a.indexOf(v) === i)
      .sort((a, b) => a - b)

    return {
      ...node,
      data: {
        ...node.data,
        label: service.display_name || service.name || service.id,
        service,
        incomingCount: incoming.get(service.id) || 0,
        outgoingCount: outgoing.get(service.id) || 0,
        healthy: service.healthy !== false,
        ports: listeningPorts,
      },
    }
  })
}

/**
 * Updates edge data without recreating edges.
 * Used when only metrics/throughput data changes (not structure).
 */
export function updateEdgeData(currentEdges: Edge[], topology: Topology): Edge[] {
  const connectionMap = new Map<string, TopologyConnection>()
  topology.connections.forEach(c => connectionMap.set(c.id, c))

  return currentEdges.map(edge => {
    const conn = connectionMap.get(edge.id)
    if (!conn) return edge

    return {
      ...edge,
      data: {
        port: conn.port,
        count: conn.count,
        bytesSent: conn.bytes_sent,
        bytesRecv: conn.bytes_recv,
        bytesSentRate: conn.bytes_sent_rate,
        bytesRecvRate: conn.bytes_recv_rate,
        packetsSent: conn.packets_sent,
        packetsRecv: conn.packets_recv,
        latency: conn.latency_ms,
      },
    }
  })
}
