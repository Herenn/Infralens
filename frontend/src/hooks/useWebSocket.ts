import { useEffect, useRef, useState, useCallback } from 'react'
import { Topology } from '../types'
import { wsUrl } from '../lib/api'

const RECONNECT_DELAY = 3000

export function useWebSocket() {
  const [topology, setTopology] = useState<Topology | null>(null)
  const [isConnected, setIsConnected] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimeoutRef = useRef<number | null>(null)

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
