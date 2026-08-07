import { Trash2 } from 'lucide-react'
import ServiceListPanel from './ServiceListPanel'
import { StaleService } from '../types'

function formatDaysAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  const days = Math.floor(ms / (1000 * 60 * 60 * 24))
  if (days <= 0) return 'today'
  if (days === 1) return '1 day ago'
  return `${days} days ago`
}

export default function DecommissionPanel() {
  return (
    <ServiceListPanel<StaleService>
      icon={<Trash2 size={16} />}
      label="Decommission candidates"
      tooltip="Services not seen recently - candidates for decommissioning"
      fetchUrl="/api/v1/topology/history/stale"
      emptyMessage="Nothing idle — everything's been seen recently."
      getId={svc => svc.id}
      getName={svc => svc.name || svc.id}
      renderSubtitle={svc => (
        <>last seen {formatDaysAgo(svc.last_seen)}{svc.node && <> · {svc.node}</>}</>
      )}
    />
  )
}
