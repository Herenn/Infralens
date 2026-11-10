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

import { buildBackend, startBackend, startFrontend, REPO_ROOT } from './lib/stack.mjs'
import { startWorkload } from './lib/scenario.mjs'
import { startCapture } from './lib/capture.mjs'
import { encodeAll } from './lib/encode.mjs'
import { Director } from './lib/director.mjs'
import { settings, scenario, scenes, sel } from './storyboard.mjs'

const HERE = import.meta.dirname
const { values: opt } = parseArgs({
  options: {
    'backend-port': { type: 'string', default: process.env.DEMO_BACKEND_PORT || '18931' },
    'frontend-port': { type: 'string', default: process.env.DEMO_FRONTEND_PORT || '18932' },
    out: { type: 'string', default: path.join(HERE, 'out') },
    install: { type: 'boolean', default: false },
    'gif-width': { type: 'string' },
    'gif-fps': { type: 'string' },
    'gif-quality': { type: 'string' },
    'record-at': { type: 'string' },
    'no-captions': { type: 'boolean', default: false },
    'no-scenario': { type: 'boolean', default: false },
    'encode-only': { type: 'boolean', default: false },
    'keep-running': { type: 'boolean', default: false },
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
  --out DIR            where raw captures and outputs go (default scripts/demo-gif/out)
  --install            also copy demo.gif / demo.mp4 / demo-hero.png into docs/
  --gif-width N        GIF width in px (default ${settings.gif.width})
  --gif-fps N          GIF frame rate (default ${settings.gif.fps})
  --gif-quality N      gifski quality 1-100 (default ${settings.gif.quality})
  --record-at S        seconds of warm-up before recording (default ${settings.warmup.recordAt})
  --no-captions        record without the caption pills
  --no-scenario        do not inject the extra workload (bare simulator)
  --encode-only        re-encode the last raw capture in --out (no browser, no servers)
  --keep-running       leave the stack up after recording (Ctrl-C to stop)
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
const rawDir = path.join(outDir, 'raw')

function install(files) {
  const docs = path.join(REPO_ROOT, 'docs')
  for (const [src, name] of files) {
    fs.copyFileSync(src, path.join(docs, name))
    console.log(`[install] docs/${name}`)
  }
}

if (opt['encode-only']) {
  const r = encodeAll({ rawDir, outDir, gif: gifCfg, mp4: settings.mp4 })
  if (opt.install) install([[r.gifPath, 'demo.gif'], [r.mp4Path, 'demo.mp4']])
  process.exit(0)
}

const cleanups = []
const cleanup = async () => {
  while (cleanups.length) {
    try { await cleanups.pop()() } catch (err) { console.error('[cleanup]', err.message) }
  }
}
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, async () => { await cleanup(); process.exit(130) })

const sleepUntil = async (t0, seconds, label) => {
  const ms = t0 + seconds * 1000 - Date.now()
  if (ms > 0) {
    console.log(`[warmup] ${label} in ${(ms / 1000).toFixed(0)} s`)
    await new Promise(r => setTimeout(r, ms))
  }
}

try {
  const backendPort = Number(opt['backend-port'])
  const frontendPort = Number(opt['frontend-port'])
  const bin = buildBackend()
  const backend = await startBackend({ port: backendPort, bin })
  cleanups.push(backend.stop)
  const t0 = Date.now()
  const frontend = await startFrontend({ port: frontendPort, backendPort, dev: opt.dev })
  cleanups.push(frontend.stop)

  // Warm-up: let history accumulate, then add the scenario's new workload.
  const world = { startedMs: t0 }
  const recordAt = Number(opt['record-at'] || settings.warmup.recordAt)
  if (scenario && !opt['no-scenario']) {
    await sleepUntil(t0, settings.warmup.workloadAt, 'new workload starts')
    world.workloadAt = Date.now()
    const w = await startWorkload(backend.url, scenario)
    cleanups.push(w.stop)
  }
  // The instant the time-travel/diff scenes scrub back to: half-way between
  // the start of history and the workload appearing.
  world.beforeWorkloadMs = world.workloadAt ? t0 + (world.workloadAt - t0) / 2 : t0 + (Date.now() - t0) / 2
  await sleepUntil(t0, recordAt - 6, 'browser opens')

  const browser = await chromium.launch({ channel: 'chrome', headless: !opt.headed })
  cleanups.push(() => browser.close())
  const context = await browser.newContext({
    viewport: settings.viewport,
    deviceScaleFactor: settings.deviceScaleFactor,
    locale: settings.locale,
    timezoneId: settings.timezoneId,
    colorScheme: 'dark',
  })
  const page = await context.newPage()
  page.on('pageerror', err => console.error('[page error]', err.message))
  const d = new Director(page, { captions: settings.captions && !opt['no-captions'] })
  await d.install()
  await page.goto(frontend.url)
  await page.locator(sel.node('10.0.2.10/api-gateway')).waitFor({ timeout: 20000 })
  await page.locator('button:has-text("History")').waitFor({ timeout: 40000 })
  await d.install() // overlay present even if the init script raced the app
  await page.mouse.move(settings.cursorRest.x, settings.cursorRest.y)
  d.pos = { ...settings.cursorRest }
  await d.wait(2500) // initial layout + fitView + a couple of live updates

  // Hero still: the opening state, without cursor or caption.
  fs.mkdirSync(outDir, { recursive: true })
  const heroPath = path.join(outDir, 'demo-hero.png')
  await page.evaluate(() => { document.getElementById('__demo_cursor').style.visibility = 'hidden' })
  await page.screenshot({ path: heroPath })
  await page.evaluate(() => { document.getElementById('__demo_cursor').style.visibility = '' })

  const ctx = { d, page, sel, world }
  if (scenes[0].setup) await scenes[0].setup(ctx)
  const vp = settings.viewport
  const cap = await startCapture(page, rawDir, {
    width: vp.width * settings.deviceScaleFactor,
    height: vp.height * settings.deviceScaleFactor,
    format: opt.format,
  })
  const marks = []
  for (const scene of scenes) {
    const at = Date.now()
    console.log(`[scene] ${scene.name}`)
    await scene.run(ctx)
    marks.push({ scene: scene.name, seconds: (Date.now() - at) / 1000 })
  }
  await cap.stop()
  fs.writeFileSync(path.join(rawDir, 'scenes.json'), JSON.stringify(marks, null, 1))
  console.table(marks)

  if (opt['keep-running']) {
    console.log(`[stack] still running: ${frontend.url} - Ctrl-C to stop`)
    await new Promise(() => {})
  }
  await cleanup()

  const r = encodeAll({ rawDir, outDir, gif: gifCfg, mp4: settings.mp4 })
  if (opt.install) install([[r.gifPath, 'demo.gif'], [r.mp4Path, 'demo.mp4'], [heroPath, 'demo-hero.png']])
  console.log(`[done] ${path.relative(process.cwd(), outDir)}/{demo.gif,demo.mp4,demo-hero.png}`)
} catch (err) {
  console.error(err)
  await cleanup()
  process.exit(1)
}
