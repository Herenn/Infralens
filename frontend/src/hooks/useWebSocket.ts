import { useEffect, useRef, useState, useCallback } from 'react'
import { Topology, Service, Connection, NodeMetrics } from '../types'
import { wsUrl } from '../lib/api'

const RECONNECT_DELAY = 3000

// Deltas are applied to an internal store and flushed to React state at most
// this often, so high-frequency updates don't cause render storms.
const FLUSH_INTERVAL = 1000

// Message envelope sent by the backend (v2 delta protocol)
interface WSMessage {
  type: 'snapshot' | 'service' | 'service.deleted' | 'connection' | 'connection.deleted' | 'metrics'
  data: unknown
}

// Internal mutable topology store, converted to Topology on flush
interface TopologyStore {
  services: Map<string, Service>
  connections: Map<string, Connection>
  nodeMetrics: Record<string, NodeMetrics>
  updatedAt: string
}

function emptyStore(): TopologyStore {
  return {
    services: new Map(),
    connections: new Map(),
    nodeMetrics: {},
    updatedAt: new Date().toISOString(),
  }
}

function storeToTopology(store: TopologyStore): Topology {
  return {
    services: Array.from(store.services.values()),
    connections: Array.from(store.connections.values()),
    node_metrics: { ...store.nodeMetrics },
    updated_at: store.updatedAt,
  }
}

function applySnapshot(store: TopologyStore, topology: Topology) {
  store.services = new Map(topology.services.map((s) => [s.id, s]))
  store.connections = new Map(topology.connections.map((c) => [c.id, c]))
  store.nodeMetrics = topology.node_metrics || {}
  store.updatedAt = topology.updated_at || new Date().toISOString()
}

export function useWebSocket() {
  const [topology, setTopology] = useState<Topology | null>(null)
  const [isConnected, setIsConnected] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimeoutRef = useRef<number | null>(null)
  const storeRef = useRef<TopologyStore>(emptyStore())
  const dirtyRef = useRef(false)
  const flushTimeoutRef = useRef<number | null>(null)

  const flush = useCallback(() => {
    flushTimeoutRef.current = null
    if (!dirtyRef.current) return
    dirtyRef.current = false
    setTopology(storeToTopology(storeRef.current))
  }, [])

  const scheduleFlush = useCallback(
    (immediate = false) => {
      dirtyRef.current = true
      if (immediate) {
        if (flushTimeoutRef.current !== null) {
          clearTimeout(flushTimeoutRef.current)
        }
        flush()
        return
      }
      if (flushTimeoutRef.current === null) {
        flushTimeoutRef.current = window.setTimeout(flush, FLUSH_INTERVAL)
      }
    },
    [flush]
  )

  const handleMessage = useCallback(
    (raw: string) => {
      const parsed = JSON.parse(raw)
      const store = storeRef.current

      // Legacy backends (pre-v2) send the raw topology without an envelope
      if (parsed && !parsed.type && Array.isArray(parsed.services)) {
        applySnapshot(store, parsed as Topology)
        scheduleFlush(true)
        return
      }

      const msg = parsed as WSMessage
      switch (msg.type) {
        case 'snapshot':
          applySnapshot(store, msg.data as Topology)
          scheduleFlush(true)
          break

        case 'service': {
          const svc = msg.data as Service
          store.services.set(svc.id, svc)
          scheduleFlush()
          break
        }

        case 'service.deleted': {
          const { service_id } = msg.data as { service_id: string }
          store.services.delete(service_id)
          scheduleFlush()
          break
        }

        case 'connection': {
          const conn = msg.data as Connection
          store.connections.set(conn.id, conn)
          scheduleFlush()
          break
        }

        case 'connection.deleted': {
          const { connection_id } = msg.data as { connection_id: string }
          store.connections.delete(connection_id)
          scheduleFlush()
          break
        }

        case 'metrics': {
          const m = msg.data as NodeMetrics
          store.nodeMetrics[m.node_name] = m
          scheduleFlush()
          break
        }

        default:
          console.warn('Unknown WebSocket message type:', msg.type)
      }
    },
    [scheduleFlush]
  )

  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) return

    const url = wsUrl()
    console.log('Connecting to WebSocket:', url)
    const ws = new WebSocket(url)

    ws.onopen = () => {
      console.log('WebSocket connected')
      setIsConnected(true)
    }

    ws.onmessage = (event) => {
      try {
        handleMessage(event.data)
      } catch (err) {
        console.error('Failed to parse WebSocket message:', err)
      }
    }

    ws.onclose = () => {
      console.log('WebSocket disconnected')
      setIsConnected(false)
      wsRef.current = null

      // Schedule reconnection
      reconnectTimeoutRef.current = window.setTimeout(() => {
        connect()
      }, RECONNECT_DELAY)
    }

    ws.onerror = (error) => {
      console.error('WebSocket error:', error)
    }

    wsRef.current = ws
  }, [handleMessage])

  useEffect(() => {
    connect()

    return () => {
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
      }
      if (flushTimeoutRef.current) {
        clearTimeout(flushTimeoutRef.current)
      }
      if (wsRef.current) {
        wsRef.current.close()
      }
    }
  }, [connect])

  return { topology, isConnected }
}
