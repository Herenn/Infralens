import { CARD_W } from './layout'

// How an edge is drawn between two fixed-size cards (see utils/layout.ts),
// shared by the edge component and the label placer so a label always sits
// on its line.
//
// - Forward (target column to the right): from the source's right side to the
//   target's left side, through the gaps of any column in between.
// - Backward (target to the left, e.g. a scraper calling into an earlier
//   tier): from the source's left side to the target's right side.
// - Same column: a bracket on the right-hand side, out of the source and
//   into the target's right side.

type Point = [number, number]

export interface EdgeRoute {
  path: string
  // A polyline approximation, for placing labels along the edge.
  samples: Point[]
}

const SAMPLES_PER_CURVE = 10

function cubic(p0: Point, c1: Point, c2: Point, p1: Point, out: Point[]) {
  for (let i = 1; i <= SAMPLES_PER_CURVE; i++) {
    const t = i / SAMPLES_PER_CURVE
    const u = 1 - t
    out.push([
      u * u * u * p0[0] + 3 * u * u * t * c1[0] + 3 * u * t * t * c2[0] + t * t * t * p1[0],
      u * u * u * p0[1] + 3 * u * u * t * c1[1] + 3 * u * t * t * c2[1] + t * t * t * p1[1],
    ])
  }
}

/**
 * sourceX/Y is the source card's right-hand handle, targetX/Y the target's
 * left-hand handle, as React Flow reports them.
 */
export function routeEdge(
  sourceX: number, sourceY: number, targetX: number, targetY: number,
  waypoints?: Point[] | null,
): EdgeRoute {
  const forward = targetX > sourceX + 12
  const backward = !forward && targetX + CARD_W < sourceX - CARD_W - 12

  if (!forward && !backward) {
    // Same column: a bracket on the right.
    const start: Point = [sourceX, sourceY]
    const end: Point = [targetX + CARD_W, targetY]
    const bulge = 34 + Math.min(70, Math.abs(targetY - sourceY) * 0.12)
    const right = Math.max(start[0], end[0]) + bulge
    const c1: Point = [right, start[1]]
    const c2: Point = [right, end[1]]
    const samples: Point[] = [start]
    cubic(start, c1, c2, end, samples)
    return { path: `M${start[0]},${start[1]} C${c1[0]},${c1[1]} ${c2[0]},${c2[1]} ${end[0]},${end[1]}`, samples }
  }

  const start: Point = forward ? [sourceX, sourceY] : [sourceX - CARD_W, sourceY]
  const end: Point = forward ? [targetX, targetY] : [targetX + CARD_W, targetY]
  const dir = forward ? 1 : -1

  // Waypoints come from the layout; after a card was dragged they may no
  // longer lie between the ends, and are then ignored.
  const inside = (p: Point) => dir * (p[0] - start[0]) > 0 && dir * (end[0] - p[0]) > 0
  const via = waypoints && waypoints.length > 0 && waypoints.every(inside) ? waypoints : []
  const points: Point[] = [start, ...via, end]

  let path = `M${start[0]},${start[1]}`
  const samples: Point[] = [start]
  for (let i = 1; i < points.length; i++) {
    const p0 = points[i - 1]
    const p1 = points[i]
    // Waypoints come in pairs (entering and leaving a crossed column): the
    // edge runs straight between the two.
    const throughColumn = i >= 2 && i < points.length - 1 && i % 2 === 0
    if (throughColumn) {
      path += ` L${p1[0]},${p1[1]}`
      samples.push(p1)
      continue
    }
    const dx = (p1[0] - p0[0]) / 2
    const c1: Point = [p0[0] + dx, p0[1]]
    const c2: Point = [p1[0] - dx, p1[1]]
    path += ` C${c1[0]},${c1[1]} ${c2[0]},${c2[1]} ${p1[0]},${p1[1]}`
    cubic(p0, c1, c2, p1, samples)
  }
  return { path, samples }
}

/** The point a fraction f (0..1) of the way along a sampled route. */
export function pointAt(samples: Point[], f: number): Point {
  let total = 0
  const lengths: number[] = [0]
  for (let i = 1; i < samples.length; i++) {
    total += Math.hypot(samples[i][0] - samples[i - 1][0], samples[i][1] - samples[i - 1][1])
    lengths.push(total)
  }
  const goal = total * f
  for (let i = 1; i < samples.length; i++) {
    if (lengths[i] >= goal) {
      const seg = lengths[i] - lengths[i - 1] || 1
      const t = (goal - lengths[i - 1]) / seg
      return [
        samples[i - 1][0] + (samples[i][0] - samples[i - 1][0]) * t,
        samples[i - 1][1] + (samples[i][1] - samples[i - 1][1]) * t,
      ]
    }
  }
  return samples[samples.length - 1]
}
