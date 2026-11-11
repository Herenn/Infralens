import { Node, Edge } from '@xyflow/react'
import { Topology, Service, Connection as TopologyConnection } from '../types'
import { isAddressLike, serviceLabel } from './hostnames'

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------
//
// Every service card has the same fixed size, so edges and edge labels can be
// routed from positions alone (see utils/edgeRoute.ts) without waiting for
// the DOM to be measured.

export const CARD_W = 192
export const CARD_H = 56

const ROW_GAP = 20            // vertical space between cards in a column
const SUBCOL_GAP = 104        // between wrapped columns inside one group
const COLUMN_GAP = 116        // between flow columns: the gutters edges and labels use
const GROUP_PAD_X = 12
const GROUP_HEADER_H = 58     // the group's title bar (50px) and breathing room
const GROUP_PAD_BOTTOM = 14
const GROUP_STACK_GAP = 36    // between groups stacked in one flow column
const MIN_GROUP_W = CARD_W + GROUP_PAD_X * 2

// A group taller than this many cards wraps into several columns.
const MIN_ROWS_PER_COLUMN = 9

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

export type GroupKind = 'host' | 'clients' | 'external' | 'unmonitored'

export interface GroupInfo {
  id: string
  kind: GroupKind
  label: string
  // The node name host metrics are reported under (hosts only).
  metricsKey?: string
}

const CLIENTS_GROUP: GroupInfo = { id: 'group:clients', kind: 'clients', label: 'Inbound clients' }
const EXTERNAL_GROUP: GroupInfo = { id: 'group:external', kind: 'external', label: 'External services' }
const UNMONITORED_GROUP: GroupInfo = { id: 'group:unmonitored', kind: 'unmonitored', label: 'Unmonitored hosts' }

function hostGroup(node: string): GroupInfo {
  return { id: `server-${node}`, kind: 'host', label: node, metricsKey: node }
}

function isPrivateAddress(ip: string | undefined): boolean {
  if (!ip) return false
  if (/^10\./.test(ip) || /^192\.168\./.test(ip) || /^127\./.test(ip) || /^169\.254\./.test(ip)) return true
  const m = ip.match(/^172\.(\d+)\./)
  if (m && Number(m[1]) >= 16 && Number(m[1]) <= 31) return true
  if (/^(::1|f[cd][0-9a-f]{2}:|fe80:)/i.test(ip)) return true
  return false
}

function hasNode(svc: Service): boolean {
  return !!svc.node && svc.node !== 'Unknown' && svc.node !== 'External Network'
}

/**
 * Which group each service is drawn in. Computed from the graph, never from
 * names:
 *  - a service with a node belongs to that host;
 *  - a service without one whose IP is a monitored host's IP is drawn on that
 *    host (agents name a destination "IP:port" from the caller's side, while
 *    the host it runs on reports it under its process name);
 *  - otherwise it is outside: "Inbound clients" if it only calls in,
 *    "External services" if it is called (public address or a DNS name), and
 *    "Unmonitored hosts" for private addresses no agent reports on.
 */
export function assignGroups(topology: Topology): Map<string, GroupInfo> {
  const { services, connections } = topology
  const hostByIp = new Map<string, string>()
  for (const svc of services) {
    if (hasNode(svc) && svc.pod_ip && !hostByIp.has(svc.pod_ip)) hostByIp.set(svc.pod_ip, svc.node!)
  }

  const out = new Map<string, number>()
  const inc = new Map<string, number>()
  for (const c of connections) {
    if (c.source_id === c.target_id) continue
    out.set(c.source_id, (out.get(c.source_id) || 0) + 1)
    inc.set(c.target_id, (inc.get(c.target_id) || 0) + 1)
  }

  const hosts = new Map<string, GroupInfo>()
  const host = (name: string) => {
    let g = hosts.get(name)
    if (!g) { g = hostGroup(name); hosts.set(name, g) }
    return g
  }

  const groups = new Map<string, GroupInfo>()
  for (const svc of services) {
    if (hasNode(svc)) { groups.set(svc.id, host(svc.node!)); continue }
    const viaIp = svc.pod_ip ? hostByIp.get(svc.pod_ip) : undefined
    if (viaIp) { groups.set(svc.id, host(viaIp)); continue }
    const calls = out.get(svc.id) || 0
    const called = inc.get(svc.id) || 0
    if (calls > 0 && called === 0) groups.set(svc.id, CLIENTS_GROUP)
    else if (isPrivateAddress(svc.pod_ip) && !svc.hostname && !(svc.hostnames && svc.hostnames.length)) groups.set(svc.id, UNMONITORED_GROUP)
    else groups.set(svc.id, EXTERNAL_GROUP)
  }
  return groups
}

