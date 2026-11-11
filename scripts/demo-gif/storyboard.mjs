// The storyboard: WHAT the demo shows, in order. Edit this file to re-shoot.
//
// Each scene is { name, run(ctx) }. ctx gives you:
//   d      - Director (lib/director.mjs): d.move / d.click / d.drag / d.type /
//            d.press / d.hover / d.wait / d.caption(html | null, position)
//   page   - the Playwright page
//   sel    - selector helpers below
//   world  - { applyAt, arrivalAt, backendUrl }: when the live change below
//            is applied and expected in the Changes feed (epoch ms)
//
// Timing guideline: ~2 s of stillness after each reveal so it registers,
// cursor moves of 0.5-1 s, and the whole loop at 28-32 s. The GIF loops, so
// the last scene must leave the UI as the first frame shows it.

/** Recording/encoding settings. CLI flags override some of these (see README). */
export const settings = {
  viewport: { width: 1280, height: 800 }, // CSS px; captured at deviceScaleFactor 2
  deviceScaleFactor: 2,
  locale: 'en-US',
  timezoneId: 'UTC',
  // How long the page is open before capture starts (layout, fitView and a
  // few live updates settle; nothing of this is recorded).
  pageSettleSeconds: 12,
  // Measured on the final ~29.6 s take (gifski 1.34; target <= ~4.5 MB):
  //   1200 px 15 fps q85  6.5 MB    1200 px 12 fps q80  4.9 MB (ghosting)
  //   1100 px 12 fps q85  5.0 MB    1000 px 15 fps q85  4.8 MB
  //   1000 px 12 fps q90  4.9 MB    1000 px 12 fps q85  4.3 MB  <- chosen
  // Below q85 gifski's lossy compression leaves visible ghosts of earlier
  // frames on the dark background; width is the cleaner thing to give up.
  gif: { width: 1000, fps: 12, quality: 85, crossfade: 0.4 },
  mp4: { width: 1920, fps: 30, crf: 18 },
  captions: true,
  // Record with the browser's prefers-reduced-motion setting on, which the
  // UI honours by hiding the travelling "flow dots" on live edges (and
  // snapping, not animating, node re-layouts). At README size the dots are
  // sub-pixel shimmer, but they change every pixel along every edge on every
  // frame and cost well over half of the GIF's bytes. --flow-dots turns them
  // back on.
  reducedMotion: true,
  // Where the cursor rests on the first/last frame: empty canvas above the
  // map, so the poster frame is unobstructed.
  cursorRest: { x: 700, y: 150 },
}

/**
 * The live change the Changes scene shows arriving. Demo mode's simulator
 * applies "api-gateway starts posting alerts to Slack" 3 m 30 s after start
 * (backend/service/demo/story.go); the drift detector reports it on its next
 * 15 s pass. The recorder predicts that moment from the detector's phase and
 * starts capture `leadSeconds` before it, so the row slides into an already
 * open feed.
 */
export const liveArrival = {
  scriptAt: 210,
  detectorInterval: 15,
  // Must exceed the overview + minimum drawer scene (~8 s); the drawer's
  // final hold absorbs the rest (see the 'drawer' scene).
  leadSeconds: 10,
  match: ev => ev.kind === 'appeared' && /hooks\.slack\.com/.test(`${ev.target_name || ''} ${ev.target_id}`),
  row: '[data-panel=changes] li:has-text("hooks.slack.com")',
}

export const sel = {
  node: id => `.react-flow__node[data-id="${id}"]`,
  rail: id => `[data-rail=${id}]`,
  panel: id => `[data-panel=${id}]`,
  panelClose: id => `[data-panel=${id}] [data-testid=panel-close]`,
  drawer: '[data-testid=drawer]',
  drawerClose: '[data-testid=drawer-close]',
  criticalRow: name => `[data-panel=critical] li button:has-text("${name}")`,
  impactPill: '[data-testid=impact-pill]',
  impactClear: '[data-testid=impact-clear]',
  historyBanner: '[data-testid=history-banner]',
  timelineClose: '[data-testid=timeline-close]',
  fitView: '.react-flow__controls-fitview',
}

