import { useEffect, useRef, useState, useCallback } from 'react'
import { Topology, Service, Connection, NodeMetrics, DriftEvent } from '../types'
import { wsUrl } from '../lib/api'
import { emitDriftEvents, emitDriftResync } from '../lib/drift'
import { checkAfterDisconnect, notifyWebSocketRejected } from '../lib/auth'

const RECONNECT_DELAY = 3000

// Handshakes refused while the API and session are fine (e.g. a proxy
// breaking the Origin check) are retried with exponential backoff, and after
// this many in a row the app shows a persistent warning instead of retrying
// silently forever.
const REJECTED_WARN_AFTER = 3
const MAX_RECONNECT_DELAY = 60000

// Deltas are applied to an internal store and flushed to React state at most
// this often, so high-frequency updates don't cause render storms.
const FLUSH_INTERVAL = 1000

// Message envelope sent by the backend (v2 delta protocol)
interface WSMessage {
  type: 'snapshot' | 'service' | 'service.deleted' | 'connection' | 'connection.deleted' | 'metrics' | 'drift'
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
  // Set on unmount so the close handler doesn't schedule a reconnect for a
  // socket we deliberately tore down.
  const closedRef = useRef(false)
  const everConnectedRef = useRef(false)
  const rejectedCountRef = useRef(0)
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

        // Architecture drift just recorded - not topology state, so it is
        // handed to the Changes feed rather than the store.
        case 'drift': {
          const { events, truncated } = msg.data as { events: DriftEvent[]; truncated?: boolean }
          // A capped message (a large rollout) is a hint, not the data.
          if (truncated) emitDriftResync()
          else if (events?.length) emitDriftEvents(events)
          break
        }

        default:
          console.warn('Unknown WebSocket message type:', msg.type)
      }
    },
    [scheduleFlush]
  )

  const connect = useCallback(() => {
    if (closedRef.current) return
    const current = wsRef.current
    if (current && (current.readyState === WebSocket.OPEN || current.readyState === WebSocket.CONNECTING)) return

    const url = wsUrl()
    console.log('Connecting to WebSocket:', url)
    const ws = new WebSocket(url)
    let opened = false

    ws.onopen = () => {
      console.log('WebSocket connected')
      opened = true
      if (rejectedCountRef.current >= REJECTED_WARN_AFTER) {
        notifyWebSocketRejected(false)
      }
      rejectedCountRef.current = 0
      setIsConnected(true)
      // Live drift sent while we were away is gone; tell the feed to refetch.
      if (everConnectedRef.current) emitDriftResync()
      everConnectedRef.current = true
    }

    ws.onmessage = (event) => {
      try {
        handleMessage(event.data)
      } catch (err) {
        console.error('Failed to parse WebSocket message:', err)
      }
    }

    ws.onclose = () => {
      // A socket this hook has already let go of (see the effect cleanup)
      // must not touch the state of the one that replaced it.
      if (wsRef.current !== ws) return
      console.log('WebSocket disconnected')
      setIsConnected(false)
      wsRef.current = null

      // Don't reconnect a socket we closed ourselves on unmount
      if (closedRef.current) return

      // A refused handshake looks like any other close from here (browsers
      // hide its HTTP status), so ask whether the session is gone before
      // retrying: if it is, the app drops to the login screen instead of
      // retrying a doomed handshake every few seconds.
      void checkAfterDisconnect().then((cause) => {
        if (cause === 'unauthorized' || closedRef.current) return

        let delay = RECONNECT_DELAY
        if (!opened && cause === 'ok') {
          // The server answered and the session is valid, yet the handshake
          // failed: it is being refused, so back off and say so.
          rejectedCountRef.current += 1
          if (rejectedCountRef.current === REJECTED_WARN_AFTER) {
            console.error('WebSocket handshake keeps being refused although the API is reachable')
            notifyWebSocketRejected(true)
          }
          delay = Math.min(RECONNECT_DELAY * 2 ** (rejectedCountRef.current - 1), MAX_RECONNECT_DELAY)
        }

        // Schedule reconnection
        reconnectTimeoutRef.current = window.setTimeout(() => {
          connect()
        }, delay)
      })
    }

    ws.onerror = (error) => {
      console.error('WebSocket error:', error)
    }

    wsRef.current = ws
  }, [handleMessage])

  useEffect(() => {
    closedRef.current = false
    connect()

    return () => {
      closedRef.current = true
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
        reconnectTimeoutRef.current = null
      }
      if (flushTimeoutRef.current) {
        clearTimeout(flushTimeoutRef.current)
        flushTimeoutRef.current = null
      }
      const ws = wsRef.current
      wsRef.current = null
      if (ws) {
        // Detach first: a socket closed here never reconnects or reports,
        // whatever order its events arrive in (React StrictMode mounts,
        // unmounts and remounts in development).
        ws.onopen = null
        ws.onmessage = null
        ws.onerror = null
        ws.onclose = null
        // Closing a socket that is still connecting logs a browser error;
        // let it finish opening, then close it.
        if (ws.readyState === WebSocket.CONNECTING) ws.addEventListener('open', () => ws.close())
        else ws.close()
      }
    }
  }, [connect])

  return { topology, isConnected }
}
