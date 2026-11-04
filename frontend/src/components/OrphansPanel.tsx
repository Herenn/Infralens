import { Unlink } from 'lucide-react'
import ServiceListPanel from './ServiceListPanel'
import { Service } from '../types'

export default function OrphansPanel() {
  return (
    <ServiceListPanel<Service>
      icon={<Unlink size={16} />}
      label="Orphan services"
      tooltip="Services with no connections at all - misconfigured, leftover, or not yet observed talking to anything"
      fetchUrl="/api/v1/graph/orphans"
      emptyMessage="No orphans — every service has at least one connection."
      getId={svc => svc.id}
      getName={svc => svc.name || svc.id}
      renderSubtitle={svc => (
        <>{svc.type || svc.tech || 'no connections'}{svc.node && <> · {svc.node}</>}</>
      )}
    />
  )
}