/** A key that changes only when what is drawn where changes. */
export function structureKey(topology: Topology, groups: Map<string, GroupInfo>): string {
  const parts: string[] = []
  for (const svc of topology.services) parts.push(`${svc.id}@${groups.get(svc.id)?.id}`)
  parts.sort()
  return parts.join('\n')
}

// ---------------------------------------------------------------------------
// Ranks: how far along the request flow a service sits
// ---------------------------------------------------------------------------

function computeRanks(ids: string[], succ: Map<string, string[]>, pred: Map<string, string[]>): Map<string, number> {
  // 1. Break cycles: an iterative DFS in a fixed order marks back edges.
  const state = new Map<string, number>() // 1 = on stack, 2 = done
  const back = new Set<string>()
  for (const root of ids) {
    if (state.has(root)) continue
    const stack: [string, number][] = [[root, 0]]
    state.set(root, 1)
    while (stack.length) {
      const top = stack[stack.length - 1]
      const next = succ.get(top[0]) || []
      if (top[1] < next.length) {
        const v = next[top[1]++]
        const s = state.get(v)
        if (s === 1) back.add(`${top[0]}\n${v}`)
        else if (s === undefined) { state.set(v, 1); stack.push([v, 0]) }
      } else {
        state.set(top[0], 2)
        stack.pop()
      }
    }
  }

  // 2. Longest path from the sources over the remaining DAG (Kahn order).
  const indeg = new Map<string, number>()
  for (const id of ids) indeg.set(id, 0)
  for (const id of ids) for (const v of succ.get(id) || []) if (!back.has(`${id}\n${v}`)) indeg.set(v, (indeg.get(v) || 0) + 1)
  const rank = new Map<string, number>()
  const queue = ids.filter(id => indeg.get(id) === 0)
  for (const id of queue) rank.set(id, 0)
  for (let i = 0; i < queue.length; i++) {
    const u = queue[i]
    for (const v of succ.get(u) || []) {
      if (back.has(`${u}\n${v}`)) continue
      rank.set(v, Math.max(rank.get(v) || 0, (rank.get(u) || 0) + 1))
      const d = (indeg.get(v) || 0) - 1
      indeg.set(v, d)
      if (d === 0) queue.push(v)
    }
  }

  // 3. A pure caller (a scraper, a worker) sits just before what it calls,
  //    not at the far left with the edge proxies.
  for (const id of ids) {
    if ((pred.get(id) || []).length > 0) continue
    const next = succ.get(id) || []
    if (next.length === 0) continue
    const min = Math.min(...next.map(v => rank.get(v) || 0))
    rank.set(id, Math.max(0, min - 1))
  }
  return rank
}

function median(values: number[]): number {
  if (values.length === 0) return Infinity
  const s = [...values].sort((a, b) => a - b)
  const m = Math.floor(s.length / 2)
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2
}

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

export interface RouteColumn {
  // Card column's horizontal extent.
  x0: number
  x1: number
  // Free vertical intervals a passing edge can use, sorted by y.
  gaps: [number, number][]
}

export interface LayoutResult {
  nodes: Node[]
  edges: Edge[]
}

interface GroupLayout {
  info: GroupInfo
  members: Service[]
  rank: number
  rows: number
  cols: number
  width: number
  height: number
  x: number
  y: number
}

function edgeData(conn: TopologyConnection) {
  return {
    port: conn.port,
    protocol: conn.protocol,
    count: conn.count,
    bytesSent: conn.bytes_sent,
    bytesRecv: conn.bytes_recv,
    bytesSentRate: conn.bytes_sent_rate,
    bytesRecvRate: conn.bytes_recv_rate,
    latency: conn.latency_ms,
  }
}

/**
 * Converts topology data to React Flow nodes and edges, laid out in flow
 * order: inbound clients on the left, then hosts in the order requests reach
 * them (edge, app, data - from the direction of the edges, not from names),
 * then external destinations on the right. Inside a group cards are ordered
 * to keep edges short and uncrossed. The result depends only on the graph's
 * structure, so the same topology always lays out the same way.
 */
