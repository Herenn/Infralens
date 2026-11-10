// The storyboard: WHAT the demo shows, in order. Edit this file to re-shoot.
//
// Each scene is { name, run(ctx) }. ctx gives you:
//   d      - Director (lib/director.mjs): d.move / d.click / d.drag / d.type /
//            d.press / d.wait / d.caption(html | null)
//   page   - the Playwright page
//   sel    - selector helpers below
//   world  - timestamps of the warm-up (e.g. world.workloadAt) for history
//
// Timing guideline: ~2 s of stillness after each reveal so it registers,
// cursor moves of 0.5-1 s, and the whole loop at 25-30 s. The GIF loops, so
// the last scene must leave the UI exactly as the first frame shows it.

/** Recording/encoding settings. CLI flags override these (see README). */
export const settings = {
  viewport: { width: 1280, height: 800 }, // CSS px; captured at deviceScaleFactor 2
  deviceScaleFactor: 2,
  locale: 'en-US',
  timezoneId: 'UTC',
  // Warm-up before recording (seconds since backend start). History needs
  // to exist before the moment the time-travel scene scrubs back to.
  warmup: {
    workloadAt: 40, // when the scenario's new workload starts reporting
    recordAt: 75, // when the browser opens the page and capture starts (>= workloadAt + 30, so
    //               the simulator's 60 s service refresh has restored dst names)
  },
  gif: { width: 1200, fps: 15, quality: 90, crossfade: 0.6 },
  mp4: { width: 1920, fps: 30, crf: 18 },
  captions: true,
  // Where the cursor rests on the first/last frame: empty canvas, so the
  // poster frame is unobstructed.
  cursorRest: { x: 1010, y: 610 },
}

/**
 * Churn injected through the ingest API during warm-up (see lib/scenario.mjs).
 * A new batch job shows up on demo-app-1 and starts talking to postgres and
 * kafka, giving time travel and "Compare to now" a real change to show.
 * Set to null to record the bare simulator.
 */
export const scenario = {
  node: 'demo-app-1',
  ip: '10.0.2.14',
  comm: 'inventory-sync',
  calls: [
    { ip: '10.0.3.10', port: 5432, sentKBps: 20, recvKBps: 140 }, // postgres
    { ip: '10.0.3.12', port: 9092, sentKBps: 35, recvKBps: 4 }, // kafka
  ],
}

export const sel = {
  node: id => `.react-flow__node[data-id="${id}"]`,
  drawerClose: 'div.fixed.top-0.right-0 .h-16 button',
  searchClear: 'button[title="Clear search"]',
  fitView: '.react-flow__controls-fitview',
  timeline: 'input[type=range] >> nth=0',
}

/** Point on a range input at fraction f (0..1) of its track. */
async function onSlider(page, selector, f) {
  const b = await page.locator(selector).first().boundingBox()
  const pad = 8 // thumb radius: the track's usable span is inset by it
  return { x: b.x + pad + (b.width - 2 * pad) * f, y: b.y + b.height / 2 }
}

/** Fraction of the recorded history range that `at` (epoch ms) falls on. */
async function rangeFraction(page, atMs) {
  const r = await page.evaluate(() => fetch('/api/v1/topology/history/range').then(x => x.json()))
  const e = Date.parse(r.earliest)
  const l = Date.parse(r.latest)
  return Math.min(0.95, Math.max(0.05, (atMs - e) / (l - e)))
}

export const scenes = [
  {
    // First frame doubles as the poster GitHub shows before the GIF loads:
    // the whole live map, with the one-line pitch.
    name: 'overview',
    // setup() of the first scene runs before capture starts, so frame 1
    // already has its caption fully faded in.
    async setup({ d }) {
      await d.caption('<b>Live service map</b> from eBPF &mdash; zero code changes')
      await d.wait(500)
    },
    async run({ d }) {
      await d.wait(2300)
    },
  },
  {
    // api-gateway, not a datastore: its drawer shows non-zero upload *and*
    // download (postgres reads "Upload 0 B/s" - see the phase-1 report).
    name: 'drawer',
    async run({ d, sel }) {
      await d.click(sel.node('10.0.2.10/api-gateway'), { pauseAfter: 250 })
      await d.caption('Click any service: <b>live throughput</b>, ports, peers', 'drawer')
      await d.wait(2100)
      await d.click(sel.drawerClose, { pauseAfter: 300 })
    },
  },
  {
    name: 'search',
    async run({ d, sel }) {
      await d.caption('Press <b>/</b> to search by name, IP, tech or node')
      await d.move({ x: 470, y: 150 })
      await d.press('/', { pauseAfter: 200 })
      await d.type('python')
      await d.wait(1700)
      await d.click(sel.searchClear, { pauseAfter: 150 })
      await d.click(sel.fitView, { pauseAfter: 350 })
    },
  },
  {
    name: 'time travel',
    async run({ d, page, sel, world }) {
      await d.click('button:has-text("History")', { pauseAfter: 250 })
      // Top: the open timeline covers the bottom-centre caption spot.
      await d.caption('<b>Time travel</b>: the map at any past moment', 'top')
      const f = await rangeFraction(page, world.beforeWorkloadMs)
      await d.drag(await onSlider(page, sel.timeline, 1), await onSlider(page, sel.timeline, f), { ms: 1000 })
      await d.wait(1900)
      await d.click('button:has-text("Go live")', { pauseAfter: 850 })
      await d.click('.absolute.bottom-6 button:has(svg.lucide-x)', { pauseAfter: 200 })
    },
  },
  {
    name: 'diff',
    async run({ d, page, world }) {
      await d.click('button:has-text("Compare to now")', { pauseAfter: 200 })
      await d.caption('<b>Diff</b>: what appeared or vanished since then')
      const slider = 'div.w-80 input[type=range]'
      const f = await rangeFraction(page, world.beforeWorkloadMs)
      await d.drag(await onSlider(page, slider, 0), await onSlider(page, slider, f), { ms: 900 })
      await d.wait(2000)
      await d.click('div.w-80 button:has(svg.lucide-x)', { pauseAfter: 200 })
    },
  },
  {
    // Blast radius is told through the ranking: the drawer's own "What
    // breaks if this goes down" highlight renders underneath the drawer's
    // blurred backdrop in v3.0 and cannot be seen (see the phase-1 report).
    name: 'blast radius ranking',
    async run({ d }) {
      await d.click('button:has-text("Most critical")', { pauseAfter: 200 })
      await d.caption('<b>Blast radius</b>: what breaks if each service goes down')
      await d.wait(2200)
      await d.click('div.w-80 button:has(svg.lucide-x)', { pauseAfter: 250 })
    },
  },
  {
    // Back to the opening state so the loop is seamless (the encoder also
    // crossfades the last moments into the first frame).
    name: 'loop back',
    async run({ d }) {
      await d.caption(null)
      // Clicking empty canvas clears the api-gateway selection highlight
      // left over from the drawer scene, restoring the opening frame.
      await d.click(settings.cursorRest, { ms: 700, pauseBefore: 100, pauseAfter: 500 })
    },
  },
]
