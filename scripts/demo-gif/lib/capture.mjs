// Frame capture via the Chrome DevTools screencast.
//
// Why not Playwright's recordVideo: it encodes a lossy VP8 stream at CSS-pixel
// size, which smears small text on a dark UI before gifski ever sees it.
// Why not a page.screenshot() loop: each 2560x1440 screenshot blocks the page
// for tens of milliseconds, so frame spacing is uneven and animations stutter.
//
// The screencast pushes a frame every time the compositor produces one, at the
// full device-pixel resolution, each with a timestamp. Frames are written to
// disk as they arrive (async, never blocking the page); encode.mjs later turns
// the irregular timestamps into an evenly timed, constant-fps sequence.

import fs from 'node:fs'
import path from 'node:path'

export async function startCapture(page, dir, { width, height, format = 'png', quality = 95 }) {
  fs.rmSync(dir, { recursive: true, force: true })
  fs.mkdirSync(dir, { recursive: true })
  const cdp = await page.context().newCDPSession(page)
  const frames = []
  const pending = new Set()
  let n = 0
  let t0 = null

  cdp.on('Page.screencastFrame', ({ data, metadata, sessionId }) => {
    // Ack first so Chrome keeps the frames coming while we write this one.
    cdp.send('Page.screencastFrameAck', { sessionId }).catch(() => {})
    const ts = metadata.timestamp // seconds, monotonic-ish wall clock
    if (t0 === null) t0 = ts
    const file = path.join(dir, `f${String(n++).padStart(5, '0')}.${format === 'jpeg' ? 'jpg' : 'png'}`)
    frames.push({ file, t: ts - t0 })
    const p = fs.promises.writeFile(file, Buffer.from(data, 'base64')).finally(() => pending.delete(p))
    pending.add(p)
  })

  await cdp.send('Page.startScreencast', {
    format,
    ...(format === 'jpeg' ? { quality } : {}),
    maxWidth: width,
    maxHeight: height,
    everyNthFrame: 1,
  })
  const startedAt = Date.now()

  return {
    /** Stops capture; resolves with [{file, t}] (t in seconds from first frame) and wall duration. */
    async stop() {
      const wall = (Date.now() - startedAt) / 1000
      await cdp.send('Page.stopScreencast').catch(() => {})
      await new Promise(r => setTimeout(r, 300))
      await Promise.all(pending)
      await cdp.detach().catch(() => {})
      const manifest = { frames, wallSeconds: wall }
      fs.writeFileSync(path.join(dir, 'manifest.json'), JSON.stringify(manifest, null, 1))
      return manifest
    },
  }
}
