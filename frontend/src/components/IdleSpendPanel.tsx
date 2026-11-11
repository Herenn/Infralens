import { useCallback, useEffect, useState } from 'react'
import {
  PiggyBank, X, RefreshCw, Copy, Check, FileSpreadsheet, ChevronDown, ChevronRight,
  Crosshair, Info, ShieldCheck, AlertTriangle, History, Server,
} from 'lucide-react'
import { apiUrl } from '../lib/api'
import { DOCK_CLASS } from './dock'
import {
  CATEGORY_LABELS, Confidence, EXCLUSION_LABELS, IdleServiceItem, IdleSpendReport,
  formatDuration, formatMoney, itemNotInTotalText, silentAgentCount, silentAgentsHeadline, splitSilentNotes, totalsNotInTotalText,
} from '../lib/idleSpend'

interface IdleSpendPanelProps {
  onClose: () => void
  /** Focus a service on the graph (only called for items present in it). */
  onFocusService: (id: string) => void
  focusedId: string | null
}

const ENDPOINT = '/api/v1/graph/idle-cost'

const CONFIDENCE_STYLES: Record<Confidence, { dot: string; text: string; bar: string; label: string }> = {
  high: { dot: 'bg-emerald-400', text: 'text-emerald-300', bar: 'bg-emerald-400', label: 'High' },
  medium: { dot: 'bg-amber-400', text: 'text-amber-300', bar: 'bg-amber-400', label: 'Medium' },
  low: { dot: 'bg-slate-400', text: 'text-slate-300', bar: 'bg-slate-500', label: 'Low' },
  none: { dot: 'bg-dark-600', text: 'text-dark-400', bar: 'bg-dark-600', label: 'n/a' },
}

const CATEGORY_STYLES: Record<string, string> = {
  orphan: 'bg-rose-500/10 text-rose-300 border-rose-500/20',
  no_inbound: 'bg-orange-500/10 text-orange-300 border-orange-500/20',
  quiet: 'bg-amber-500/10 text-amber-300 border-amber-500/20',
  idle_callers_only: 'bg-violet-500/10 text-violet-300 border-violet-500/20',
  stale: 'bg-slate-500/10 text-slate-300 border-slate-500/20',
}

function withQuery(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams()
  Object.entries(params).forEach(([k, v]) => { if (v) q.set(k, v) })
  const s = q.toString()
  return apiUrl(s ? `${ENDPOINT}?${s}` : ENDPOINT)
}

function pricingChip(r: IdleSpendReport): { label: string; className: string; title: string } {
  const sym = r.currency_symbol
  switch (r.pricing.source) {
    case 'demo':
      return { label: 'Demo prices', className: 'bg-sky-500/10 text-sky-300 border-sky-500/20', title: 'Illustrative prices set by demo mode - not real costs' }
    case 'assumed':
      return {
        label: `Assumed ${formatMoney(sym, r.pricing.assumed_node_monthly ?? 0)}/node`,
        className: 'bg-amber-500/10 text-amber-300 border-amber-500/20',
        title: 'A what-if you entered. Set IDLE_COST_NODE_MONTHLY to make it permanent.',
      }
    case 'none':
      return { label: 'No prices set', className: 'bg-dark-700 text-dark-300 border-dark-600', title: 'Set IDLE_COST_NODE_MONTHLY to estimate costs' }
    default:
      return { label: 'Configured prices', className: 'bg-emerald-500/10 text-emerald-300 border-emerald-500/20', title: 'From IDLE_COST_* settings' }
  }
}

/** Copies text, falling back to execCommand where the async clipboard API
 * is unavailable (it requires a secure context). Resolves false if neither
 * works. */
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // fall through to the legacy path
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok
}