export function layoutGraph(topology: Topology, groups = assignGroups(topology)): LayoutResult & { routeColumns: RouteColumn[] } {
  const { services, connections } = topology
  if (services.length === 0) return { nodes: [], edges: [], routeColumns: [] }

  const byId = new Map(services.map(s => [s.id, s]))
  const label = new Map(services.map(s => [s.id, serviceLabel(s).toLowerCase()]))
  const ids = [...byId.keys()].sort()

  const succ = new Map<string, string[]>()
  const pred = new Map<string, string[]>()
  const seen = new Set<string>()
  for (const c of connections) {
    if (c.source_id === c.target_id || !byId.has(c.source_id) || !byId.has(c.target_id)) continue
    const key = `${c.source_id}\n${c.target_id}`
    if (seen.has(key)) continue
    seen.add(key)
    if (!succ.has(c.source_id)) succ.set(c.source_id, [])
    if (!pred.has(c.target_id)) pred.set(c.target_id, [])
    succ.get(c.source_id)!.push(c.target_id)
    pred.get(c.target_id)!.push(c.source_id)
  }
  for (const list of succ.values()) list.sort()
  const rank = computeRanks(ids, succ, pred)

  // --- Groups, with their size and flow position -------------------------
  const groupMap = new Map<string, GroupLayout>()
  for (const id of ids) {
    const info = groups.get(id) || EXTERNAL_GROUP
    let g = groupMap.get(info.id)
    if (!g) {
      g = { info, members: [], rank: 0, rows: 0, cols: 0, width: 0, height: 0, x: 0, y: 0 }
      groupMap.set(info.id, g)
    }
    g.members.push(byId.get(id)!)
  }
  for (const g of groupMap.values()) {
    const n = g.members.length
    g.rows = Math.min(n, Math.max(MIN_ROWS_PER_COLUMN, Math.ceil(Math.sqrt(n * 1.5))))
    g.cols = Math.ceil(n / g.rows)
    g.width = Math.max(MIN_GROUP_W, g.cols * CARD_W + (g.cols - 1) * SUBCOL_GAP + GROUP_PAD_X * 2)
    g.height = GROUP_HEADER_H + g.rows * CARD_H + (g.rows - 1) * ROW_GAP + GROUP_PAD_BOTTOM
    const connected = g.members.filter(s => succ.has(s.id) || pred.has(s.id)).map(s => rank.get(s.id) || 0)
    g.rank = median(connected)
  }

  // Flow columns: clients first, external services last, hosts (and
  // unmonitored addresses) in between by their median rank. Groups whose
  // ranks round to the same step share a column, stacked, as long as the
  // column stays within a height that suits a landscape screen.
  const clients = groupMap.get(CLIENTS_GROUP.id)
  const external = groupMap.get(EXTERNAL_GROUP.id)
  const middle = [...groupMap.values()]
    .filter(g => g !== clients && g !== external)
    .sort((a, b) => a.rank - b.rank || a.info.label.localeCompare(b.info.label))

  const totalArea = [...groupMap.values()].reduce((sum, g) => sum + g.width * g.height, 0)
  const tallest = Math.max(...[...groupMap.values()].map(g => g.height))
  const targetHeight = Math.max(tallest * 1.5, Math.sqrt(totalArea / 1.2))

  const columns: GroupLayout[][] = []
  if (clients) columns.push([clients])
  let current: GroupLayout[] = []
  let currentStep: number | null = null
  let currentHeight = 0
  for (const g of middle) {
    const step = Number.isFinite(g.rank) ? Math.round(g.rank) : Number.MAX_SAFE_INTEGER
    const fits = current.length > 0 && step === currentStep && currentHeight + GROUP_STACK_GAP + g.height <= targetHeight
    if (!fits) {
      if (current.length) columns.push(current)
      current = []
      currentHeight = -GROUP_STACK_GAP
      currentStep = step
    }
    current.push(g)
    currentHeight += GROUP_STACK_GAP + g.height
  }
  if (current.length) columns.push(current)
  if (external) columns.push([external])

  // Place columns left to right, each centred vertically on the tallest.
  const columnHeights = columns.map(col => col.reduce((h, g) => h + g.height, 0) + (col.length - 1) * GROUP_STACK_GAP)
  const maxHeight = Math.max(...columnHeights)
  let x = 0
  columns.forEach((col, i) => {
    const width = Math.max(...col.map(g => g.width))
    let y = Math.round((maxHeight - columnHeights[i]) / 2)
    for (const g of col) {
      g.width = width // one width per column: tidy edges
      g.x = x
      g.y = y
      y += g.height + GROUP_STACK_GAP
    }
    x += width + COLUMN_GAP
  })

  // --- Card order inside each group ---------------------------------------
  // Start by rank then name, then a few barycentre sweeps: each card moves
  // towards the average height of what it talks to. Wrapped groups keep
  // their rank order across sub-columns (flow left to right inside them too)
  // and only reorder within a sub-column.
  const pos = new Map<string, { x: number; y: number }>()
  const ordered = new Map<GroupLayout, Service[]>()
  const place = (g: GroupLayout, order: Service[]) => {
    ordered.set(g, order)
    const blockW = g.cols * CARD_W + (g.cols - 1) * SUBCOL_GAP
    const left = Math.round((g.width - blockW) / 2)
    order.forEach((svc, i) => {
      const col = Math.floor(i / g.rows)
      const row = i % g.rows
      pos.set(svc.id, {
        x: left + col * (CARD_W + SUBCOL_GAP),
        y: GROUP_HEADER_H + row * (CARD_H + ROW_GAP),
      })
    })
  }
  const groupList = columns.flat()
  // Services with no connections at all go to the bottom of their group.
  const linked = (id: string) => succ.has(id) || pred.has(id)
  for (const g of groupList) {
    place(g, [...g.members].sort((a, b) =>
      Number(linked(b.id)) - Number(linked(a.id)) ||
      (rank.get(a.id) || 0) - (rank.get(b.id) || 0) || label.get(a.id)!.localeCompare(label.get(b.id)!)))
  }
  const centreY = (id: string) => {
    const p = pos.get(id)
    const g = groupMap.get((groups.get(id) || EXTERNAL_GROUP).id)!
    return g.y + (p ? p.y : 0) + CARD_H / 2
  }
  for (let sweep = 0; sweep < 4; sweep++) {
    const list = sweep % 2 === 0 ? groupList : [...groupList].reverse()
    for (const g of list) {
      const current = ordered.get(g)!
      const bary = new Map<string, number>()
      current.forEach(svc => {
        const near = [...(succ.get(svc.id) || []), ...(pred.get(svc.id) || [])]
          .filter(v => groups.get(v)?.id !== g.info.id)
        bary.set(svc.id, near.length ? near.reduce((s, v) => s + centreY(v), 0) / near.length
          : linked(svc.id) ? centreY(svc.id) : Number.MAX_SAFE_INTEGER)
      })
      const next: Service[] = []
      for (let c = 0; c < g.cols; c++) {
        const chunk = current.slice(c * g.rows, (c + 1) * g.rows)
        chunk.sort((a, b) => bary.get(a.id)! - bary.get(b.id)! || label.get(a.id)!.localeCompare(label.get(b.id)!))
        next.push(...chunk)
      }
      place(g, next)
    }
  }

  // --- React Flow nodes ---------------------------------------------------
  const listening = new Map<string, Set<number>>()
  for (const c of connections) {
    if (!listening.has(c.target_id)) listening.set(c.target_id, new Set())
    listening.get(c.target_id)!.add(c.port)
  }

  const nodes: Node[] = []
  for (const g of groupList) {
    const metrics = g.info.metricsKey ? topology.node_metrics?.[g.info.metricsKey] : undefined
    nodes.push({
      id: g.info.id,
      type: 'server',
      position: { x: g.x, y: g.y },
      data: {
        label: g.info.label,
        serverName: g.info.label,
        kind: g.info.kind,
        metricsKey: g.info.metricsKey,
        serviceCount: g.members.length,
        totalConnections: 0,
        cpuPercent: metrics?.cpu_percent,
        memPercent: metrics?.mem_percent,
        memUsed: metrics?.mem_used,
        memTotal: metrics?.mem_total,
      },
      style: { width: g.width, height: g.height, pointerEvents: 'none' },
      zIndex: 5,
      selectable: false,
      draggable: false,
    })
  }
  for (const g of groupList) {
    for (const svc of ordered.get(g)!) {
      nodes.push({
        id: svc.id,
        type: 'service',
        position: pos.get(svc.id)!,
        parentId: g.info.id,
        extent: 'parent',
        data: serviceNodeData(svc, listening, succ, pred),
        style: { width: CARD_W, height: CARD_H },
        width: CARD_W,
        height: CARD_H,
        zIndex: 100,
      })
    }
  }

  // --- Route columns: where a long edge may cross a column ----------------
  // Obstacles per card column: its cards and the title bars of its groups.
  const blocksByX = new Map<number, [number, number][]>()
  for (const g of groupList) {
    const blockW = g.cols * CARD_W + (g.cols - 1) * SUBCOL_GAP
    const left = g.x + Math.round((g.width - blockW) / 2)
    for (let c = 0; c < g.cols; c++) {
      const x0 = left + c * (CARD_W + SUBCOL_GAP)
      if (!blocksByX.has(x0)) blocksByX.set(x0, [])
      const blocks = blocksByX.get(x0)!
      blocks.push([g.y, g.y + GROUP_HEADER_H])
      const count = Math.min(g.rows, g.members.length - c * g.rows)
      for (let r = 0; r < count; r++) {
        const top = g.y + GROUP_HEADER_H + r * (CARD_H + ROW_GAP)
        blocks.push([top, top + CARD_H])
      }
    }
  }
  const routeColumns: RouteColumn[] = [...blocksByX.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([x0, blocks]) => {
      blocks.sort((a, b) => a[0] - b[0])
      const gaps: [number, number][] = [[-Infinity, blocks[0][0] - 6]]
      for (let i = 1; i < blocks.length; i++) {
        const lo = blocks[i - 1][1]
        const hi = blocks[i][0]
        if (hi - lo >= 8) gaps.push([lo, hi])
      }
      gaps.push([blocks[blocks.length - 1][1] + 6, Infinity])
      return { x0, x1: x0 + CARD_W, gaps }
    })

  // --- Edges ---------------------------------------------------------------
  const abs = new Map<string, { x: number; y: number }>()
  for (const g of groupList) for (const svc of g.members) {
    const p = pos.get(svc.id)!
    abs.set(svc.id, { x: g.x + p.x, y: g.y + p.y })
  }
  const edges: Edge[] = connections
    .filter(c => byId.has(c.source_id) && byId.has(c.target_id))
    .map(conn => ({
      id: conn.id,
      source: conn.source_id,
      target: conn.target_id,
      type: 'connection',
      zIndex: 0,
      data: {
        ...edgeData(conn),
        waypoints: waypointsFor(abs.get(conn.source_id)!, abs.get(conn.target_id)!, routeColumns),
      },
    }))

  return { nodes, edges, routeColumns }
}

