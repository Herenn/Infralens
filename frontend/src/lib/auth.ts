// UI login (InfraLens 3.1).
//
// The backend authenticates the UI with an HttpOnly session cookie, so API
// calls and the WebSocket need no auth code of their own: same-origin
// requests carry the cookie automatically. What this module adds is noticing
// when a session is missing or has ended, so the app can drop back to the
// login screen instead of showing errors:
//
//  - installAuthInterceptor() wraps window.fetch once. Any 401 from the
//    InfraLens API (outside /api/v1/auth/) fires UNAUTHORIZED_EVENT. Plain
//    `fetch(apiUrl(...))` calls anywhere in the app are covered without
//    changes.
//  - The WebSocket cannot see the HTTP status of a refused handshake, so
//    useWebSocket asks shouldReconnect() before retrying.
//
// Cross-origin backends (VITE_API_URL on another origin): when login is
// required, API requests are sent with credentials: 'include'. That needs the
// backend's CORS_ORIGINS to list this origin explicitly and
// CORS_CREDENTIALS=true, and because the cookie is SameSite=Strict, the two
// origins must be the same site (e.g. ui.example.com and api.example.com).

import { createContext, useContext } from 'react'
import { apiUrl } from './api'

export interface AuthStatus {
  login_required: boolean
  authenticated: boolean
  method?: 'session' | 'api_key'
  username?: string
  expires_at?: string
}

export type LoginResult =
  | { ok: true; status: AuthStatus }
  | { ok: false; error: string; retryAfter?: number }

/** Fired on window when an API call or the WebSocket finds no valid session. */
export const UNAUTHORIZED_EVENT = 'infralens:unauthorized'

/**
 * Fired when WebSocket handshakes keep failing although the API answers and
 * the session is fine (e.g. a proxy breaking the Origin/Host check), and
 * again with detail false once a socket opens.
 */
export const WS_REJECTED_EVENT = 'infralens:ws-rejected'

// localStorage/sessionStorage keys with this prefix hold data derived from
// the topology (e.g. the AI-docs cache, 'infralens_ai_docs_<id>') and are
// cleared on logout so the next person at this browser cannot read them.
// New client-side caches of API data should use this prefix.
const SESSION_SCOPED_STORAGE_PREFIX = 'infralens_'

// Captured before the interceptor replaces window.fetch, so auth calls never
// recurse into it.
const nativeFetch: typeof window.fetch = window.fetch.bind(window)

// Only turned on once the backend says login is required: sending
// credentials to a cross-origin backend that does not allow them would break
// setups that work today without login.
let includeCredentials = false

function apiPrefix(): string {
  return new URL(apiUrl('/api/'), window.location.href).href
}

function isCrossOrigin(): boolean {
  return new URL(apiPrefix()).origin !== window.location.origin
}

function credentials(): RequestCredentials {
  return includeCredentials ? 'include' : 'same-origin'
}

async function getStatus(creds: RequestCredentials): Promise<AuthStatus | null> {
  const resp = await nativeFetch(apiUrl('/api/v1/auth/status'), { credentials: creds, cache: 'no-store' })
  if (!resp.ok) return null
  return (await resp.json()) as AuthStatus
}

/**
 * Asks the backend whether login is required and whether we have a session.
 * Returns null when that cannot be determined (backend down, or a pre-3.1
 * backend without the endpoint) - callers treat that as "no login".
 */
export async function fetchAuthStatus(): Promise<AuthStatus | null> {
  try {
    const status = await getStatus(credentials())
    if (status?.login_required && !includeCredentials && isCrossOrigin()) {
      includeCredentials = true
      try {
        return (await getStatus('include')) ?? status
      } catch {
        return status
      }
    }
    return status
  } catch {
    return null
  }
}

export async function login(username: string, password: string): Promise<LoginResult> {
  let resp: Response
  try {
    resp = await nativeFetch(apiUrl('/api/v1/auth/login'), {
      method: 'POST',
      credentials: credentials(),
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    })
  } catch {
    return { ok: false, error: 'Could not reach the InfraLens backend.' }
  }

  if (resp.ok) {
    return { ok: true, status: (await resp.json()) as AuthStatus }
  }
  switch (resp.status) {
    case 401:
      return { ok: false, error: 'Invalid username or password.' }
    case 429: {
      const retryAfter = Number(resp.headers.get('Retry-After')) || undefined
      return {
        ok: false,
        error: retryAfter
          ? `Too many attempts. Try again in ${retryAfter} second${retryAfter === 1 ? '' : 's'}.`
          : 'Too many attempts. Try again shortly.',
        retryAfter,
      }
    }
    case 403:
      return { ok: false, error: 'Sign-in was rejected because it came from an untrusted origin.' }
    default:
      return { ok: false, error: `Sign-in failed (HTTP ${resp.status}).` }
  }
}

