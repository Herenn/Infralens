import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import { 
  Server, 
  Database, 
  HardDrive,
  Globe,
  MessageSquare,
  Shield,
  Activity,
  Box,
  Cloud,
  Zap,
} from 'lucide-react'
import { Service, TypeColors } from '../types'
import { serviceHostnames } from '../utils/hostnames'

export interface ServiceNodeData {
  label: string
  service: Service
  incomingCount: number
  outgoingCount: number
  healthy: boolean
  ports?: number[] // Listening ports
}

interface ServiceNodeProps {
  data: ServiceNodeData
  selected?: boolean
}

// Get icon component based on service type
function getServiceIcon(type?: string, icon?: string) {
  // Check specific icons first
  switch (icon) {
    case 'postgresql':
    case 'mysql':
    case 'mongodb':
    case 'mariadb':
    case 'cassandra':
    case 'cockroachdb':
    case 'couchdb':
    case 'sqlserver':
    case 'oracle':
    case 'etcd':
      return Database
    case 'redis':
    case 'memcached':
    case 'dragonfly':
      return HardDrive
    case 'kafka':
    case 'rabbitmq':
    case 'nats':
    case 'activemq':
      return MessageSquare
    case 'nginx':
    case 'apache':
    case 'caddy':
    case 'http':
    case 'https':
      return Globe
    case 'traefik':
    case 'envoy':
    case 'haproxy':
    case 'kong':
      return Shield
    case 'prometheus':
    case 'grafana':
    case 'elasticsearch':
    case 'kibana':
    case 'jaeger':
    case 'zipkin':
    case 'loki':
    case 'otel':
      return Activity
    case 'kubernetes':
    case 'docker':
    case 'containerd':
      return Box
    case 'consul':
      return Cloud
  }

  // Fall back to type-based icons
  switch (type) {
    case 'database':
      return Database
    case 'cache':
      return HardDrive
    case 'message_queue':
      return MessageSquare
    case 'web_server':
      return Globe
    case 'proxy':
      return Shield
    case 'monitoring':
      return Activity
    case 'application':
      return Zap
    default:
      return Server
  }
}

// Get color based on service type
function getTypeColor(type?: string): string {
  return TypeColors[type || 'unknown'] || TypeColors.unknown
}

// Check if the service is a K8s resource
function isK8sResource(service: Service): 'pod' | 'service' | null {
  const resolved = service.display_name || service.resolved_name || ''
  if (resolved.startsWith('Pod:')) return 'pod'
  if (resolved.startsWith('Svc:')) return 'service'
  return null
}

// Get display info from K8s resolved name
function getK8sDisplayInfo(service: Service): { type: string; namespace: string; name: string } | null {
  const resolved = service.display_name || service.resolved_name || ''
  const match = resolved.match(/^(Pod|Svc):\s*([^/]+)\/(.+)$/)
  if (match) {
    return {
      type: match[1] === 'Pod' ? 'Pod' : 'Service',
      namespace: match[2],
      name: match[3]
    }
  }
  return null
}

// Handles sit exactly on the card's left and right edges with no size, so
// edges meet the card where utils/edgeRoute.ts expects them.
const HANDLE_CLASS = '!w-px !h-px !min-w-0 !min-h-0 !border-0 !bg-transparent !opacity-0'

function ServiceNode({ data, selected }: ServiceNodeProps) {
  const { label, service, healthy, ports } = data

  const k8sType = isK8sResource(service)
  const k8sInfo = getK8sDisplayInfo(service)
  const hostnames = serviceHostnames(service)

  // Get icon and color based on fingerprinted type
  const Icon = getServiceIcon(service.type, service.icon)
  const typeColor = getTypeColor(service.type)

  const title = k8sInfo ? k8sInfo.name : label
  const techText = service.tech || (k8sInfo ? k8sInfo.type : null)
  const techColor = service.type ? typeColor : (k8sType === 'pod' ? '#3b82f6' : '#8b5cf6')
  // The address is secondary: shown under the name, unless it is the name.
  const address = service.pod_ip && service.pod_ip !== title ? service.pod_ip : null
  const firstPort = ports && ports.length > 0 ? ports[0] : null
  const morePorts = (ports?.length || 0) - 1

  return (
    <div
      className={`il-card relative h-full w-full flex items-center gap-2.5 pl-2.5 pr-3 rounded-lg border transition-[border-color,box-shadow] duration-150
        bg-gradient-to-br from-dark-800 to-[#131c2e]
        ${selected
          ? 'border-lens-500 shadow-[0_0_0_3px_rgba(34,197,94,0.18)]'
          : healthy ? 'border-dark-600/90 hover:border-dark-400' : 'border-red-500/60'}
      `}
      title={hostnames.length > 1 ? `DNS: ${hostnames.join(', ')}` : undefined}
    >
      <Handle type="target" position={Position.Left} className={HANDLE_CLASS} isConnectable={false} />
      <Handle type="source" position={Position.Right} className={HANDLE_CLASS} isConnectable={false} />

      <div
        className="w-8 h-8 rounded-md flex items-center justify-center shrink-0"
        style={{ backgroundColor: `${typeColor}1f`, color: healthy ? typeColor : '#ef4444' }}
      >
        <Icon size={17} />
      </div>

      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-1.5 min-w-0">
          <h3 className="il-card-title font-semibold text-dark-100 truncate text-[13px] leading-[18px]">
            {title}
          </h3>
          {!k8sInfo && hostnames.length > 1 && (
            <span className="shrink-0 text-[10px] text-dark-400">+{hostnames.length - 1}</span>
          )}
        </div>
        <div className="flex items-center gap-1.5 min-w-0 text-[10.5px] leading-4 mt-px">
          {techText && (
            <span className="shrink-0 max-w-[90px] truncate font-medium" style={{ color: techColor }}>{techText}</span>
          )}
          {k8sInfo && <span className="truncate text-dark-400">ns/{k8sInfo.namespace}</span>}
          {!k8sInfo && (address || firstPort !== null) && (
            <span className="truncate font-mono text-dark-400">
              {address}{firstPort !== null && <span className="text-dark-300">:{firstPort}</span>}
              {morePorts > 0 && <span className="text-dark-500"> +{morePorts}</span>}
            </span>
          )}
        </div>
      </div>

      <span
        className={`absolute top-1.5 right-1.5 w-1.5 h-1.5 rounded-full ${healthy ? 'bg-lens-500' : 'bg-red-500'}`}
        title={healthy ? 'Healthy' : 'Unhealthy'}
      />
    </div>
  )
}

export default memo(ServiceNode)
