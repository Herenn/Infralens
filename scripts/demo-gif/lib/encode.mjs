// Turns the raw screencast (irregularly timed full-resolution frames) into:
//   master.mkv  - lossless, constant fps, with the loop crossfade applied
//   demo.mp4    - H.264 for the releases page / social
//   demo.gif    - gifski-encoded README GIF
//
// Every derived output is cut from the lossless master, never from another
// lossy file, so the GIF is downscaled straight from the 2x capture.

import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'

const log = (...a) => console.log('[encode]', ...a)

function run(cmd, args) {
  execFileSync(cmd, args, { stdio: ['ignore', 'ignore', 'inherit'] })
}

function probeDuration(file) {
  return Number(execFileSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', file]).toString().trim())
}

export function encodeAll({ rawDir, outDir, gif, mp4 }) {
  const { frames, wallSeconds } = JSON.parse(fs.readFileSync(path.join(rawDir, 'manifest.json'), 'utf8'))
  if (frames.length < 2) throw new Error('capture produced fewer than 2 frames')
  fs.mkdirSync(outDir, { recursive: true })

  // 1. Concat list with each frame's real on-screen duration. The screencast
  //    only emits a frame when something changed, so a long gap is a still.
  const lines = []
  for (let i = 0; i < frames.length; i++) {
    const next = i + 1 < frames.length ? frames[i + 1].t : Math.max(frames[i].t + 0.1, wallSeconds)
    lines.push(`file '${frames[i].file}'`, `duration ${Math.max(0.001, next - frames[i].t).toFixed(4)}`)
  }
  lines.push(`file '${frames[frames.length - 1].file}'`) // concat demuxer quirk: last entry repeated
  const list = path.join(rawDir, 'frames.txt')
  fs.writeFileSync(list, lines.join('\n') + '\n')
  const gaps = frames.slice(1).map((f, i) => f.t - frames[i].t)
  log(`${frames.length} frames over ${wallSeconds.toFixed(1)} s (avg ${(frames.length / wallSeconds).toFixed(1)} fps, ` +
    `longest gap ${(Math.max(...gaps) * 1000).toFixed(0)} ms)`)

  // 2. Lossless constant-fps master, crossfading the last moments back into
  //    the first frame so the loop has no visible seam.
  const fps = mp4.fps
  const timed = path.join(rawDir, 'timed.mkv')
  run('ffmpeg', ['-y', '-v', 'error', '-f', 'concat', '-safe', '0', '-i', list,
    '-vf', `fps=${fps},format=gbrp`, '-c:v', 'ffv1', timed])
  const dur = probeDuration(timed)
  const master = path.join(rawDir, 'master.mkv')
  const xf = gif.crossfade
  if (xf > 0) {
    run('ffmpeg', ['-y', '-v', 'error', '-i', timed, '-loop', '1', '-t', String(xf + 0.5), '-i', frames[0].file,
      '-filter_complex',
      `[0:v]settb=AVTB,fps=${fps},format=gbrp[a];[1:v]settb=AVTB,fps=${fps},format=gbrp[b];` +
      `[a][b]xfade=transition=fade:duration=${xf}:offset=${(dur - xf).toFixed(3)},trim=duration=${dur.toFixed(3)}[v]`,
      '-map', '[v]', '-c:v', 'ffv1', master])
  } else {
    fs.renameSync(timed, master)
  }
  log(`master: ${dur.toFixed(2)} s @ ${fps} fps`)

  // 3. MP4.
  const mp4Path = path.join(outDir, 'demo.mp4')
  run('ffmpeg', ['-y', '-v', 'error', '-i', master,
    '-vf', `scale=${mp4.width}:-2:flags=lanczos:out_color_matrix=bt709,format=yuv420p`,
    '-c:v', 'libx264', '-preset', 'slow', '-crf', String(mp4.crf), '-tune', 'animation',
    '-colorspace', 'bt709', '-color_primaries', 'bt709', '-color_trc', 'bt709',
    '-movflags', '+faststart', '-an', mp4Path])

  // 4. GIF: evenly timed PNGs at the target size straight from the master,
  //    then gifski (per-frame palettes + temporal dithering control - far
  //    cleaner on a dark UI than ffmpeg's palettegen/paletteuse).
  const gifFrames = path.join(rawDir, 'gif-frames')
  fs.rmSync(gifFrames, { recursive: true, force: true })
  fs.mkdirSync(gifFrames)
  run('ffmpeg', ['-y', '-v', 'error', '-i', master,
    '-vf', `fps=${gif.fps},scale=${gif.width}:-1:flags=lanczos+accurate_rnd+full_chroma_int`,
    path.join(gifFrames, 'g%05d.png')])
  const pngs = fs.readdirSync(gifFrames).filter(f => f.endsWith('.png')).sort().map(f => path.join(gifFrames, f))
  const gifPath = path.join(outDir, 'demo.gif')
  // --width is mandatory: without it gifski silently caps the size (to 600 px
  // here), throwing away the whole point of the 2x capture.
  const gifskiArgs = ['--fps', String(gif.fps), '--quality', String(gif.quality), '--width', String(gif.width),
    '--repeat', '0', '-o', gifPath]
  if (gif.motionQuality) gifskiArgs.push('--motion-quality', String(gif.motionQuality))
  if (gif.lossyQuality) gifskiArgs.push('--lossy-quality', String(gif.lossyQuality))
  if (gif.extra) gifskiArgs.push('--extra')
  run('gifski', [...gifskiArgs, ...pngs])

  const mb = f => (fs.statSync(f).size / 1024 / 1024).toFixed(2) + ' MB'
  log(`gif: ${gifPath} (${mb(gifPath)}, ${pngs.length} frames @ ${gif.fps} fps, ${gif.width} px wide)`)
  log(`mp4: ${mp4Path} (${mb(mp4Path)})`)
  return { gifPath, mp4Path, master, durationSeconds: dur, gifFrames: pngs.length }
}