/**
 * The points a long edge passes through so it crosses intermediate columns
 * in the gaps between cards rather than behind them. Each crossed column
 * contributes two points at the same height: the edge runs straight through.
 */
export function waypointsFor(
  src: { x: number; y: number },
  dst: { x: number; y: number },
  columns: RouteColumn[],
): [number, number][] | undefined {
  const sy = src.y + CARD_H / 2
  const ty = dst.y + CARD_H / 2
  const forward = dst.x > src.x + CARD_W
  const backward = dst.x + CARD_W < src.x
  if (!forward && !backward) return undefined
  const sx = forward ? src.x + CARD_W : src.x
  const tx = forward ? dst.x : dst.x + CARD_W
  const lo = Math.min(sx, tx)
  const hi = Math.max(sx, tx)
  const crossed = columns.filter(c => c.x0 > lo && c.x1 < hi)
  if (crossed.length === 0) return undefined
  if (backward) crossed.reverse()
  const points: [number, number][] = []
  for (const col of crossed) {
    const mid = (col.x0 + col.x1) / 2
    const ideal = sy + (ty - sy) * ((mid - sx) / (tx - sx))
    let best = ideal
    let bestDist = Infinity
    for (const [a, b] of col.gaps) {
      const y = Number.isFinite(a) && Number.isFinite(b) ? (a + b) / 2
        : !Number.isFinite(a) ? Math.min(ideal, b - 4) : Math.max(ideal, a + 4)
      const d = Math.abs(y - ideal)
      if (d < bestDist) { bestDist = d; best = y }
    }
    const pad = 10
    if (forward) points.push([col.x0 - pad, best], [col.x1 + pad, best])
    else points.push([col.x1 + pad, best], [col.x0 - pad, best])
  }
  return points
}

