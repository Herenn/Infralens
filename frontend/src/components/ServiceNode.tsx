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
  ArrowDownToLine, 
  ArrowUpFromLine 
} from 'lucide-react'
import { Service, TypeColors } from '../types'

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

function ServiceNode({ data, selected }: ServiceNodeProps) {
  const { label, service, incomingCount, outgoingCount, healthy, ports } = data

  const k8sType = isK8sResource(service)
  const k8sInfo = getK8sDisplayInfo(service)
  
  // Get icon and color based on fingerprinted type
  const Icon = getServiceIcon(service.type, service.icon)
  const typeColor = getTypeColor(service.type)

  // Determine the badge text (tech or K8s type)
  const badgeText = service.tech || (k8sInfo ? k8sInfo.type : null)
  const badgeColor = service.type ? typeColor : (k8sType === 'pod' ? '#3b82f6' : '#8b5cf6')

  // Format ports for display (show first 2 max)
  const displayPorts = ports?.slice(0, 2) || []
  const hasMorePorts = (ports?.length || 0) > 2

  return (
    <div
      className={`
        relative px-3 py-2.5 rounded-xl border-2 transition-all duration-200
        bg-gradient-to-br from-dark-800 to-dark-900
        ${selected 
          ? 'border-lens-500 shadow-lg shadow-lens-500/20' 
          : 'border-dark-600 hover:border-dark-500'
        }
        ${!healthy ? 'border-red-500/50' : ''}
      `}
      style={{ minWidth: 160, maxWidth: 220 }}
    >
      {/* Connection handles */}
      <Handle
        type="target"
        position={Position.Left}
        className="!w-3 !h-3 !bg-dark-500 !border-2 !border-dark-400 hover:!bg-lens-500 transition-colors"
      />
      <Handle
        type="source"
        position={Position.Right}
        className="!w-3 !h-3 !bg-dark-500 !border-2 !border-dark-400 hover:!bg-lens-500 transition-colors"
      />

      {/* Status indicator */}
      <div 
        className={`absolute -top-1 -right-1 w-2.5 h-2.5 rounded-full ${
          healthy ? 'bg-lens-500 animate-pulse-slow' : 'bg-red-500'
        }`}
      />

      {/* Tech/Type badge */}
      {badgeText && (
        <div 
          className="absolute -top-2 left-3 px-1.5 py-0.5 text-[9px] font-semibold rounded text-white"
          style={{ backgroundColor: badgeColor }}
        >
          {badgeText}
        </div>
      )}

      {/* Content */}
      <div className="flex items-start gap-2 mt-1">
        <div 
          className="p-1.5 rounded-lg flex-shrink-0"
          style={{ 
            backgroundColor: `${typeColor}15`,
            color: healthy ? typeColor : '#ef4444'
          }}
        >
          <Icon size={18} />
        </div>
        
        <div className="flex-1 min-w-0">
          {/* Primary name */}
          <h3 className="font-medium text-dark-100 truncate text-xs leading-tight">
            {k8sInfo ? k8sInfo.name : label}
          </h3>

          {/* Namespace (if K8s) */}
          {k8sInfo && (
            <p className="text-[10px] text-dark-400 truncate">
              ns/{k8sInfo.namespace}
            </p>
          )}

          {/* IP address + Port badges */}
          <div className="flex items-center gap-1 mt-0.5 flex-wrap">
            {service.pod_ip && (
              <span className="text-[10px] text-dark-500 font-mono">
                {service.pod_ip}
              </span>
            )}
            {displayPorts.map(port => (
              <span 
                key={port}
                className="text-[9px] font-mono bg-slate-700 text-slate-300 px-1 py-0.5 rounded"
              >
                :{port}
              </span>
            ))}
            {hasMorePorts && (
              <span className="text-[9px] text-dark-500">+{(ports?.length || 0) - 2}</span>
            )}
          </div>
        </div>
      </div>

      {/* Connection counts */}
      <div className="flex items-center gap-2 mt-1.5 pt-1.5 border-t border-dark-700/50">
        <div className="flex items-center gap-1 text-[10px] text-dark-400">
          <ArrowDownToLine size={10} className="text-blue-400" />
          <span>{incomingCount}</span>
        </div>
        <div className="flex items-center gap-1 text-[10px] text-dark-400">
          <ArrowUpFromLine size={10} className="text-lens-400" />
          <span>{outgoingCount}</span>
        </div>
      </div>
    </div>
  )
}

export default memo(ServiceNode)
