#!/usr/bin/env node
// One-command re-record of the README demo:
//
//   npm run record                 # build, run demo stack, record, encode, clean up
//   npm run record -- --help       # all options
//
// See README.md in this directory.

import fs from 'node:fs'
import path from 'node:path'
import { parseArgs } from 'node:util'
import { chromium } from 'playwright-core'

import { buildBackend, startBackend, startFrontend, reapStale, REPO_ROOT } from './lib/stack.mjs'
import { startCapture } from './lib/capture.mjs'
import { encodeAll } from './lib/encode.mjs'
import { Director } from './lib/director.mjs'
import { settings, liveArrival, scenes, sel } from './storyboard.mjs'

const HERE = import.meta.dirname
const { values: opt } = parseArgs({
  options: {
    'backend-port': { type: 'string', default: process.env.DEMO_BACKEND_PORT || '18931' },
    'frontend-port': { type: 'string', default: process.env.DEMO_FRONTEND_PORT || '18932' },
    out: { type: 'string', default: path.join(HERE, 'out') },
    'force-out': { type: 'boolean', default: false },
    install: { type: 'boolean', default: false },
    'gif-width': { type: 'string' },
    'gif-fps': { type: 'string' },
    'gif-quality': { type: 'string' },
    'no-captions': { type: 'boolean', default: false },
    'flow-dots': { type: 'boolean', default: false },
    'encode-only': { type: 'boolean', default: false },
    serve: { type: 'boolean', default: false },
    dev: { type: 'boolean', default: false },
    headed: { type: 'boolean', default: false },
    format: { type: 'string', default: 'png' },
    help: { type: 'boolean', short: 'h', default: false },
  },
})

if (opt.help) {
  console.log(`Usage: npm run record -- [options]

  --backend-port N     backend port (default 18931, env DEMO_BACKEND_PORT)
  --frontend-port N    frontend port (default 18932, env DEMO_FRONTEND_PORT)
  --out DIR            raw captures and outputs (default scripts/demo-gif/out; a
                       directory outside scripts/demo-gif needs --force-out, since
                       <out>/raw is deleted and rewritten on every run)
  --install            also copy the finished demo.gif into docs/
  --gif-width N        GIF width in px (default ${settings.gif.width})
  --gif-fps N          GIF frame rate (default ${settings.gif.fps})
  --gif-quality N      gifski quality 1-100 (default ${settings.gif.quality})
  --no-captions        record without the caption pills
  --flow-dots          keep the live-edge flow dots (records without prefers-reduced-motion;
                       roughly doubles the GIF size)
  --encode-only        re-encode the last raw capture in --out (no browser, no servers)
  --serve              only start the demo stack and keep it up (Ctrl-C to stop),
                       for exploring the UI while editing the storyboard
  --dev                serve the frontend with the Vite dev server instead of a production build
  --headed             show the browser window (capture still works)
  --format png|jpeg    screencast frame format (default png)`)
  process.exit(0)
}

const gifCfg = {
  ...settings.gif,
  ...(opt['gif-width'] && { width: Number(opt['gif-width']) }),
  ...(opt['gif-fps'] && { fps: Number(opt['gif-fps']) }),
  ...(opt['gif-quality'] && { quality: Number(opt['gif-quality']) }),
}

const outDir = path.resolve(opt.out)
if (path.relative(HERE, outDir).startsWith('..') || path.isAbsolute(path.relative(HERE, outDir))) {
  if (!opt['force-out']) {
    console.error(`--out ${outDir} is outside scripts/demo-gif; its raw/ subdirectory is deleted and ` +
      'rewritten on every run. Pass --force-out if that is really what you want.')
    process.exit(2)
  }
}
const rawDir = path.join(outDir, 'raw')

function installGif(gifPath) {
  const dest = path.join(REPO_ROOT, 'docs', 'demo.gif')
  fs.copyFileSync(gifPath, dest)
  console.log(`[install] ${path.relative(REPO_ROOT, dest)}`)
}

if (opt['encode-only']) {
  const r = encodeAll({ rawDir, outDir, gif: gifCfg, mp4: settings.mp4 })
  if (opt.install) installGif(r.gifPath)
  process.exit(0)
}

const cleanups = []
let cleaningUp = false
const cleanup = async () => {
  cleaningUp = true
  while (cleanups.length) {
    try { await cleanups.pop()() } catch (err) { console.error('[cleanup]', err.message) }
  }
}
const abort = async (code) => { if (!cleaningUp) { await cleanup(); process.exit(code) } }
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => abort(130))
const onDeath = () => { console.error('[abort] a demo server died - stopping'); abort(1) }

const sleep = ms => new Promise(r => setTimeout(r, ms))
const ts = ms => new Date(ms).toISOString().slice(11, 23)

async function driftEvents(backendUrl) {
  const res = await fetch(`${backendUrl}/api/v1/drift/events?limit=50`)
  if (!res.ok) throw new Error(`drift events: HTTP ${res.status}`)
  const body = await res.json()
  return body.events || []
}

/**
 * Predicts when the storyboard's live drift event will be detected. The
 * simulator applies the change `liveArrival.scriptAt` s after it starts, and
 * the detector reports it on its next pass; passes are a fixed interval apart,
 * so the phase is read off the first live detection (an earlier scripted
 * change) instead of being guessed.
 */
