# README demo recorder

Scripted, reproducible recording of `docs/demo.gif` (plus `docs/demo.mp4` and
the `docs/demo-hero.png` still). One command builds and starts InfraLens in
demo mode on private ports, drives a real Chrome through the storyboard with a
visible cursor, captures it at 2x resolution, encodes it, and stops
everything it started.

## Re-record

```sh
cd scripts/demo-gif
npm ci                        # first time only
npm run record -- --install   # ~3 min; writes out/ and copies the results into docs/
```

Without `--install` the results stay in `scripts/demo-gif/out/` (`demo.gif`,
`demo.mp4`, `demo-hero.png`) for review first. `npm run record -- --help`
lists every option. The ones you'll use most:

| Option | Default | |
|---|---|---|
| `--backend-port` / `--frontend-port` | 18931 / 18932 | also `DEMO_BACKEND_PORT` / `DEMO_FRONTEND_PORT` |
| `--gif-width`, `--gif-fps`, `--gif-quality` | 1200, 15, 90 | size/quality trade-off, see below |
| `--no-captions` | | record without the caption pills |
| `--no-scenario` | | don't inject the extra workload (bare simulator) |
| `--encode-only` | | re-encode the last capture in `out/raw` with new GIF settings, no browser |
| `--keep-running` | | leave the stack up after recording, to poke at it |
| `--headed` | | watch the browser while it records |

## Prerequisites

- Go (builds `backend/` into `.cache/`), Node 20+ (the frontend's own
  `node_modules` are installed with `npm ci` if missing)
- Google Chrome installed (Playwright drives it via `channel: 'chrome'`; no
  browser download)
- `ffmpeg` and `gifski` on `PATH` (`brew install ffmpeg gifski`)

## Editing the storyboard

Everything about *what* is shown lives in [`storyboard.mjs`](storyboard.mjs):

- `settings` - viewport, warm-up timing, GIF/MP4 encode settings
- `scenario` - the workload injected during warm-up (see below)
- `scenes` - an ordered list of `{ name, run(ctx) }`; each uses the
  `Director` helpers (`d.click(selector)`, `d.move`, `d.drag`, `d.type`,
  `d.press`, `d.wait`, `d.caption(html, position)`)

Keep ~2 s of stillness after each reveal, and make the last scene restore the
first frame's UI state - the GIF loops, and the encoder crossfades the final
0.6 s into frame 1. The first scene's `setup()` runs before capture starts, so
frame 1 (which GitHub shows before the GIF loads) is already fully composed.

## How it works

| File | |
|---|---|
| `record.mjs` | CLI: orchestrates stack, warm-up, browser, capture, encode, cleanup |
| `lib/stack.mjs` | backend (`DEMO_MODE=true`, `ALLOW_NO_AUTH=true`, fresh SQLite in `.cache/`, `TZ=UTC`) and Vite (production build + preview, proxy pointed at the backend port) - configured only through env and Vite's inline config, no app files touched |
| `lib/scenario.mjs` | reports a new workload through the public ingest API (`/api/v1/events`, `/api/v1/stats`), like an agent would |
| `lib/director.mjs` | cursor overlay, eased mouse movement, click ripples, typing cadence, captions |
| `lib/capture.mjs` | Chrome DevTools screencast: full device-pixel frames with timestamps (~55 fps), written asynchronously |
| `lib/encode.mjs` | timestamps -> constant-fps lossless master (+ loop crossfade) -> H.264 MP4 and gifski GIF |

Why the scenario: the v3.0 simulator's topology never changes shape, so time
travel and "Compare to now" would have nothing to show. 40 s into warm-up an
`inventory-sync` job appears on `demo-app-1` and starts talking to postgres
and kafka; the time-travel and diff scenes scrub back to before that.

Why `TZ=UTC`: v3.0's SQLite history store compares timestamps as text in the
process's local zone, so on a non-UTC machine time travel renders an empty
graph and the diff always reports 0 changes. The Docker images run in UTC,
which is why the shipped demo is unaffected.

## Size / quality trade-off (1200 px, ~32 s)

| gifski quality | size | |
|---|---|---|
| 90 (default) | ~5.7 MB | clean gradients, no visible banding |
| 80 | ~3.8 MB | slight banding behind the drawer blur |
| 70 | ~2.6 MB | visible posterisation on the dark UI |

Going to 1000 px wide at quality 90 gives ~4.8 MB but makes the small text in
the overview noticeably softer on retina screens.
