import { pointAt } from './edgeRoute'

// Edge labels are placed, not just drawn at the midpoint: each tries a few
// spots along its edge and takes the first that covers no card, no group
// title and no label placed before it. One that fits nowhere is not shown
// (its edge is still highlighted, and hovering it shows it).

export interface Rect { x: number; y: number; w: number; h: number }

const CELL = 160
// Spots along the edge, best first: the middle, then outwards.
const FRACTIONS = [0.5, 0.42, 0.58, 0.34, 0.66, 0.26, 0.74, 0.18, 0.82]

class Grid {
  private cells = new Map<string, Rect[]>()

  add(r: Rect) {
    for (const key of this.keys(r)) {
      let list = this.cells.get(key)
      if (!list) { list = []; this.cells.set(key, list) }
      list.push(r)
    }
  }

  hits(r: Rect): boolean {
    for (const key of this.keys(r)) {
      for (const o of this.cells.get(key) || []) {
        if (r.x < o.x + o.w && r.x + r.w > o.x && r.y < o.y + o.h && r.y + r.h > o.y) return true
      }
    }
    return false
  }

  private *keys(r: Rect) {
    const x0 = Math.floor(r.x / CELL), x1 = Math.floor((r.x + r.w) / CELL)
    const y0 = Math.floor(r.y / CELL), y1 = Math.floor((r.y + r.h) / CELL)
    for (let x = x0; x <= x1; x++) for (let y = y0; y <= y1; y++) yield `${x},${y}`
  }
}

export interface LabelRequest {
  id: string
  samples: [number, number][]
  width: number
  height: number
}

/** Centre points for the labels that fit, keyed by request ID. */
export function placeLabels(requests: LabelRequest[], obstacles: Rect[]): Map<string, { x: number; y: number }> {
  const grid = new Grid()
  for (const o of obstacles) grid.add(o)
  const placed = new Map<string, { x: number; y: number }>()
  for (const req of requests) {
    for (const f of FRACTIONS) {
      const [x, y] = pointAt(req.samples, f)
      const rect = { x: x - req.width / 2 - 3, y: y - req.height / 2 - 3, w: req.width + 6, h: req.height + 6 }
      if (grid.hits(rect)) continue
      grid.add(rect)
      placed.set(req.id, { x, y })
      break
    }
  }
  return placed
}

/** Rough width of one label line in the edge-label font (10.5px mono). */
export function labelWidth(line: string): number {
  return Math.ceil(line.length * 6.35 + 14)
}
