import { Service } from '../types'

// DNS names for external destinations.
//
// The backend reports, per service, the DNS names its callers resolved its
// address from (observed by the agent in DNS answers, not reverse lookups).
// Only address-keyed services ("52.1.2.3:443") ever carry them; services the
// Kubernetes watcher resolved already have a better name and keep it.

function isK8sResolved(service: Service): boolean {
  const resolved = service.display_name || service.resolved_name || ''
  return /^(Deploy|STS|DS|Job|App|Pod|Svc|RS):/.test(resolved)
}

// All DNS names for a service, primary first. Empty when none was observed.
export function serviceHostnames(service: Service): string[] {
  // An aggregated "HTTPS (5)" node is not one destination: its hostnames are
  // kept for search only, never used as its label.
  if (isK8sResolved(service) || service.aggregated_count) return []
  if (service.hostnames && service.hostnames.length > 0) return service.hostnames
  return service.hostname ? [service.hostname] : []
}

// The primary DNS name, if any.
export function primaryHostname(service: Service): string | undefined {
  return serviceHostnames(service)[0]
}

// The label a service is drawn and sorted by: its DNS name when it has one,
// otherwise the existing display name / name / ID.
export function serviceLabel(service: Service): string {
  return primaryHostname(service) || service.display_name || service.name || service.id
}

// Union of the DNS names of several services, for an aggregated node.
export function unionHostnames(services: Service[]): string[] {
  const seen = new Set<string>()
  for (const svc of services) {
    for (const h of serviceHostnames(svc)) seen.add(h)
  }
  return Array.from(seen)
}
