// Human-paced interaction helpers for the storyboard: a visible cursor
// (headless Chrome draws none), eased mouse movement, click ripples, typing
// with a natural cadence, and an optional caption pill.
//
// The overlay is injected into the page purely for the recording. It is
// pointer-events:none, so it never intercepts the real input events that
// Playwright dispatches to the app.

const OVERLAY_SCRIPT = () => {
  const install = () => {
    if (document.getElementById('__demo_cursor')) return
    const style = document.createElement('style')
    style.textContent = `
      #__demo_cursor { position: fixed; left: 0; top: 0; width: 26px; height: 26px; z-index: 2147483647;
        pointer-events: none; transform: translate(-100px, -100px); will-change: transform;
        filter: drop-shadow(0 2px 4px rgba(0,0,0,.55)); }
      .__demo_ripple { position: fixed; z-index: 2147483646; pointer-events: none; width: 44px; height: 44px;
        margin: -22px 0 0 -22px; border-radius: 50%; border: 2px solid rgba(74, 222, 128, .95);
        background: rgba(74, 222, 128, .18); animation: __demo_ripple .55s ease-out forwards; }
      @keyframes __demo_ripple { from { transform: scale(.35); opacity: 1 } to { transform: scale(1.25); opacity: 0 } }
      #__demo_caption { position: fixed; left: 50%; bottom: 92px; z-index: 2147483645; pointer-events: none;
        transform: translate(-50%, 8px); opacity: 0; transition: opacity .35s ease, transform .35s ease;
        padding: 10px 20px; border-radius: 999px; white-space: nowrap;
        font: 600 19px/1.2 Inter, system-ui, -apple-system, 'Segoe UI', sans-serif; letter-spacing: .1px;
        color: #f8fafc; background: rgba(15, 23, 42, .92); border: 1px solid rgba(74, 222, 128, .45);
        box-shadow: 0 10px 30px rgba(0,0,0,.45); }
      #__demo_caption.show { opacity: 1; transform: translate(-50%, 0); }
      #__demo_caption.top { bottom: auto; top: 66px; }
      #__demo_caption.drawer { left: calc((100% - 420px) / 2); }
      #__demo_caption b { color: #4ade80; font-weight: 700; }
    `
    document.documentElement.appendChild(style)
    const cursor = document.createElement('div')
    cursor.id = '__demo_cursor'
    // Classic arrow pointer: white fill, dark outline - readable on any UI.
    cursor.innerHTML = `<svg width="26" height="26" viewBox="0 0 26 26" xmlns="http://www.w3.org/2000/svg">
      <path d="M3 2 L3 21 L8.2 16.4 L11.6 24 L15 22.5 L11.7 15 L18.5 15 Z" fill="#fff" stroke="#0f172a"
        stroke-width="1.6" stroke-linejoin="round"/></svg>`
    document.documentElement.appendChild(cursor)
    const caption = document.createElement('div')
    caption.id = '__demo_caption'
    document.documentElement.appendChild(caption)

    window.addEventListener('mousemove', e => {
      cursor.style.transform = `translate(${e.clientX - 3}px, ${e.clientY - 2}px)`
    }, true)
    window.addEventListener('mousedown', e => {
      const r = document.createElement('div')
      r.className = '__demo_ripple'
      r.style.left = e.clientX + 'px'
      r.style.top = e.clientY + 'px'
      document.documentElement.appendChild(r)
      setTimeout(() => r.remove(), 700)
    }, true)
    // position: 'bottom' (default), 'top' (when the timeline occupies the
    // bottom), 'drawer' (bottom, centred on the canvas left of the drawer).
    window.__demoCaption = (html, position) => {
      if (html) {
        caption.innerHTML = html
        caption.className = `show ${position || ''}`
      } else caption.classList.remove('show')
    }
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', install)
  else install()
}

const easeInOut = t => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2)

export class Director {
  constructor(page, { captions = true } = {}) {
    this.page = page
    this.captions = captions
    this.pos = { x: 640, y: 400 }
  }

  async install() {
    await this.page.addInitScript(OVERLAY_SCRIPT)
    await this.page.evaluate(OVERLAY_SCRIPT)
  }

  wait(ms) { return this.page.waitForTimeout(ms) }

  /** Resolves a target to a point: {x,y}, a selector string, or a Locator. */
  async point(target, { dx = 0, dy = 0, anchor = 'center' } = {}) {
    if (target && typeof target.x === 'number') return { x: target.x + dx, y: target.y + dy }
    const loc = typeof target === 'string' ? this.page.locator(target).first() : target
    await loc.waitFor({ state: 'visible', timeout: 10000 })
    const b = await loc.boundingBox()
    if (!b) throw new Error(`no bounding box for ${target}`)
    const ax = anchor.includes('left') ? 0.25 : anchor.includes('right') ? 0.75 : 0.5
    return { x: b.x + b.width * ax + dx, y: b.y + b.height / 2 + dy }
  }

  /** Glides the cursor to target. Duration scales with distance unless given. */
  async move(target, { ms, ...opts } = {}) {
    const to = await this.point(target, opts)
    const from = this.pos
    const dist = Math.hypot(to.x - from.x, to.y - from.y)
    const duration = ms ?? Math.min(900, Math.max(320, 220 + dist * 0.7))
    // A slight arc reads as a hand moving, not a robot sliding.
    const nx = -(to.y - from.y) / (dist || 1)
    const ny = (to.x - from.x) / (dist || 1)
    const bow = Math.min(40, dist * 0.08)
    const start = Date.now()
    for (;;) {
      const t = Math.min(1, (Date.now() - start) / duration)
      const e = easeInOut(t)
      const arc = Math.sin(Math.PI * e) * bow
      await this.page.mouse.move(from.x + (to.x - from.x) * e + nx * arc, from.y + (to.y - from.y) * e + ny * arc)
      if (t >= 1) break
      await this.page.waitForTimeout(16)
    }
    this.pos = to
    return to
  }

  async click(target, { pauseBefore = 150, pauseAfter = 450, ...opts } = {}) {
    await this.move(target, opts)
    await this.wait(pauseBefore)
    await this.page.mouse.down()
    await this.wait(90)
    await this.page.mouse.up()
    await this.wait(pauseAfter)
  }

  /** Drags from one point to another with eased motion (sliders, panning). */
  async drag(from, to, { ms = 1200, holdBefore = 200, holdAfter = 300 } = {}) {
    const a = await this.move(from)
    await this.wait(holdBefore)
    await this.page.mouse.down()
    const b = await this.point(to)
    const start = Date.now()
    for (;;) {
      const t = Math.min(1, (Date.now() - start) / ms)
      const e = easeInOut(t)
      await this.page.mouse.move(a.x + (b.x - a.x) * e, a.y + (b.y - a.y) * e)
      if (t >= 1) break
      await this.page.waitForTimeout(16)
    }
    this.pos = b
    await this.wait(holdAfter)
    await this.page.mouse.up()
  }

  async press(key, { pauseAfter = 300 } = {}) {
    await this.page.keyboard.press(key)
    await this.wait(pauseAfter)
  }

  async type(text, { delay = 115 } = {}) {
    // Uneven but deterministic cadence, so re-records time out the same.
    let i = 0
    for (const ch of text) {
      await this.page.keyboard.type(ch)
      await this.wait(delay + ((i++ * 37) % 50) - 25)
    }
  }

  async caption(html, position) {
    if (!this.captions) return
    await this.page.evaluate(([h, p]) => window.__demoCaption?.(h, p), [html ?? null, position ?? null])
  }
}
