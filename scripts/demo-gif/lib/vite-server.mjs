// Child process that builds and serves the frontend for the recording.
//
// It runs with cwd = frontend/ (set by the parent's spawn), because PostCSS
// and Tailwind resolve their config and `content` globs against the process
// cwd rather than Vite's root - building from anywhere else silently ships a
// page with almost no styles. Running it as a child keeps the recorder's own
// cwd untouched and gives the parent a PID it can stop (or reap later).
//
// Usage: node vite-server.mjs <frontendDir> <port> <backendPort> <outDir> <dev:0|1>
// Prints "READY <url>" on stdout once listening.

import fs from 'node:fs'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const [frontendDir, port, backendPort, outDir, dev] = process.argv.slice(2)

const pkg = JSON.parse(fs.readFileSync(path.join(frontendDir, 'node_modules/vite/package.json'), 'utf8'))
const entry = typeof pkg.exports['.'] === 'string' ? pkg.exports['.'] : pkg.exports['.'].import
const vite = await import(pathToFileURL(path.join(frontendDir, 'node_modules/vite', entry)).href)

// Merged on top of frontend/vite.config.ts: only the ports, the proxy target
// and the build output change. changeOrigin stays false like the app's own
// config (the backend compares the WebSocket's Origin with Host).
const proxy = {
  '/api': { target: `http://127.0.0.1:${backendPort}`, changeOrigin: false, ws: true },
}
const common = {
  root: frontendDir,
  configFile: path.join(frontendDir, 'vite.config.ts'),
  logLevel: 'warn',
  clearScreen: false,
}
const server = { port: Number(port), strictPort: true, host: '127.0.0.1', proxy }

let handle
if (dev === '1') {
  handle = await vite.createServer({ ...common, server })
  await handle.listen()
} else {
  // Production build: what users actually get, and no dev-mode overhead
  // competing with the screen capture for CPU.
  await vite.build({ ...common, build: { outDir, emptyOutDir: true } })
  handle = await vite.preview({ ...common, build: { outDir }, preview: server })
}
console.log(`READY http://127.0.0.1:${port}`)

const stop = async () => {
  try { await handle.close?.() } finally { process.exit(0) }
}
process.on('SIGTERM', stop)
process.on('SIGINT', stop)
