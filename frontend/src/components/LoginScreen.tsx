import { FormEvent, useEffect, useState } from 'react'
import { Activity, Lock, LogIn, User } from 'lucide-react'
import { AuthStatus, login } from '../lib/auth'

interface LoginScreenProps {
  /** Shown above the form, e.g. when an existing session has ended. */
  notice?: string
  onLoggedIn: (status: AuthStatus) => void
}

export default function LoginScreen({ notice, onLoggedIn }: LoginScreenProps) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  // While throttled, the button stays disabled until the backend's
  // Retry-After has passed.
  const [lockedUntil, setLockedUntil] = useState<number | null>(null)
  const [, setTick] = useState(0)

  useEffect(() => {
    if (lockedUntil === null) return
    const timer = window.setInterval(() => {
      if (Date.now() >= lockedUntil) {
        setLockedUntil(null)
        setError(null)
      } else {
        setTick((t) => t + 1)
      }
    }, 1000)
    return () => window.clearInterval(timer)
  }, [lockedUntil])

  const locked = lockedUntil !== null && Date.now() < lockedUntil
  const secondsLeft = locked ? Math.ceil((lockedUntil - Date.now()) / 1000) : 0

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    if (submitting || locked || !password) return
    setSubmitting(true)
    setError(null)
    const result = await login(username.trim(), password)
    setSubmitting(false)
    if (result.ok) {
      onLoggedIn(result.status)
      return
    }
    setPassword('')
    setError(result.error)
    if (result.retryAfter) {
      setLockedUntil(Date.now() + result.retryAfter * 1000)
    }
  }

  const inputClass =
    'w-full pl-9 pr-3 py-2 rounded-lg text-sm bg-dark-800 text-dark-100 border border-dark-700 ' +
    'placeholder:text-dark-500 focus:outline-none focus:border-lens-500/50 focus:ring-1 focus:ring-lens-500/30 transition-all'

  return (
    <div className="h-screen w-screen flex items-center justify-center bg-dark-950 px-4">
      <div className="w-full max-w-sm">
        <div className="flex flex-col items-center mb-8">
          <div className="w-12 h-12 rounded-xl bg-gradient-to-br from-lens-500 to-lens-600 flex items-center justify-center mb-4 shadow-lg shadow-lens-500/20">
            <Activity size={26} className="text-white" />
          </div>
          <h1 className="text-2xl font-semibold tracking-tight text-dark-100">InfraLens</h1>
          <p className="text-sm text-dark-400 mt-1">Sign in to view your service topology</p>
        </div>

        <form
          onSubmit={handleSubmit}
          className="bg-dark-900/80 border border-dark-800 rounded-xl p-6 shadow-2xl space-y-4"
        >
          {notice && !error && (
            <div className="text-xs text-amber-300 bg-amber-500/10 border border-amber-500/20 rounded-lg px-3 py-2">
              {notice}
            </div>
          )}

          <div>
            <label htmlFor="login-username" className="block text-xs font-medium text-dark-400 mb-1.5">
              Username
            </label>
            <div className="relative">
              <User size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-dark-500 pointer-events-none" />
              <input
                id="login-username"
                type="text"
                autoComplete="username"
                autoFocus
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="admin"
                className={inputClass}
              />
            </div>
          </div>

          <div>
            <label htmlFor="login-password" className="block text-xs font-medium text-dark-400 mb-1.5">
              Password
            </label>
            <div className="relative">
              <Lock size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-dark-500 pointer-events-none" />
              <input
                id="login-password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className={inputClass}
              />
            </div>
          </div>

          {error && (
            <div role="alert" className="text-xs text-red-400 bg-red-500/10 border border-red-500/20 rounded-lg px-3 py-2">
              {locked ? `Too many attempts. Try again in ${secondsLeft}s.` : error}
            </div>
          )}

          <button
            type="submit"
            disabled={submitting || locked || !password}
            className="w-full flex items-center justify-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all
              bg-lens-600 text-white hover:bg-lens-500 disabled:bg-dark-800 disabled:text-dark-500 disabled:cursor-not-allowed"
          >
            <LogIn size={15} />
            {submitting ? 'Signing in…' : 'Sign in'}
          </button>
        </form>
      </div>
    </div>
  )
}