function serviceNodeData(
  service: Service,
  listening: Map<string, Set<number>>,
  succ: Map<string, string[]>,
  pred: Map<string, string[]>,
) {
  return {
    label: serviceLabel(service),
    service,
    incomingCount: (pred.get(service.id) || []).length,
    outgoingCount: (succ.get(service.id) || []).length,
    healthy: service.healthy !== false,
    ports: [...(listening.get(service.id) || [])].sort((a, b) => a - b),
  }
}

// What a service card shows; when none of it changed, the node object is
// kept as it is so React Flow does not re-render it.
function cardSignature(svc: Service, ports: number[]): string {
  return [serviceLabel(svc), svc.tech, svc.type, svc.icon, svc.pod_ip, svc.healthy !== false,
    svc.display_name, (svc.hostnames || []).join(','), svc.hostname, svc.aggregated_count, ports.join(',')].join('|')
}

/**
 * Updates node data without changing positions, for live updates that do
 * not change the structure. Nodes whose visible content did not change keep
 * their identity, so a live tick re-renders only what moved.
 */
export function updateNodeData(currentNodes: Node[], topology: Topology): Node[] {
  const serviceMap = new Map<string, Service>()
  topology.services.forEach(s => serviceMap.set(s.id, s))

  const listening = new Map<string, Set<number>>()
  const succ = new Map<string, string[]>()
  const pred = new Map<string, string[]>()
  for (const c of topology.connections) {
    if (!listening.has(c.target_id)) listening.set(c.target_id, new Set())
    listening.get(c.target_id)!.add(c.port)
    if (!succ.has(c.source_id)) succ.set(c.source_id, [])
    if (!pred.has(c.target_id)) pred.set(c.target_id, [])
    succ.get(c.source_id)!.push(c.target_id)
    pred.get(c.target_id)!.push(c.source_id)
  }

  let changed = false
  const next = currentNodes.map(node => {
    if (node.type === 'server') {
      const key = node.data.metricsKey as string | undefined
      const m = key ? topology.node_metrics?.[key] : undefined
      if (m?.cpu_percent === node.data.cpuPercent && m?.mem_percent === node.data.memPercent) return node
      changed = true
      return {
        ...node,
        data: { ...node.data, cpuPercent: m?.cpu_percent, memPercent: m?.mem_percent, memUsed: m?.mem_used, memTotal: m?.mem_total },
      }
    }
    const service = serviceMap.get(node.id)
    if (!service) return node
    const data = serviceNodeData(service, listening, succ, pred)
    const old = node.data as ReturnType<typeof serviceNodeData>
    if (cardSignature(service, data.ports) === cardSignature(old.service, old.ports)
      && old.incomingCount === data.incomingCount && old.outgoingCount === data.outgoingCount) return node
    changed = true
    return { ...node, data: { ...node.data, ...data } }
  })
  return changed ? next : currentNodes
}

