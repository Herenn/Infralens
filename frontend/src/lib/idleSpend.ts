// Types and helpers for GET /api/v1/graph/idle-cost (see backend
// service/idlecost.go for the authoritative definitions and policy).

export type Confidence = 'high' | 'medium' | 'low' | 'none'

export type IdleCategory = 'orphan' | 'no_inbound' | 'quiet' | 'idle_callers_only' | 'stale'

export interface PriceRule {
  pattern: string
  monthly: number
}

export interface IdleCostEstimate {
  monthly: number
  annual: number
  node_monthly: number
  share: number
  shared_with: number
  price_source: 'service_rule' | 'node_rule' | 'default' | 'assumed' | 'cluster'
  price_rule?: string
  share_method: 'direct' | 'carve_out' | 'whole_node' | 'even_split' | 'cluster_split'
  explanation: string
}

export interface IdleEvidence {
  last_inbound: string | null
  quiet_seconds: number // observed silence
  quiet_wall_seconds: number
  unobserved_seconds: number
  quiet_is_lower_bound: boolean
  first_seen: string | null
  last_seen: string
  live_inbound_edges: number
  live_outbound_edges: number
  outbound_peers?: string[]
  outbound_bytes: number
  inbound_bytes: number
  ignored_callers?: string[]
  idle_callers?: string[]
  notes: string[]
}

export interface IdleServiceItem {
  id: string
  name: string
  type?: string
  tech?: string
  namespace?: string
  node?: string
  in_graph: boolean
  category: IdleCategory
  evidence: IdleEvidence
  cost: IdleCostEstimate | null
  idle_confidence: Confidence
  cost_confidence: Confidence
  confidence: Confidence
  in_total: boolean
  score: number
}

export interface IdleNodeSummary {
  node: string
  monthly: number | null
  price_source?: string
  price_rule?: string
  services: number
  idle_services: number
  idle_monthly: number
  fully_idle: boolean
  cluster: boolean
  agent_nodes?: string[]
  reporting: boolean
  cpu_percent?: number
  mem_percent?: number
}

export interface IdleExclusion {
  id: string
  name: string
  node?: string
  reason: string
  detail: string
}

// A CLUSTER_NAME member that stopped reporting within the evidence window.
// While a group has one, its candidates are low confidence and left out of
// the total (and report.notes opens with a sentence per group saying so).
export interface IdleSilentAgent {
  group: string
  node: string
  last_seen: string
}

export interface IdleSpendReport {
  generated_at: string
  beta: boolean
  disclaimer: string
  currency: string
  currency_symbol: string
  pricing: {
    configured: boolean
    source: 'env' | 'demo' | 'assumed' | 'none' | string
    default_node_monthly: number | null
    assumed_node_monthly: number | null
    node_rules: PriceRule[]
    service_rules: PriceRule[]
    warnings: string[]
  }
  evidence_window: {
    history_enabled: boolean
    from: string
    to: string
    observed_seconds: number
    window_seconds: number
    quiet_after_seconds: number
    warmup_seconds: number
    warming_up: boolean
    unobserved_seconds: number
    cluster_grouped: boolean
  }
  totals: {
    monthly: number
    annual: number
    services: number
    priced: number
    unpriced: number
    monthly_by_confidence: Record<'high' | 'medium' | 'low', number>
    not_in_total_monthly: number
  }
  items: IdleServiceItem[]
  nodes: IdleNodeSummary[]
  excluded: IdleExclusion[]
  excluded_count: number
  notes: string[]
  // Newest silence first, at most 20. Absent from older backends.
  silent_agents?: IdleSilentAgent[]
}

/**
 * Why an item's cost is shown but kept out of the total. The report has no
 * per-item reason, so it is inferred: without history every out-of-total
 * item is current-window evidence only; with history the only cause is a
 * silent agent in the item's group. Anything else gets neutral wording.
 */
export function notInTotalReason(report: IdleSpendReport): 'current_window' | 'silent_agent' | 'unknown' {
  if (!report.evidence_window.history_enabled) return 'current_window'
  if ((report.silent_agents?.length ?? 0) > 0) return 'silent_agent'
  return 'unknown'
}

export const NOT_IN_TOTAL_TEXT: Record<ReturnType<typeof notInTotalReason>, { short: string; long: string }> = {
  current_window: { short: 'current-window only, not in total', long: 'Not in the total: current-window evidence only' },
  silent_agent: {
    short: 'not in total: an agent in its group stopped reporting',
    long: 'Not in the total: an agent in this group stopped reporting, so this may be in use',
  },
  unknown: { short: 'not in total, see note', long: 'Not in the total: see the note' },
}

/** The notes about silent agents: report.notes opens with one per group. */
export function splitSilentNotes(report: IdleSpendReport): { silent: string[]; other: string[] } {
  const agents = report.silent_agents ?? []
  if (agents.length === 0) return { silent: [], other: report.notes }
  const groups = new Set(agents.map(a => a.group)).size
  const n = Math.min(groups, report.notes.length)
  return { silent: report.notes.slice(0, n), other: report.notes.slice(n) }
}

export const CATEGORY_LABELS: Record<IdleCategory, string> = {
  orphan: 'No connections',
  no_inbound: 'Nothing connects',
  quiet: 'Inbound stopped',
  idle_callers_only: 'Only idle callers',
  stale: 'Not seen',
}

export const EXCLUSION_LABELS: Record<string, string> = {
  warmup: 'Too new',
  outbound_only: 'Worker / cron',
  monitoring: 'Monitoring',
  reached_by_address: 'Reached by address',
  infralens: 'InfraLens',
  user_excluded: 'Excluded by config',
  node_gone: 'Node gone',
  not_quiet_long_enough: 'Not enough observed silence',
  live_traffic: 'Live connection',
  possible_replica: 'Possible replica',
  udp_service: 'UDP traffic',
  history_disabled: 'History disabled',
}

/** Formats an amount: whole units from 100 up (or when whole), cents below. */
export function formatMoney(symbol: string, value: number): string {
  const digits = Math.abs(value) >= 100 || Number.isInteger(value) ? 0 : 2
  const n = value.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits })
  return `${symbol}${n}`
}

/** Coarse duration: "9d", "4d 6h", "3h", "12m". */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d >= 3) return `${d}d`
  if (d > 0) return h ? `${d}d ${h}h` : `${d}d`
  if (h > 0) return `${h}h`
  return `${m}m`
}