export default function IdleSpendPanel({ onClose, onFocusService, focusedId }: IdleSpendPanelProps) {
  const [report, setReport] = useState<IdleSpendReport | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [assumed, setAssumed] = useState<string>('')
  const [appliedAssumption, setAppliedAssumption] = useState<string | undefined>(undefined)
  const [expanded, setExpanded] = useState<string | null>(null)
  const [showExcluded, setShowExcluded] = useState(false)
  const [showMethod, setShowMethod] = useState(false)
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'manual' | 'failed'>('idle')
  const [manualText, setManualText] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const [assumeError, setAssumeError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch(withQuery({ assume_node_monthly: appliedAssumption }))
      .then(res => {
        if (res.status === 401) throw new Error('Not authorised to read idle spend.')
        if (res.status === 400) throw new Error('The what-if price was rejected - enter a positive amount up to 1,000,000.')
        if (!res.ok) throw new Error(`Failed to load (${res.status})`)
        return res.json()
      })
      .then((data: IdleSpendReport) => { if (!cancelled) setReport(data) })
      .catch(err => {
        if (cancelled) return
        // Never leave numbers from a different query on screen under an error.
        setReport(null)
        setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [appliedAssumption, reloadKey])

  const copyMarkdown = useCallback(async () => {
    try {
      const res = await fetch(withQuery({ format: 'markdown', assume_node_monthly: appliedAssumption }))
      if (!res.ok) throw new Error(String(res.status))
      const text = await res.text()
      if (await copyText(text)) {
        setCopyState('copied')
        setManualText(null)
        window.setTimeout(() => setCopyState('idle'), 1800)
      } else {
        // No clipboard access (e.g. plain http on a non-localhost host):
        // show the text so it can be copied by hand.
        setCopyState('manual')
        setManualText(text)
      }
    } catch (err) {
      console.error('Copy failed:', err)
      setCopyState('failed')
      window.setTimeout(() => setCopyState('idle'), 2500)
    }
  }, [appliedAssumption])

  const downloadCsv = useCallback(async () => {
    try {
      const res = await fetch(withQuery({ format: 'csv', assume_node_monthly: appliedAssumption }))
      if (!res.ok) throw new Error(String(res.status))
      const url = URL.createObjectURL(await res.blob())
      const link = document.createElement('a')
      link.download = `infralens-idle-spend-${new Date().toISOString().slice(0, 10)}.csv`
      link.href = url
      link.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('CSV export failed:', err)
    }
  }, [appliedAssumption])

  const sym = report?.currency_symbol ?? '$'
  const totals = report?.totals
  const priced = (totals?.priced ?? 0) > 0
  const chip = report ? pricingChip(report) : null
  const needsPrice = report && !report.pricing.configured
  const notes = report ? splitSilentNotes(report) : { silent: [], other: [] }
  const silentCount = report ? silentAgentCount(report) : 0

  return (
    <section className={DOCK_CLASS} data-panel="idle" aria-label="Idle spend">
      {/* Title */}
      <div className="flex items-center gap-3 px-4 pt-4 pb-3 shrink-0">
        <div className="w-8 h-8 rounded-lg bg-amber-500/15 border border-amber-500/25 flex items-center justify-center">
          <PiggyBank size={16} className="text-amber-300" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5 text-sm font-semibold text-dark-100">
            Idle spend
            <span className="text-[9px] font-semibold uppercase tracking-wider px-1.5 py-px rounded bg-sky-500/15 text-sky-300 border border-sky-500/25">Beta</span>
          </div>
          <div className="text-[11px] text-dark-500">Cleanup candidates to verify - not recommendations to delete</div>
        </div>
        <button
          onClick={() => setReloadKey(k => k + 1)}
          className="p-1.5 rounded-md text-dark-400 hover:text-white hover:bg-dark-800 transition-colors"
          title="Refresh"
          aria-label="Refresh idle spend"
        >
          <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
        </button>
        <button onClick={onClose} className="p-1.5 rounded-md text-dark-400 hover:text-white hover:bg-dark-800 transition-colors" title="Close" aria-label="Close idle spend panel" data-testid="panel-close">
          <X size={16} />
        </button>
      </div>

      {error && <p className="mx-4 mb-3 text-xs text-red-300 bg-red-500/10 border border-red-500/20 rounded-lg px-3 py-2">{error}</p>}
      {!report && !error && <p className="px-4 pb-4 text-xs text-dark-500">Loading…</p>}

      {report && totals && (
        <>
          {/* Agents that stopped reporting: the list below is unreliable for
              their groups, so this leads, ahead of the headline. Absent
              (and nothing rendered) when every agent reports. */}
          {report && silentCount > 0 && (
            <div
              role="alert"
              data-testid="idle-silent-agents"
              className="mx-4 mb-3 rounded-lg border border-orange-500/40 bg-orange-500/10 px-3 py-2.5 shrink-0"
            >
              <div className="flex items-start gap-2 text-xs">
                <AlertTriangle size={14} className="shrink-0 mt-px text-orange-300" />
                <p className="leading-snug">
                  <span className="font-semibold text-orange-200">
                    {silentAgentsHeadline(report)}
                  </span>{' '}
                  <span className="text-orange-200/70">- this list may be wrong for {silentCount === 1 ? 'its group' : 'their groups'}</span>
                </p>
              </div>
              {notes.silent.map(n => (
                <p key={n} className="mt-1.5 text-[11px] leading-snug text-orange-100/85">{n}</p>
              ))}
            </div>
          )}

          {/* Headline */}
          <div className="mx-4 mb-3 rounded-lg border border-dark-700 bg-gradient-to-br from-dark-800 to-dark-900 px-4 py-3 shrink-0">
            <div className="flex items-start justify-between gap-2">
              <div>
                {priced ? (
                  <>
                    <div className="flex items-baseline gap-1">
                      <span className="text-3xl font-semibold tracking-tight text-white">~{formatMoney(sym, totals.monthly)}</span>
                      <span className="text-sm text-dark-400">/mo</span>
                    </div>
                    <div className="text-xs text-dark-400 mt-0.5">
                      across {totals.services} candidate{totals.services === 1 ? '' : 's'}
                      <span className="text-dark-600"> · </span>~{formatMoney(sym, totals.annual)}/yr
                      {totals.unpriced > 0 && <span className="text-dark-500"> · {totals.unpriced} unpriced</span>}
                    </div>
                    {totals.not_in_total_monthly > 0 && (
                      <div className="text-[10px] text-dark-500 mt-0.5">
                        +{formatMoney(sym, totals.not_in_total_monthly)}/mo {totalsNotInTotalText(report)}
                      </div>
                    )}
                  </>
                ) : (
                  <>
                    <div className="text-2xl font-semibold text-white">{totals.services} candidate{totals.services === 1 ? '' : 's'}</div>
                    <div className="text-xs text-dark-400 mt-0.5">{needsPrice ? 'Costs not estimated - no prices configured' : 'No priced services'}</div>
                  </>
                )}
              </div>
              {chip && (
                <span className={`shrink-0 text-[10px] font-medium px-2 py-0.5 rounded-full border ${chip.className}`} title={chip.title}>
                  {chip.label}
                </span>
              )}
            </div>

            {priced && totals.monthly > 0 && (
              <div className="mt-3">
                <div className="flex h-1.5 rounded-full overflow-hidden bg-dark-700">
                  {(['high', 'medium', 'low'] as const).map(c => {
                    const v = totals.monthly_by_confidence[c] || 0
                    return v > 0 ? <div key={c} className={CONFIDENCE_STYLES[c].bar} style={{ width: `${(v / totals.monthly) * 100}%` }} /> : null
                  })}
                </div>
                <div className="flex gap-3 mt-1.5 text-[10px] text-dark-400">
                  {(['high', 'medium', 'low'] as const).map(c => (
                    <span key={c} className="flex items-center gap-1">
                      <span className={`w-1.5 h-1.5 rounded-full ${CONFIDENCE_STYLES[c].dot}`} />
                      {CONFIDENCE_STYLES[c].label} {formatMoney(sym, totals.monthly_by_confidence[c] || 0)}
                    </span>
                  ))}
                </div>
              </div>
            )}

            <div className="mt-2 flex items-center gap-1 text-[10px] text-dark-500">
              <History size={11} />
              {report.evidence_window.history_enabled
                ? <>
                    Evidence from {formatDuration(report.evidence_window.observed_seconds)} of observed network history
                    {report.evidence_window.unobserved_seconds > 3600 && <> ({formatDuration(report.evidence_window.unobserved_seconds)} not observed)</>}
                  </>
                : <>History disabled - current window only</>}
            </div>
          </div>

          {/* What-if when nothing is configured */}
          {needsPrice && (
            <form
              className="mx-4 mb-3 flex items-center gap-2 text-xs shrink-0"
              onSubmit={e => {
                e.preventDefault()
                const v = Number(assumed)
                if (assumed && !(v > 0 && v <= 1_000_000)) {
                  setAssumeError('Enter a positive amount up to 1,000,000.')
                  return
                }
                setAssumeError(null)
                setAppliedAssumption(assumed ? assumed : undefined)
              }}
            >
              <label htmlFor="idle-assume" className="text-dark-400">What if a node costs</label>
              <div className="relative">
                <span className="absolute left-2 top-1/2 -translate-y-1/2 text-dark-500">{sym}</span>
                <input
                  id="idle-assume" aria-label="Assumed monthly cost per node"
                  type="number" min="1" max="1000000" step="any" value={assumed} onChange={e => setAssumed(e.target.value)}
                  placeholder="150"
                  className="w-20 pl-5 pr-1 py-1 rounded-md bg-dark-800 border border-dark-600 text-dark-100 focus:outline-none focus:border-amber-500/50"
                />
              </div>
              <span className="text-dark-400">/mo?</span>
              <button type="submit" className="px-2 py-1 rounded-md bg-amber-500/15 text-amber-300 border border-amber-500/25 hover:bg-amber-500/25">
                Apply
              </button>
              {assumeError && <span role="alert" className="text-red-300">{assumeError}</span>}
            </form>
          )}

          {/* Notes and warnings */}
          {(notes.other.length > 0 || report.pricing.warnings.length > 0) && (
            <div className="mx-4 mb-2 space-y-1.5 shrink-0">
              {report.pricing.warnings.map(w => (
                <div key={w} className="flex gap-2 text-[11px] text-red-300 bg-red-500/10 border border-red-500/20 rounded-md px-2.5 py-1.5">
                  <AlertTriangle size={12} className="shrink-0 mt-0.5" />{w}
                </div>
              ))}
              {notes.other.map(n => (
                <div key={n} className="flex gap-2 text-[11px] text-amber-200/90 bg-amber-500/5 border border-amber-500/15 rounded-md px-2.5 py-1.5">
                  <Server size={12} className="shrink-0 mt-0.5 text-amber-300" />{n}
                </div>
              ))}
            </div>
          )}

          {/* Ranked list */}
          <div className="flex-1 overflow-y-auto px-2 pb-2 min-h-0">
            {report.items.length === 0 && (
              <div className="px-3 py-6 text-center">
                <ShieldCheck size={22} className="mx-auto text-lens-400 mb-2" />
                <p className="text-sm text-dark-200">
                  {report.evidence_window.warming_up ? 'Still collecting evidence' : 'Nothing idle found'}
                </p>
                <p className="text-xs text-dark-500 mt-1">
                  {report.evidence_window.warming_up
                    ? 'Idle detection starts once enough history has been recorded.'
                    : 'Every service has been talked to recently.'}
                </p>
              </div>
            )}
            {report.items.map((item, i) => (
              <IdleRow
                key={item.id}
                rank={i + 1}
                item={item}
                symbol={sym}
                expanded={expanded === item.id}
                focused={focusedId === item.id}
                onClick={() => {
                  setExpanded(cur => (cur === item.id ? null : item.id))
                  if (item.in_graph) onFocusService(item.id)
                }}
              />
            ))}

            {report.excluded_count > 0 && (
              <div className="mt-2 mx-1">
                <button
                  onClick={() => setShowExcluded(v => !v)}
                  className="w-full flex items-center gap-1.5 px-2 py-1.5 text-[11px] text-dark-400 hover:text-dark-200"
                >
                  {showExcluded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  <ShieldCheck size={12} className="text-lens-400" />
                  {report.excluded_count} quiet service{report.excluded_count === 1 ? '' : 's'} held back, and why
                </button>
                {showExcluded && (
                  <div className="space-y-1 pb-1">
                    {report.excluded.map(e => (
                      <div key={e.id + e.reason} className="px-3 py-1.5 rounded-md bg-dark-800/50">
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-xs text-dark-300 truncate" title={e.id}>{e.name}</span>
                          <span className="shrink-0 text-[10px] text-dark-400">{EXCLUSION_LABELS[e.reason] ?? e.reason}</span>
                        </div>
                        <div className="text-[10px] text-dark-500 leading-snug">{e.detail}</div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            <div className="mx-1">
              <button
                onClick={() => setShowMethod(v => !v)}
                className="w-full flex items-center gap-1.5 px-2 py-1.5 text-[11px] text-dark-400 hover:text-dark-200"
              >
                {showMethod ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                <Info size={12} />
                How this is estimated
              </button>
              {showMethod && <MethodExplainer report={report} />}
            </div>
          </div>

          {/* Export */}
          <div className="flex items-center gap-2 px-4 py-2.5 border-t border-dark-800 shrink-0">
            <button
              onClick={copyMarkdown}
              className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs bg-dark-800 border border-dark-700 text-dark-300 hover:text-white hover:border-dark-500 transition-colors"
              title="Copy a Markdown summary for a cleanup ticket"
            >
              {copyState === 'copied' ? <Check size={13} className="text-lens-400" /> : <Copy size={13} />}
              {copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy as Markdown'}
            </button>
            <button
              onClick={downloadCsv}
              className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs bg-dark-800 border border-dark-700 text-dark-300 hover:text-white hover:border-dark-500 transition-colors"
              title="Download one row per idle service"
            >
              <FileSpreadsheet size={13} />
              CSV
            </button>
            <span className="ml-auto text-[10px] text-dark-600">estimates, not billing data</span>
          </div>
          {manualText !== null && (
            <div className="px-4 pb-3 shrink-0">
              <div className="flex items-center justify-between text-[11px] text-amber-200 mb-1">
                <span>Clipboard unavailable here - select all and copy:</span>
                <button onClick={() => { setManualText(null); setCopyState('idle') }} className="text-dark-400 hover:text-white" aria-label="Close copy box">
                  <X size={12} />
                </button>
              </div>
              <textarea
                readOnly value={manualText} aria-label="Markdown summary to copy"
                onFocus={e => e.currentTarget.select()} autoFocus
                className="w-full h-28 text-[10px] font-mono bg-dark-950 border border-dark-700 rounded-md p-2 text-dark-300"
              />
            </div>
          )}
        </>
      )}
    </section>
  )
}

function IdleRow({ rank, item, symbol, expanded, focused, onClick }: {
  rank: number
  item: IdleServiceItem
  symbol: string
  expanded: boolean
  focused: boolean
  onClick: () => void
}) {
  const conf = CONFIDENCE_STYLES[item.confidence]
  const quiet = formatDuration(item.evidence.quiet_seconds)
  return (
    <button
      onClick={onClick}
      className={`w-full text-left px-3 py-2.5 rounded-lg transition-colors border
        ${focused ? 'bg-amber-500/5 border-amber-500/30' : 'border-transparent hover:bg-dark-800/70'}`}
    >
      <div className="flex items-center gap-2">
        <span className="w-4 text-[10px] text-dark-600 tabular-nums">{rank}</span>
        <span className="flex-1 min-w-0 text-sm font-medium text-dark-100 truncate" title={item.id}>{item.name}</span>
        {item.cost ? (
          <span className={`text-sm font-semibold tabular-nums ${item.in_total ? 'text-white' : 'text-dark-400'}`}
            title={item.in_total ? undefined : itemNotInTotalText(item)}>
            {formatMoney(symbol, item.cost.monthly)}<span className="text-[10px] font-normal text-dark-500">/mo</span>
          </span>
        ) : (
          <span className="text-xs text-dark-500">unpriced</span>
        )}
      </div>
      <div className="flex items-center gap-1.5 mt-1 ml-6 flex-wrap">
        <span className={`text-[10px] px-1.5 py-px rounded border ${CATEGORY_STYLES[item.category] ?? ''}`}>
          {CATEGORY_LABELS[item.category] ?? item.category}
        </span>
        <span className="text-[10px] text-dark-400">
          {item.evidence.quiet_is_lower_bound ? `${quiet}+ quiet` : `quiet ${quiet}`}
          {item.evidence.unobserved_seconds > 3600 && ` (observed; ${formatDuration(item.evidence.unobserved_seconds)} gap)`}
        </span>
        {item.node && <span className="text-[10px] text-dark-500">· {item.node}</span>}
        {!item.in_graph && <span className="text-[10px] text-dark-500">· history only</span>}
        <span className={`ml-auto flex items-center gap-1 text-[10px] ${conf.text}`} title={`Idle: ${item.idle_confidence} · Cost: ${item.cost_confidence}`}>
          <span className={`w-1.5 h-1.5 rounded-full ${conf.dot}`} />
          {conf.label}
        </span>
      </div>
      {!expanded && item.evidence.notes[0] && (
        <div className="mt-1 ml-6 text-[11px] text-dark-400 leading-snug line-clamp-2">{item.evidence.notes[0]}</div>
      )}
      {expanded && (
        <div className="mt-2 ml-6 space-y-1.5">
          <ul className="space-y-1">
            {item.evidence.notes.map(n => (
              <li key={n} className="flex gap-1.5 text-[11px] text-dark-300 leading-snug">
                <span className="text-dark-600">•</span>{n}
              </li>
            ))}
          </ul>
          {item.cost && (
            <div className="text-[11px] text-dark-400 bg-dark-800/60 rounded-md px-2 py-1.5">
              <span className="text-dark-300">{formatMoney(symbol, item.cost.monthly)}/mo</span> = {item.cost.explanation}
              <div className="text-[10px] text-dark-500 mt-0.5">
                idle confidence {item.idle_confidence} · cost confidence {item.cost_confidence}
              </div>
            </div>
          )}
          {item.in_graph && focused && (
            <div className="flex items-center gap-1 text-[10px] text-amber-300/80">
              <Crosshair size={11} /> highlighted on the graph
            </div>
          )}
        </div>
      )}
    </button>
  )
}

function MethodExplainer({ report }: { report: IdleSpendReport }) {
  const sym = report.currency_symbol
  const quietAfter = formatDuration(report.evidence_window.quiet_after_seconds)
  return (
    <div className="mx-2 mb-2 px-3 py-2.5 rounded-lg bg-dark-800/50 text-[11px] text-dark-400 leading-relaxed space-y-2">
      <p>
        <span className="text-dark-200">Idle is observed, within limits.</span> InfraLens sees connections at the kernel
        (eBPF). A service is a candidate when nothing has connected to it for at least {quietAfter} <em>of time InfraLens
        was actually watching</em> - agent or backend downtime is never counted as silence - when it has no connections at
        all, when only other candidates reach it, or when it has not been seen at all. Monitoring scrapes and InfraLens
        itself do not count as use; anything still carrying traffic is never a candidate.
      </p>
      <p>
        <span className="text-dark-200">Cost is an attribution.</span> Agents report node-wide CPU/RAM only, so a
        service's figure is its node's configured price split evenly across the services observed on that node (the
        whole node when it is the only one, or when every service on it is a candidate). A service with its own price
        (IDLE_COST_SERVICE_PRICES) has it carved out of its node's price, never added on top. When agents report a
        cluster name, the node is unknown and only a low-confidence cluster-wide split is shown.
        {report.pricing.default_node_monthly != null && <> Default node: {formatMoney(sym, report.pricing.default_node_monthly)}/mo.</>}
        {report.pricing.node_rules.length > 0 && (
          <> Node rules: {report.pricing.node_rules.map(r => `${r.pattern} = ${formatMoney(sym, r.monthly)}`).join(', ')}.</>
        )}
      </p>
      <p>
        <span className="text-dark-200">Confidence</span> is the lower of two ratings: the idle evidence (high only after
        more than 31 observed days of silence, so a monthly job can never reach it - with the default 30-day retention the
        best possible is medium) and how direct the price is (an even split across busy neighbours is low).
      </p>
      <p>
        <span className="text-dark-200">Held back:</span> outbound-only workers and cron jobs (even with a metrics port),
        monitoring, possible replicas and standbys, anything with UDP traffic, servers reached by IP from another node or
        through a Kubernetes Service of the same name, brand-new services, anything in IDLE_COST_EXCLUDE, and - with
        history disabled - everything except services with no connections at all.
      </p>
      <p>
        <span className="text-dark-200">Blind spots:</span> a Kubernetes Service whose name differs from its workload's is
        not matched; inbound traffic through a hostPort or NodePort to a node IP, and receive-only UDP servers, are not
        seen as inbound. Treat every line as a candidate to verify with its owner, not a recommendation to delete.
      </p>
    </div>
  )
}
