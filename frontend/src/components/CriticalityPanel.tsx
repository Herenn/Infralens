import { Flame } from 'lucide-react'
import ServiceListPanel from './ServiceListPanel'
import { CriticalityEntry } from '../types'
import { isAddressLike, nameFromServiceId } from '../utils/hostnames'

// The best name for a ranked service, and what tells two alike apart: its
// node, or its address when it has none (external destinations).
function entryName(svc: CriticalityEntry): string {
  if (svc.hostname) return svc.hostname
  if (svc.name && !isAddressLike(svc.name) && svc.name !== 'external') return svc.name
  return nameFromServiceId(svc.id)
}

function entryContext(svc: CriticalityEntry): string {
  const where = svc.node || (svc.hostname || (svc.name && !isAddressLike(svc.name)) ? svc.id.replace(/\/.*$/, '') : 'external')
  return [svc.type && svc.type !== 'unknown' ? svc.type.replace('_', ' ') : null, where].filter(Boolean).join(' · ')
}

interface CriticalityPanelProps {
  onClose: () => void
  // One click shows that service's blast radius on the graph.
  onSelect: (id: string) => void
  selectedId: string | null
}

export default function CriticalityPanel({ onClose, onSelect, selectedId }: CriticalityPanelProps) {
  return (
    <ServiceListPanel<CriticalityEntry>
      id="critical"
      icon={<Flame size={16} />}
      tone="bg-rose-500/15 border-rose-500/30 text-rose-300"
      title="Most critical"
      subtitle="Ranked by blast radius: how many services depend on each, directly or not"
      fetchUrl="/api/v1/graph/criticality"
      emptyMessage="Not enough connections yet to rank criticality."
      onClose={onClose}
      hint="Click one to light up everything that breaks if it goes down."
      getId={svc => svc.id}
      getName={entryName}
      renderSubtitle={entryContext}
      renderAside={svc => (
        <span className="inline-flex flex-col items-end leading-none">
          <span className="text-sm font-semibold tabular-nums text-dark-100">{svc.blast_radius}</span>
          <span className="text-[10px] text-dark-500 mt-0.5">affected</span>
        </span>
      )}
      onSelect={svc => onSelect(svc.id)}
      selectedId={selectedId}
    />
  )
}
