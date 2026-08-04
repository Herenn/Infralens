// Backend URL resolution.
//
// By default all requests are same-origin (`/api/v1/...`), which works behind
// the bundled nginx proxy, a Kubernetes ingress, or the Vite dev proxy without
// any configuration. Set VITE_API_URL / VITE_WS_URL at build time only when
// the backend is served from a different origin.

const API_BASE = (import.meta.env.VITE_API_URL || '').replace(/\/+$/, '')

/** Builds a backend HTTP URL for the given absolute path (e.g. "/api/v1/topology"). */
export function apiUrl(path: string): string {
  return `${API_BASE}${path}`
}

/** Resolves the WebSocket URL for real-time topology updates. */
export function wsUrl(): string {
  if (import.meta.env.VITE_WS_URL) {
    return import.meta.env.VITE_WS_URL
  }
  if (API_BASE) {
    return API_BASE.replace(/^http/, 'ws') + '/api/v1/ws'
  }
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/api/v1/ws`
}