// Edge width steps by throughput; a rate change that stays in its step
// needs no re-render (labels read exact rates from the topology).
export function rateStep(rate: number | undefined): number {
  if (!rate || rate <= 0) return 0
  if (rate < 10 * 1024) return 1
  if (rate < 100 * 1024) return 2
  if (rate < 1024 * 1024) return 3
  return 4
}

/**
 * Updates edge data without recreating edges. Keeps an edge object as it is
 * when nothing it draws changed.
 */
export function updateEdgeData(currentEdges: Edge[], topology: Topology): Edge[] {
  const connectionMap = new Map<string, TopologyConnection>()
  topology.connections.forEach(c => connectionMap.set(c.id, c))

  let changed = false
  const next = currentEdges.map(edge => {
    const conn = connectionMap.get(edge.id)
    if (!conn) return edge
    const d = edge.data as ReturnType<typeof edgeData>
    const oldStep = rateStep(Math.max(d.bytesSentRate || 0, d.bytesRecvRate || 0))
    const newStep = rateStep(Math.max(conn.bytes_sent_rate || 0, conn.bytes_recv_rate || 0))
    if (oldStep === newStep && d.port === conn.port && d.protocol === conn.protocol) return edge
    changed = true
    return { ...edge, data: { ...edge.data, ...edgeData(conn) } }
  })
  return changed ? next : currentEdges
}

// Exposed for the search and diff panels: the label of an ID that may or may
// not be on the graph.
export function labelForId(id: string, services: Map<string, Service>): string {
  const svc = services.get(id)
  if (svc) return serviceLabel(svc)
  return isAddressLike(id) ? id : id.replace(/^[^/]*\//, '')
}
