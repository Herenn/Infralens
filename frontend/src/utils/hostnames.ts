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

// An address rather than a name: "10.0.2.14", "10.0.3.10:5432", "::1",
// "[fd00::1]:443", or a bare ":443" (how an outbound destination is named
// when nothing better is known).
export function isAddressLike(value: string): boolean {
  const v = value.trim()
  if (v === '') return true
  if (/^:\d+$/.test(v)) return true
  if (/^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(v)) return true
  if (/^\[?[0-9a-f]*:[0-9a-f:.]*\]?(:\d+)?$/i.test(v)) return true
  return false
}

// Names the processor gives when it knows nothing: not worth a title.
const GENERIC_NAMES = new Set(['external', 'unknown'])

// The label a service is drawn and sorted by - the best name available:
// its DNS name, then a Kubernetes or service name, then the process name an
// agent reported. Agents name a host-side service by IP in display_name and
// by process in name, so an address in display_name never wins over a real
// name. Only when there is no name at all is the address the label.
export function serviceLabel(service: Service): string {
  const host = primaryHostname(service)
  if (host) return host
  for (const candidate of [service.display_name, service.name, service.inspection?.process_name]) {
    if (candidate && !isAddressLike(candidate) && !GENERIC_NAMES.has(candidate.toLowerCase())) return candidate
  }
  return service.pod_ip || service.display_name || service.name || service.id
}

// A readable name for a service known only by its ID (e.g. from a diff or
// an event for a service no longer on the graph): "10.0.2.14/inventory-sync"
// -> "inventory-sync"; "10.0.3.10:5432" stays as it is.
export function nameFromServiceId(id: string): string {
  const slash = id.indexOf('/')
  if (slash > 0 && isAddressLike(id.slice(0, slash))) return id.slice(slash + 1)
  return id
}

// Union of the DNS names of several services, for an aggregated node.
export function unionHostnames(services: Service[]): string[] {
  const seen = new Set<string>()
  for (const svc of services) {
    for (const h of serviceHostnames(svc)) seen.add(h)
  }
  return Array.from(seen)
}
