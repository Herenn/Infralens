// Starts (and reliably stops) the InfraLens stack used for the recording:
// the Go backend in DEMO_MODE and the frontend served by Vite, proxied to it.
//
// Nothing here edits application code or config: the backend is configured
// purely through env vars, and Vite gets an inline config merged on top of
// frontend/vite.config.ts that only overrides ports, the proxy target and the
// build output directory (see vite-server.mjs).
//
// Process hygiene: both servers run in their own process group, and their
// PIDs are recorded in .cache/pids/. If this recorder is killed outright
// (SIGKILL, a crashed terminal) and leaves them running, the next run finds
// the PID files, verifies each PID still belongs to *this* script's binary or
// Vite wrapper, and stops it before starting fresh.

import { spawn, execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'

export const REPO_ROOT = path.resolve(import.meta.dirname, '../../..')
export const FRONTEND_DIR = path.join(REPO_ROOT, 'frontend')
export const CACHE_DIR = path.resolve(import.meta.dirname, '../.cache')
const PID_DIR = path.join(CACHE_DIR, 'pids')
// Uniquely named so it is never mistaken for (or by) any other InfraLens
// backend running on the same machine.
const BACKEND_BIN = path.join(CACHE_DIR, 'demo-gif-backend')
const VITE_SERVER = path.join(import.meta.dirname, 'vite-server.mjs')

const log = (...a) => console.log('[stack]', ...a)
const sleep = ms => new Promise(r => setTimeout(r, ms))

async function waitFor(url, timeoutMs, label) {
  const deadline = Date.now() + timeoutMs
  let lastErr
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url)
      if (res.ok) return
      lastErr = new Error(`HTTP ${res.status}`)
    } catch (err) {
      lastErr = err
    }
    await sleep(250)
  }
  throw new Error(`${label} did not become ready at ${url}: ${lastErr?.message}`)
}

async function assertPortFree(port) {
  try {
    await fetch(`http://127.0.0.1:${port}/`, { signal: AbortSignal.timeout(500) })
  } catch (err) {
    if (err?.cause?.code === 'ECONNREFUSED') return
    if (err?.name === 'TimeoutError') throw new Error(`port ${port} is busy (something is listening)`)
    return
  }
  throw new Error(`port ${port} is already in use - pass a different port (see README)`)
}

