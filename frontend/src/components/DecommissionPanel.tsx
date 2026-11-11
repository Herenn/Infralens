import { Trash2 } from 'lucide-react'
import ServiceListPanel from './ServiceListPanel'
import { StaleService } from '../types'
import { isAddressLike, nameFromServiceId } from '../utils/hostnames'

function formatDaysAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  const days = Math.floor(ms / (1000 * 60 * 60 * 24))
  if (days <= 0) return 'today'
  if (days === 1) return '1 day ago'
  return `${days} days ago`
}

export default function DecommissionPanel({ onClose }: { onClose: () => void }) {
  return (
    <ServiceListPanel<StaleService>
      id="stale"
      icon={<Trash2 size={16} />}
      tone="bg-dark-700/60 border-dark-600 text-dark-300"
      title="Decommission candidates"
      subtitle="Services not seen recently: no longer on the live map"
      fetchUrl="/api/v1/topology/history/stale"
      emptyMessage="Nothing idle: everything has been seen recently."
      onClose={onClose}
      getId={svc => svc.id}
      getName={svc => (svc.name && !isAddressLike(svc.name) ? svc.name : nameFromServiceId(svc.id))}
      renderSubtitle={svc => (
        <>last seen {formatDaysAgo(svc.last_seen)}{svc.node && <> · {svc.node}</>}</>
      )}
    />
  )
}