// How long sign-out waits for the server before showing the login screen
// anyway (the session cookie then simply expires on its own).
const LOGOUT_TIMEOUT_MS = 5000

export async function logout(): Promise<void> {
  const ctrl = new AbortController()
  const timer = window.setTimeout(() => ctrl.abort(), LOGOUT_TIMEOUT_MS)
  try {
    await nativeFetch(apiUrl('/api/v1/auth/logout'), { method: 'POST', credentials: credentials(), signal: ctrl.signal })
  } catch {
    // The login screen is shown regardless; the cookie expires on its own.
  } finally {
    window.clearTimeout(timer)
  }
  // Cleared after the request, not before: components still mounted while
  // it was in flight could otherwise write keys back. AuthGate clears again
  // once the app has unmounted (clearSessionScopedStorage).
  clearSessionScopedStorage()
}

// Keys from before the infralens_ prefix convention, cleared with it.
const LEGACY_SESSION_KEYS = ['infralens.changes.lastSeen']

/**
 * Removes per-user browser state (the Changes "last seen" marker, cached AI
 * docs - every infralens_-prefixed key). Called whenever the login screen
 * takes over: sign-out, session expiry, any 401 that ends the session.
 */
export function clearSessionScopedStorage(): void {
  for (const store of [window.localStorage, window.sessionStorage]) {
    try {
      const keys: string[] = []
      for (let i = 0; i < store.length; i++) {
        const key = store.key(i)
        if (key && (key.startsWith(SESSION_SCOPED_STORAGE_PREFIX) || LEGACY_SESSION_KEYS.includes(key))) keys.push(key)
      }
      keys.forEach((key) => store.removeItem(key))
    } catch {
      // Storage disabled or inaccessible: nothing to clear.
    }
  }
}

export function notifyWebSocketRejected(rejected: boolean): void {
  window.dispatchEvent(new CustomEvent<boolean>(WS_REJECTED_EVENT, { detail: rejected }))
}

export function notifyUnauthorized(): void {
  window.dispatchEvent(new Event(UNAUTHORIZED_EVENT))
}

/**
 * For the WebSocket after it closes, why it may have closed:
 *  - 'unauthorized': no valid session; the login screen has been requested,
 *    do not retry.
 *  - 'ok': the backend answers and the session is fine, so a handshake that
 *    failed was refused for another reason (typically the Origin check
 *    behind a misconfigured proxy).
 *  - 'unreachable': the backend could not be asked (restarting, network) -
 *    retry as before.
 */
export type DropCause = 'unauthorized' | 'ok' | 'unreachable'

export async function checkAfterDisconnect(): Promise<DropCause> {
  const status = await fetchAuthStatus()
  if (!status) return 'unreachable'
  if (status.login_required && !status.authenticated) {
    notifyUnauthorized()
    return 'unauthorized'
  }
  return 'ok'
}

function requestUrl(input: RequestInfo | URL): string {
  const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  try {
    return new URL(raw, window.location.href).href
  } catch {
    return raw
  }
}

let interceptorInstalled = false

/** Wraps window.fetch so API 401s fire UNAUTHORIZED_EVENT. Idempotent. */
export function installAuthInterceptor(): void {
  if (interceptorInstalled) return
  interceptorInstalled = true

  const prefix = apiPrefix()
  const authPrefix = prefix + 'v1/auth/'

  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = requestUrl(input)
    if (!url.startsWith(prefix)) {
      return nativeFetch(input, init)
    }
    let effective = init
    if (includeCredentials && !(input instanceof Request) && init?.credentials === undefined) {
      effective = { ...init, credentials: 'include' }
    }
    const resp = await nativeFetch(input, effective)
    if (resp.status === 401 && !url.startsWith(authPrefix)) {
      notifyUnauthorized()
    }
    return resp
  }
}

export interface AuthContextValue {
  /** True when the backend requires login (UI_PASSWORD is set). */
  loginRequired: boolean
  username?: string
  logout: () => void
}

export const AuthContext = createContext<AuthContextValue>({
  loginRequired: false,
  logout: () => {},
})

export function useAuth(): AuthContextValue {
  return useContext(AuthContext)
}
