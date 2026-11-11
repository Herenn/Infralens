# README demo recorder

Scripted, reproducible recording of `docs/demo.gif`. One command builds and
starts InfraLens in demo mode on private ports, drives a real Chrome through
the storyboard with a visible cursor, captures it at 2x resolution, encodes
it, and stops everything it started. It also writes an MP4 of the same take
(for the GitHub release page / social) and a still of the opening frame;
those stay in `out/` and are not committed.

## Re-record

```sh
cd scripts/demo-gif
npm ci                        # first time only
npm run record -- --install   # ~5 min; writes out/ and copies demo.gif into docs/
```

Without `--install` everything stays in `scripts/demo-gif/out/`
(`demo.gif`, `demo.mp4`, `demo-hero.png`) for review first.
`npm run record -- --help` lists every option. The ones you'll use most:

| Option | Default | |
|---|---|---|
| `--backend-port` / `--frontend-port` | 18931 / 18932 | also `DEMO_BACKEND_PORT` / `DEMO_FRONTEND_PORT` |
| `--gif-width`, `--gif-fps`, `--gif-quality` | 1000, 12, 82 | size/quality trade-off, see below |
| `--no-captions` | | record without the caption pills |
| `--flow-dots` | | keep the live-edge flow dots (see *Reduced motion*) |
| `--encode-only` | | re-encode the last capture in `out/raw` with new GIF settings, no browser |
| `--serve` | | only start the demo stack and keep it up, to explore the UI while editing the storyboard |
| `--headed` | | watch the browser while it records |
| `--out DIR` | `out/` | a directory outside `scripts/demo-gif` is refused unless `--force-out` is given, because `<out>/raw` is deleted and rewritten on every run |

Why ~5 minutes: the recording is timed around a change demo mode makes on
its own 3 m 30 s after start (see *Timing* below).

## Prerequisites

- Go (builds `backend/` into `.cache/`), Node 20+
- `frontend/node_modules`: if missing, the recorder runs `npm ci` in
  `frontend/` for you (the frontend is built from source with its own
  locked dependencies)
- Google Chrome installed (Playwright drives it via `channel: 'chrome'`; no
  browser download)
- `ffmpeg` and `gifski` on `PATH` (`brew install ffmpeg gifski`)

## What gets recorded

Only what a user gets from `DEMO_MODE=true`: the recorder injects no data.
Demo mode seeds 14 days of history, a Changes feed and idle-spend prices,
then plays a short live script (new internet dependencies, a retired
service). The backend runs with `ALLOW_NO_AUTH=true` (no login screen) and a
fresh SQLite database in `.cache/demo.db`.

## Reduced motion

The browser records with `prefers-reduced-motion: reduce`, which the UI
honours by hiding the small "flow dots" that travel along live edges (and by
snapping instead of animating node re-layouts). At README size those dots are
sub-pixel shimmer, yet they change pixels along every edge on every frame and
cost well over half of the GIF's bytes. Pass `--flow-dots` to keep them.

## Editing the storyboard

Everything about *what* is shown lives in [`storyboard.mjs`](storyboard.mjs):

- `settings` - viewport, page settle time, GIF/MP4 encode settings, cursor rest point
- `liveArrival` - which live Changes event the feed scene waits for, and how
  long before it capture starts
- `sel` - selectors (the UI's `data-testid` / `data-rail` / `data-panel` hooks)
- `scenes` - an ordered list of `{ name, run(ctx) }`; each uses the
  `Director` helpers (`d.click(selector)`, `d.move`, `d.drag`, `d.scroll`,
  `d.type`, `d.press`, `d.wait`, `d.caption(html, position)`)

Keep ~2 s of stillness after each reveal, and make the last scene restore the
first frame's UI state exactly - the GIF loops with a hard cut back to
frame 1 (`settings.gif.crossfade` can blend instead, but a blend of two
different captions leaves a visible ghost in the GIF). The first scene's `setup()` runs before capture starts, so
frame 1 (which GitHub shows before the GIF loads) is already fully composed.

## Timing

Demo mode applies "api-gateway starts posting alerts to Slack" 210 s after
the backend starts, and the drift detector reports it on its next pass
(every 15 s in demo mode). The recorder reads the detector's phase from the
first live detection (SendGrid, ~40 s in), predicts when the Slack event
will land, and starts capture `liveArrival.leadSeconds` before it, so the
row slides into an already open feed. The feed scene then waits for the row
in the DOM rather than for a fixed time; the log prints the predicted and
actual arrival so a mistimed take is obvious.

## How it works

| File | |
|---|---|
| `record.mjs` | CLI: orchestrates stack, timing, browser, capture, encode, cleanup |
| `lib/stack.mjs` | backend and frontend lifecycle: own process groups, PID files in `.cache/pids/`, stale-process reaping |
| `lib/vite-server.mjs` | child process (cwd `frontend/`) that runs `vite build` + `vite preview` with an inline config merged over `frontend/vite.config.ts`: ports, proxy target, output dir |
| `lib/director.mjs` | cursor overlay, eased mouse movement and scrolling, click ripples, typing cadence, captions |
| `lib/capture.mjs` | Chrome DevTools screencast: full device-pixel frames with timestamps (~55 fps), written asynchronously |
| `lib/encode.mjs` | timestamps -> constant-fps lossless master (+ loop crossfade) -> H.264 MP4 and gifski GIF |

The frontend is built in a child process with `cwd` set to `frontend/`
because PostCSS/Tailwind resolve their config and `content` globs against the
process cwd; the recorder itself never changes directory.

Process hygiene: if the recorder is killed outright (e.g. `kill -9`) and
leaves its servers running, the next run finds `.cache/pids/*.json`, checks
that each PID's command line is still this script's backend binary or Vite
wrapper, and stops it before starting.

## Size / quality trade-off

See the comment above `settings.gif` in `storyboard.mjs` for the measured
options of the current take.