async function predictArrival(backend) {
  const { scriptAt, detectorInterval, match } = liveArrival
  for (;;) {
    const live = (await driftEvents(backend.url))
      .filter(e => Date.parse(e.detected_at) > backend.startedAt && !match(e))
    if (live.length) {
      const phase = Math.min(...live.map(e => Date.parse(e.detected_at)))
      const applyAt = backend.startedAt + scriptAt * 1000
      let pass = phase
      // The change only becomes visible to the detector after the simulator's
      // next 2 s tick writes it; a pass that lands within that window misses
      // it, and the event arrives one interval later. Require a clear margin.
      while (pass < applyAt + 3000) pass += detectorInterval * 1000
      console.log(`[timing] detector phase ${ts(phase)}, change applied ~${ts(applyAt)}, expected in the feed ~${ts(pass)}`)
      return { applyAt, arrivalAt: pass }
    }
    await sleep(1000)
  }
}

try {
  await reapStale()
  const backendPort = Number(opt['backend-port'])
  const frontendPort = Number(opt['frontend-port'])
  const bin = buildBackend()
  const backend = await startBackend({ port: backendPort, bin, onDeath })
  cleanups.push(backend.stop)
  const frontend = await startFrontend({ port: frontendPort, backendPort, dev: opt.dev, onDeath })
  cleanups.push(frontend.stop)

  if (opt.serve) {
    console.log(`[serve] ${frontend.url} (backend ${backend.url}) - Ctrl-C to stop`)
    await new Promise(() => {})
  }

  const { applyAt, arrivalAt } = await predictArrival(backend)
  const captureAt = arrivalAt - liveArrival.leadSeconds * 1000
  const openAt = captureAt - settings.pageSettleSeconds * 1000
  if (openAt > Date.now()) {
    console.log(`[timing] browser opens in ${((openAt - Date.now()) / 1000).toFixed(0)} s, capture at ${ts(captureAt)}`)
    await sleep(openAt - Date.now())
  }

  const browser = await chromium.launch({ channel: 'chrome', headless: !opt.headed })
  cleanups.push(() => browser.close())
  const context = await browser.newContext({
    viewport: settings.viewport,
    deviceScaleFactor: settings.deviceScaleFactor,
    locale: settings.locale,
    timezoneId: settings.timezoneId,
    colorScheme: 'dark',
    reducedMotion: settings.reducedMotion && !opt['flow-dots'] ? 'reduce' : 'no-preference',
  })
  const page = await context.newPage()
  page.on('pageerror', err => console.error('[page error]', err.message))
  const d = new Director(page, { captions: settings.captions && !opt['no-captions'] })
  await d.install()
  // Retried: Chrome has been seen to fail the very first navigation of a
  // fresh profile with a transient net:: error.
  for (let attempt = 1; ; attempt++) {
    try {
      await page.goto(frontend.url)
      break
    } catch (err) {
      if (attempt >= 3) throw err
      console.warn(`[browser] navigation failed (${err.message.split('\n')[0]}), retrying`)
      await sleep(1000)
    }
  }
  await page.locator(sel.node('10.0.2.10/api-gateway')).waitFor({ timeout: 20000 })
  await page.locator(sel.rail('history')).waitFor({ timeout: 40000 })
  await d.install() // overlay present even if the init script raced the app
  await page.mouse.move(settings.cursorRest.x, settings.cursorRest.y)
  d.pos = { ...settings.cursorRest }
  if (Date.now() < captureAt - 1500) await sleep(captureAt - 1500 - Date.now())

  // Hero still: the opening state, without cursor or caption.
  fs.mkdirSync(outDir, { recursive: true })
  const heroPath = path.join(outDir, 'demo-hero.png')
  await page.evaluate(() => { document.getElementById('__demo_cursor').style.visibility = 'hidden' })
  await page.screenshot({ path: heroPath })
  await page.evaluate(() => { document.getElementById('__demo_cursor').style.visibility = '' })

  const world = { applyAt, arrivalAt, backendUrl: backend.url }
  const ctx = { d, page, sel, world }
  if (scenes[0].setup) await scenes[0].setup(ctx)
  if (Date.now() < captureAt) await sleep(captureAt - Date.now())
  const vp = settings.viewport
  const cap = await startCapture(page, rawDir, {
    width: vp.width * settings.deviceScaleFactor,
    height: vp.height * settings.deviceScaleFactor,
    format: opt.format,
  })
  const t0 = Date.now()
  console.log(`[timing] capture started ${ts(t0)} (${((arrivalAt - t0) / 1000).toFixed(1)} s before the expected arrival)`)
  const marks = []
  for (const scene of scenes) {
    const at = Date.now()
    console.log(`[scene] ${scene.name}`)
    await scene.run(ctx)
    marks.push({ scene: scene.name, start: (at - t0) / 1000, seconds: (Date.now() - at) / 1000 })
  }
  await cap.stop()
  fs.writeFileSync(path.join(rawDir, 'scenes.json'), JSON.stringify(marks, null, 1))
  console.table(marks)
  const arrived = (await driftEvents(backend.url)).find(liveArrival.match)
  if (arrived) console.log(`[timing] live event detected at ${ts(Date.parse(arrived.detected_at))} = capture +${((Date.parse(arrived.detected_at) - t0) / 1000).toFixed(1)} s`)

  await cleanup()

  const r = encodeAll({ rawDir, outDir, gif: gifCfg, mp4: settings.mp4 })
  if (opt.install) installGif(r.gifPath)
  console.log(`[done] ${outDir}/{demo.gif,demo.mp4,demo-hero.png}`)
} catch (err) {
  console.error(err)
  await abort(1)
}
