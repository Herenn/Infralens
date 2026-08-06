import { Flame } from 'lucide-react'
import ServiceListPanel from './ServiceListPanel'
import { CriticalityEntry } from '../types'

export default function CriticalityPanel() {
  return (
    <ServiceListPanel<CriticalityEntry>
      icon={<Flame size={16} />}
      label="Most critical"
      tooltip="Ranked by blast radius - how many other services (transitively) depend on this one"
      fetchUrl="/api/v1/graph/criticality"
      emptyMessage="Not enough connections yet to rank criticality."
      getId={svc => svc.id}
      getName={svc => svc.name || svc.id}
      renderSubtitle={svc => (
        <>breaks {svc.blast_radius} service{svc.blast_radius === 1 ? '' : 's'} if it goes down</>
      )}
    />
  )
}
