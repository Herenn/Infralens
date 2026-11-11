import { Unlink } from 'lucide-react'
import ServiceListPanel from './ServiceListPanel'
import { Service } from '../types'
import { serviceLabel } from '../utils/hostnames'

interface OrphansPanelProps {
  onClose: () => void
  // Shows the service on the graph.
  onSelect: (id: string) => void
  selectedId: string | null
}

export default function OrphansPanel({ onClose, onSelect, selectedId }: OrphansPanelProps) {
  return (
    <ServiceListPanel<Service>
      id="orphans"
      icon={<Unlink size={16} />}
      tone="bg-sky-500/15 border-sky-500/30 text-sky-300"
      title="Orphan services"
      subtitle="No connections at all: misconfigured, left over, or not yet seen talking"
      fetchUrl="/api/v1/graph/orphans"
      emptyMessage="No orphans: every service has at least one connection."
      onClose={onClose}
      getId={svc => svc.id}
      getName={serviceLabel}
      renderSubtitle={svc => (
        <>{svc.tech || svc.type || 'no connections'}{svc.node && <> · {svc.node}</>}{svc.pod_ip && <> · {svc.pod_ip}</>}</>
      )}
      onSelect={svc => onSelect(svc.id)}
      selectedId={selectedId}
    />
  )
}
