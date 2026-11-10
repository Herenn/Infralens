import { useCallback, useEffect, useRef, useState } from 'react'
import { DriftApiError, fetchDriftSummary, getLastSeen, isAfter, onDriftEvents, onDriftResync, setLastSeen } from '../lib/drift'

// Slow on purpose: live events arrive over the WebSocket, this only catches
// up after a disconnect or a missed message.
const SUMMARY_POLL_INTERVAL = 60000

export interface DriftFeedState {
  // False until the backend confirms the feed exists (history enabled, drift
  // detection on, endpoint reachable/authorized) - the button stays hidden.
  available: boolean
  unread: number
  // What the user had seen when the panel was last opened, so the panel can
  // mark what is new since then even after the badge has been cleared.
  seenAtOpen: string
}

// Tracks the drift feed's unread count for the header badge. While the
// panel is open everything is considered seen, including live arrivals.
export function useDriftFeed(open: boolean): DriftFeedState {
  const [available, setAvailable] = useState(false)
  const [unread, setUnread] = useState(0)
  const [seenAtOpen, setSeenAtOpen] = useState<string>(getLastSeen)
  const openRef = useRef(open)
  openRef.current = open

  const markSeen = useCallback((detectedAt: string | null) => {
    if (detectedAt && isAfter(detectedAt, getLastSeen())) setLastSeen(detectedAt)
    setUnread(0)
  }, [])

  const refresh = useCallback(() => {
    const since = getLastSeen()
    fetchDriftSummary(since)
      .then(sum => {
        setAvailable(true)
        // The marker may have moved while this was in flight.
        if (openRef.current || since !== getLastSeen()) return
        setUnread(sum.total)
      })
      .catch(err => {
        // Only a definitive answer hides the feed (4xx: history off, drift
        // disabled -> 404, not authorized); a network blip or a restarting
        // backend leaves the button (and an open panel) alone.
        if (err instanceof DriftApiError && err.status >= 400 && err.status < 500) setAvailable(false)
      })
  }, [])

  useEffect(() => {
    refresh()
    const timer = window.setInterval(refresh, SUMMARY_POLL_INTERVAL)
    return () => window.clearInterval(timer)
  }, [refresh])

  // Opening the panel: remember the old marker for "new" highlighting, then
  // move it to the newest event.
  useEffect(() => {
    if (!open) return
    setSeenAtOpen(getLastSeen())
    let cancelled = false
    fetchDriftSummary()
      .then(sum => { if (!cancelled) markSeen(sum.latest) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [open, markSeen])

  useEffect(() => onDriftEvents(events => {
    if (openRef.current) {
      const newest = events.reduce<string | null>((acc, ev) => (isAfter(ev.detected_at, acc) ? ev.detected_at : acc), null)
      markSeen(newest)
    } else {
      refresh()
    }
  }), [markSeen, refresh])

  // Capped live message or reconnect: recount (the open panel refetches on
  // its own and marks what it shows as seen).
  useEffect(() => onDriftResync(() => {
    if (openRef.current) {
      fetchDriftSummary().then(sum => { if (openRef.current) markSeen(sum.latest) }).catch(() => {})
    } else {
      refresh()
    }
  }), [markSeen, refresh])

  return { available, unread, seenAtOpen }
}
