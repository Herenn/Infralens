import { useEffect, useRef, useState, useCallback } from 'react'
import { Topology } from '../types'

// In development, connect to the backend on the same host but port 8080
// In production, connect via the same host (assumes reverse proxy)
const WS_URL = import.meta.env.VITE_WS_URL || 
  `ws://${window.location.hostname}:8080/api/v1/ws`

const RECONNECT_DELAY = 3000

export function useWebSocket() {
  const [topology, setTopology] = useState<Topology | null>(null)
  const [isConnected, setIsConnected] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimeoutRef = useRef<number | null>(null)

  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) return

    console.log('Connecting to WebSocket:', WS_URL)
    const ws = new WebSocket(WS_URL)

    ws.onopen = () => {
      console.log('WebSocket connected')
      setIsConnected(true)
    }

    ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data) as Topology
        setTopology(data)
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
  }, [])

  useEffect(() => {
    connect()

    return () => {
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
      }
      if (wsRef.current) {
        wsRef.current.close()
      }
    }
  }, [connect])

  return { topology, isConnected }
}