export const scenes = [
  {
    // First frame doubles as the poster GitHub shows before the GIF loads:
    // the whole calm live map, with the one-line pitch.
    name: 'overview',
    // setup() of the first scene runs before capture starts, so frame 1
    // already has its caption fully faded in.
    async setup({ d }) {
      await d.caption('<b>Live service map</b> from eBPF &mdash; zero code changes')
      await d.wait(500)
    },
    async run({ d }) {
      await d.wait(2200)
    },
  },
  {
    // orders-service talks to datastores *and* the internet (api.sendgrid.com).
    name: 'drawer',
    async run({ d, sel, world }) {
      await d.click(sel.node('10.0.2.12/orders-service'), { pauseAfter: 200 })
      await d.caption('Every peer by name &mdash; <b>inside or on the internet</b>', 'drawer')
      await d.wait(800)
      await d.move({ x: 1080, y: 560 })
      await d.scroll(520, { ms: 650 })
      // Holds here are what absorb timing slack: the Changes feed must be
      // open ~2 s before the live arrival (world.arrivalAt).
      await d.waitUntil(world.arrivalAt - 4000, { min: 600 })
      await d.click(sel.drawerClose, { pauseAfter: 200 })
    },
  },
  {
    // The arrival is timed (see liveArrival) to land a moment after the
    // feed opens.
    name: 'changes: live arrival',
    async run({ d, page, sel }) {
      await d.click(sel.rail('changes'), { pauseAfter: 150 })
      await page.locator(sel.panel('changes')).waitFor()
      await d.caption('<b>Changes</b>: new dependencies the moment they appear', 'right')
      await d.move({ x: 300, y: 300 }, { ms: 600 })
      await page.locator(liveArrival.row).first().waitFor({ timeout: 25000 })
      await d.wait(1100)
      await d.click(liveArrival.row, { pauseAfter: 200 })
      await d.caption('Click one to <b>find it on the map</b>', 'right')
      await d.wait(1300)
    },
  },
  {
    name: 'changes: time travel',
    async run({ d, page, sel }) {
      await d.click(`${liveArrival.row} button:has-text("View when it appeared")`, { pauseAfter: 150 })
      await page.locator(sel.historyBanner).waitFor()
      await d.caption('<b>Time travel</b> to the moment it appeared', 'right')
      await d.wait(1500)
      await d.click(`${liveArrival.row} button:has-text("Back to live")`, { pauseAfter: 400 })
      // No close: the next rail click swaps the docked panel in place, so the
      // map keeps its framing (every open/close re-fits it - costly in a GIF).
    },
  },
  {
    // The money shot: give it time.
    name: 'blast radius',
    async run({ d, page, sel }) {
      await d.click(sel.rail('critical'), { pauseAfter: 200 })
      await page.locator(sel.criticalRow('postgres')).first().waitFor()
      await d.caption('Ranked by <b>blast radius</b>', 'right')
      await d.wait(400)
      await d.click(sel.criticalRow('postgres'), { pauseAfter: 200 })
      await d.caption('One click: <b>everything that breaks</b> lights up', 'right')
      await d.wait(2700)
      await d.caption(null) // don't leave the caption over an un-lit map
      await d.click(sel.impactClear, { pauseAfter: 250 })
    },
  },
  {
    name: 'idle spend',
    async run({ d, page, sel }) {
      await d.click(sel.rail('idle'), { pauseAfter: 200 })
      await page.locator(sel.panel('idle')).waitFor()
      await d.caption('<b>Idle spend</b>: what you pay for services nobody calls', 'right')
      await d.wait(2200)
      await d.click(sel.panelClose('idle'), { pauseAfter: 600 })
    },
  },
  {
    // Back to the opening state so the loop is seamless (the encoder also
    // crossfades the last moments into the first frame).
    name: 'loop back',
    async run({ d }) {
      await d.caption(null)
      await d.move(settings.cursorRest, { ms: 700 })
      await d.wait(500)
    },
  },
]
