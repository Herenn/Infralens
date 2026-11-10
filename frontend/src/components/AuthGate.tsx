import { ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import LoginScreen from './LoginScreen'
import {
  AuthContext,
  AuthContextValue,
  AuthStatus,
  UNAUTHORIZED_EVENT,
  WS_REJECTED_EVENT,
  fetchAuthStatus,
  installAuthInterceptor,
  logout,
} from '../lib/auth'

type GateState =
  | { phase: 'checking' }
  | { phase: 'login'; notice?: string }
  | { phase: 'ready'; status: AuthStatus | null }

/**
 * Renders the login screen instead of its children when the backend requires
 * login and there is no session. When login is not required (UI_PASSWORD
 * unset) - or the backend cannot be asked - children render exactly as
 * before. Unmounting the app on logout or session loss also closes its
 * WebSocket, which is what stops it from reconnecting.
 */
export default function AuthGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<GateState>({ phase: 'checking' })
  const [wsRejected, setWsRejected] = useState(false)
  const checkingRef = useRef(false)

  const evaluate = useCallback(async (notice?: string) => {
    // Many requests can 401 at once; one status check answers them all.
    if (checkingRef.current) return
    checkingRef.current = true
    try {
      const status = await fetchAuthStatus()
      if (status?.login_required && !status.authenticated) {
        setState((prev) => (prev.phase === 'login' ? prev : { phase: 'login', notice }))
      } else {
        setState((prev) =>
          prev.phase === 'ready' &&
          prev.status?.login_required === status?.login_required &&
          prev.status?.username === status?.username
            ? prev
            : { phase: 'ready', status }
        )
      }
    } finally {
      checkingRef.current = false
    }
  }, [])

  useEffect(() => {
    installAuthInterceptor()
    void evaluate()
    const onUnauthorized = () => {
      void evaluate('Your session has ended. Please sign in again.')
    }
    const onWsRejected = (event: Event) => {
      setWsRejected((event as CustomEvent<boolean>).detail)
    }
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized)
    window.addEventListener(WS_REJECTED_EVENT, onWsRejected)
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized)
      window.removeEventListener(WS_REJECTED_EVENT, onWsRejected)
    }
  }, [evaluate])

  const handleLogout = useCallback(async () => {
    await logout()
    setWsRejected(false)
    setState({ phase: 'login' })
  }, [])

  const handleLoggedIn = useCallback((status: AuthStatus) => {
    setState({ phase: 'ready', status })
  }, [])

  const readyStatus = state.phase === 'ready' ? state.status : null
  const contextValue = useMemo<AuthContextValue>(
    () => ({
      loginRequired: !!readyStatus?.login_required,
      username: readyStatus?.username,
      logout: () => void handleLogout(),
    }),
    [readyStatus, handleLogout]
  )

  if (state.phase === 'checking') {
    return <div className="h-screen w-screen bg-dark-950" />
  }
  if (state.phase === 'login') {
    return <LoginScreen notice={state.notice} onLoggedIn={handleLoggedIn} />
  }
  return (
    <AuthContext.Provider value={contextValue}>
      {children}
      {wsRejected && (
        <div
          role="alert"
          className="fixed top-16 left-1/2 -translate-x-1/2 z-[100000] max-w-xl px-4 py-3 rounded-lg text-xs
            bg-amber-500/10 text-amber-200 border border-amber-500/30 shadow-2xl backdrop-blur-sm"
        >
          <span className="font-semibold text-amber-300">Live updates are being refused by the server.</span>{' '}
          The API answers and you are signed in, but the WebSocket handshake keeps failing - usually a reverse
          proxy that rewrites the Host header without sending X-Forwarded-Host. Retrying with backoff.
        </div>
      )}
    </AuthContext.Provider>
  )
}