function commandOf(pid) {
  try {
    return execFileSync('ps', ['-p', String(pid), '-o', 'command='], { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim()
  } catch {
    return null // no such process
  }
}

function alive(pid) {
  try { process.kill(pid, 0); return true } catch { return false }
}

function killGroup(pid, signal) {
  try { process.kill(-pid, signal) } catch { try { process.kill(pid, signal) } catch { /* gone */ } }
}

/** Stops leftovers of an earlier run that died without cleaning up. */
export async function reapStale() {
  if (!fs.existsSync(PID_DIR)) return
  for (const f of fs.readdirSync(PID_DIR)) {
    const file = path.join(PID_DIR, f)
    const { pid, marker } = JSON.parse(fs.readFileSync(file, 'utf8'))
    const cmd = commandOf(pid)
    if (cmd && cmd.includes(marker)) {
      log(`stopping stale ${f.replace(/\.json$/, '')} from an earlier run (pid ${pid})`)
      killGroup(pid, 'SIGTERM')
      for (let i = 0; i < 20 && alive(pid); i++) await sleep(250)
      if (alive(pid)) killGroup(pid, 'SIGKILL')
    } else if (cmd) {
      log(`pid ${pid} from ${f} is now a different process - leaving it alone`)
    }
    fs.rmSync(file, { force: true })
  }
}

/**
 * Spawns a server in its own process group and records its PID. `marker` is
 * a string that must appear in the process's command line for a later run to
 * treat the PID as ours.
 */
function spawnManaged(name, cmd, args, { marker, onUnexpectedExit, ...opts }) {
  fs.mkdirSync(PID_DIR, { recursive: true })
  const child = spawn(cmd, args, { ...opts, detached: true })
  const pidFile = path.join(PID_DIR, `${name}.json`)
  fs.writeFileSync(pidFile, JSON.stringify({ pid: child.pid, marker }))
  let stopping = false
  child.on('exit', (code, signal) => {
    fs.rmSync(pidFile, { force: true })
    if (!stopping) onUnexpectedExit?.(code, signal)
  })
  // Last resort if this process exits without running stop(); SIGKILL of
  // the recorder itself is covered by reapStale() on the next run.
  process.on('exit', () => { if (child.exitCode === null && child.signalCode === null) killGroup(child.pid, 'SIGKILL') })
  const stop = () => new Promise(resolve => {
    stopping = true
    if (child.exitCode !== null || child.signalCode !== null) return resolve()
    child.once('exit', () => resolve())
    killGroup(child.pid, 'SIGTERM')
    setTimeout(() => killGroup(child.pid, 'SIGKILL'), 5000).unref()
  })
  return { child, stop }
}

export function buildBackend() {
  fs.mkdirSync(CACHE_DIR, { recursive: true })
  log('building backend ->', path.relative(REPO_ROOT, BACKEND_BIN))
  execFileSync('go', ['build', '-o', BACKEND_BIN, './backend'], { cwd: REPO_ROOT, stdio: 'inherit' })
  return BACKEND_BIN
}

export async function startBackend({ port, bin, onDeath }) {
  await assertPortFree(port)
  // A fresh database every run: demo mode seeds its 14-day past and drift
  // feed only into an empty database, and its live script is timed from the
  // simulator's start.
  const dbPath = path.join(CACHE_DIR, 'demo.db')
  for (const suffix of ['', '-wal', '-shm', '-journal']) fs.rmSync(dbPath + suffix, { force: true })

  const logFile = path.join(CACHE_DIR, 'backend.log')
  const out = fs.openSync(logFile, 'w')
  const startedAt = Date.now()
  const { stop } = spawnManaged('backend', bin, [], {
    marker: bin,
    cwd: CACHE_DIR,
    env: {
      ...process.env,
      DEMO_MODE: 'true',
      ALLOW_NO_AUTH: 'true',
      LISTEN_ADDR: `127.0.0.1:${port}`,
      DB_DRIVER: 'sqlite',
      DB_DSN: dbPath,
    },
    stdio: ['ignore', out, out],
    // A recording of a dead backend is worthless: abort the whole run.
    onUnexpectedExit: (code, signal) => {
      log(`backend died unexpectedly (code ${code}, signal ${signal}) - see ${logFile}`)
      onDeath?.()
    },
  })
  await waitFor(`http://127.0.0.1:${port}/health`, 30000, 'backend')
  log(`backend ready on :${port} (log: ${path.relative(REPO_ROOT, logFile)})`)
  return { url: `http://127.0.0.1:${port}`, startedAt, stop }
}

export async function startFrontend({ port, backendPort, dev = false, onDeath }) {
  if (!fs.existsSync(path.join(FRONTEND_DIR, 'node_modules'))) {
    log('frontend/node_modules missing - installing (npm ci in frontend/)')
    execFileSync('npm', ['ci', '--no-audit', '--no-fund'], { cwd: FRONTEND_DIR, stdio: 'inherit' })
  }
  await assertPortFree(port)
  const outDir = path.join(CACHE_DIR, 'dist')
  if (!dev) log('building frontend (vite build) ->', path.relative(REPO_ROOT, outDir))
  const { child, stop } = spawnManaged('frontend', process.execPath,
    [VITE_SERVER, FRONTEND_DIR, String(port), String(backendPort), outDir, dev ? '1' : '0'], {
      marker: VITE_SERVER,
      cwd: FRONTEND_DIR, // see vite-server.mjs for why
      stdio: ['ignore', 'pipe', 'inherit'],
      onUnexpectedExit: (code, signal) => {
        log(`frontend server exited unexpectedly (code ${code}, signal ${signal})`)
        onDeath?.()
      },
    })
  await new Promise((resolve, reject) => {
    let buf = ''
    child.stdout.on('data', d => {
      buf += d
      if (buf.includes('READY')) resolve()
    })
    child.once('exit', () => reject(new Error('frontend server failed to start')))
  })
  const url = `http://127.0.0.1:${port}`
  await waitFor(url, 30000, 'frontend')
  log(`frontend ready on :${port} (${dev ? 'vite dev' : 'vite preview of production build'})`)
  return { url, stop }
}
