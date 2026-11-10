// Starts (and reliably stops) the InfraLens stack used for the recording:
// the Go backend in DEMO_MODE and the frontend served by Vite, proxied to it.
//
// Nothing here edits application code or config: the backend is configured
// purely through env vars, and Vite is driven through its JS API with an
// inline config that only overrides ports, the proxy target and the build
// output directory (merged on top of frontend/vite.config.ts).

import { spawn, execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

export const REPO_ROOT = path.resolve(import.meta.dirname, '../../..')
export const FRONTEND_DIR = path.join(REPO_ROOT, 'frontend')
export const CACHE_DIR = path.resolve(import.meta.dirname, '../.cache')

const log = (...a) => console.log('[stack]', ...a)

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
    await new Promise(r => setTimeout(r, 250))
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

export function buildBackend() {
  fs.mkdirSync(CACHE_DIR, { recursive: true })
  // Deliberately not named "infralens-backend": other developers on this
  // machine run `pkill -f infralens-backend` for their own servers, which
  // would otherwise kill this one mid-recording.
  const bin = path.join(CACHE_DIR, 'demo-gif-backend')
  log('building backend ->', path.relative(REPO_ROOT, bin))
  execFileSync('go', ['build', '-o', bin, './backend'], { cwd: REPO_ROOT, stdio: 'inherit' })
  return bin
}

export async function startBackend({ port, bin }) {
  await assertPortFree(port)
  // A fresh database every run, so history starts at the moment of this run
  // and the recording never depends on leftovers from a previous one.
  const dbPath = path.join(CACHE_DIR, 'demo.db')
  for (const suffix of ['', '-wal', '-shm', '-journal']) fs.rmSync(dbPath + suffix, { force: true })

  const logFile = path.join(CACHE_DIR, 'backend.log')
  const out = fs.openSync(logFile, 'w')
  const child = spawn(bin, [], {
    cwd: CACHE_DIR,
    env: {
      ...process.env,
      DEMO_MODE: 'true',
      ALLOW_NO_AUTH: 'true',
      LISTEN_ADDR: `127.0.0.1:${port}`,
      DB_DRIVER: 'sqlite',
      DB_DSN: dbPath,
      // Workaround, not a preference: in v3.0 the SQLite history store
      // persists timestamps as Go time strings in the process's local zone
      // and compares them as text against the UTC instants the UI sends, so
      // on a non-UTC host time travel shows an empty graph and "Compare to
      // now" always reports 0 changes. The Docker images run in UTC, which is
      // why the shipped demo looks fine there. See the phase-1 report.
      TZ: 'UTC',
    },
    stdio: ['ignore', out, out],
  })
  let stopping = false
  child.on('exit', (code, signal) => {
    if (stopping) return
    // A recording of a dead backend is worthless: abort the whole run (the
    // SIGTERM handler in record.mjs cleans up the rest).
    log(`backend died unexpectedly (code ${code}, signal ${signal}) - see ${logFile}; aborting`)
    process.kill(process.pid, 'SIGTERM')
  })
  // Last-resort cleanup: never leave a backend behind on another developer's
  // machine, whatever way this process ends.
  process.on('exit', () => { if (child.exitCode === null) child.kill('SIGKILL') })
  await waitFor(`http://127.0.0.1:${port}/health`, 30000, 'backend')
  log(`backend ready on :${port} (log: ${path.relative(REPO_ROOT, logFile)})`)
  return {
    url: `http://127.0.0.1:${port}`,
    stop: () => new Promise(resolve => {
      stopping = true
      if (child.exitCode !== null || child.signalCode !== null) return resolve()
      child.once('exit', resolve)
      child.kill('SIGTERM')
      setTimeout(() => child.kill('SIGKILL'), 5000).unref()
    }),
  }
}

async function loadVite() {
  const pkg = JSON.parse(fs.readFileSync(path.join(FRONTEND_DIR, 'node_modules/vite/package.json'), 'utf8'))
  const entry = typeof pkg.exports['.'] === 'string' ? pkg.exports['.'] : pkg.exports['.'].import
  return import(pathToFileURL(path.join(FRONTEND_DIR, 'node_modules/vite', entry)).href)
}

export async function startFrontend({ port, backendPort, dev = false }) {
  if (!fs.existsSync(path.join(FRONTEND_DIR, 'node_modules'))) {
    log('installing frontend dependencies (npm ci)')
    execFileSync('npm', ['ci', '--no-audit', '--no-fund'], { cwd: FRONTEND_DIR, stdio: 'inherit' })
  }
  await assertPortFree(port)
  // PostCSS/Tailwind resolve their config and `content` globs against the
  // process cwd, not Vite's root: building from anywhere else silently ships
  // a page with almost no styles.
  process.chdir(FRONTEND_DIR)
  const vite = await loadVite()
  const proxy = {
    '/api': { target: `http://127.0.0.1:${backendPort}`, changeOrigin: true, ws: true },
  }
  const common = {
    root: FRONTEND_DIR,
    configFile: path.join(FRONTEND_DIR, 'vite.config.ts'),
    logLevel: 'warn',
    clearScreen: false,
  }

  let server
  if (dev) {
    server = await vite.createServer({ ...common, server: { port, strictPort: true, host: '127.0.0.1', proxy } })
    await server.listen()
  } else {
    // Production build: what users actually get, and no dev-mode overhead
    // competing with the screen capture for CPU.
    const outDir = path.join(CACHE_DIR, 'dist')
    log('building frontend (vite build) ->', path.relative(REPO_ROOT, outDir))
    await vite.build({ ...common, build: { outDir, emptyOutDir: true } })
    server = await vite.preview({ ...common, build: { outDir }, preview: { port, strictPort: true, host: '127.0.0.1', proxy } })
  }
  const url = `http://127.0.0.1:${port}`
  await waitFor(url, 30000, 'frontend')
  log(`frontend ready on :${port} (${dev ? 'vite dev' : 'vite preview of production build'})`)
  return {
    url,
    stop: async () => {
      if (server.close) await server.close()
      else if (server.httpServer) await new Promise(r => server.httpServer.close(r))
    },
  }
}
