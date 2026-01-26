import { X, Server, Globe, Clock, Tag, Hash, ArrowUpRight, ArrowDownLeft, Zap, Cpu, Database, HardDrive, MessageSquare, Shield, Activity as ActivityIcon } from 'lucide-react'
import { Service, Connection, formatBytes, TypeColors } from '../types'

// Format bytes/second to human readable rate
function formatRate(bytesPerSec: number): string {
  if (bytesPerSec === 0) return '0 B/s'
  const k = 1024
  const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const i = Math.floor(Math.log(bytesPerSec) / Math.log(k))
  const idx = Math.min(i, sizes.length - 1)
  return parseFloat((bytesPerSec / Math.pow(k, idx)).toFixed(1)) + ' ' + sizes[idx]
}

interface SidebarProps {
  service: Service | null
  connections: Connection[]
  onClose: () => void
}

export default function Sidebar({ service, connections, onClose }: SidebarProps) {
  if (!service) return null

  const formatTime = (isoString: string) => {
    const date = new Date(isoString)
    return date.toLocaleString()
  }

  // Find connections related to this service
  const outgoingConnections = connections.filter(c => c.source_id === service.id)
  const incomingConnections = connections.filter(c => c.target_id === service.id)

  // Calculate total throughput rates
  const totalSentRate = outgoingConnections.reduce((sum, c) => sum + (c.bytes_sent_rate || 0), 0)
  const totalRecvRate = incomingConnections.reduce((sum, c) => sum + (c.bytes_recv_rate || 0), 0)
  const totalBytesSent = outgoingConnections.reduce((sum, c) => sum + (c.bytes_sent || 0), 0)
  const totalBytesRecv = incomingConnections.reduce((sum, c) => sum + (c.bytes_recv || 0), 0)

  return (
    <div className="w-80 border-l border-dark-800 bg-dark-900/50 backdrop-blur-sm flex flex-col animate-in slide-in-from-right-4 duration-200">
      {/* Header */}
      <div className="h-14 px-4 flex items-center justify-between border-b border-dark-800">
        <h2 className="font-medium text-dark-200">Service Details</h2>
        <button
          onClick={onClose}
          className="p-1.5 rounded-lg hover:bg-dark-800 text-dark-400 hover:text-dark-200 transition-colors"
        >
          <X size={18} />
        </button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-4 space-y-6">
        {/* Service Name */}
        <div className="flex items-start gap-3">
          <div 
            className="p-2 rounded-lg"
            style={{ 
              backgroundColor: `${TypeColors[service.type || 'unknown'] || TypeColors.unknown}15`,
              color: TypeColors[service.type || 'unknown'] || TypeColors.unknown
            }}
          >
            <ServiceTypeIcon type={service.type} />
          </div>
          <div>
            <h3 className="font-semibold text-dark-100">{service.name}</h3>
            {service.tech && (
              <p 
                className="text-sm font-medium"
                style={{ color: TypeColors[service.type || 'unknown'] || TypeColors.unknown }}
              >
                {service.tech}
              </p>
            )}
            <p className="text-xs text-dark-500 font-mono">{service.id}</p>
          </div>
        </div>

        {/* Throughput Summary */}
        {(totalSentRate > 0 || totalRecvRate > 0 || totalBytesSent > 0 || totalBytesRecv > 0) && (
          <div className="bg-dark-800/50 rounded-lg p-4">
            <div className="flex items-center gap-2 mb-3">
              <Zap size={16} className="text-yellow-400" />
              <span className="text-sm font-medium text-dark-300">Throughput</span>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="bg-dark-900/50 rounded-lg p-3">
                <div className="flex items-center gap-1.5 text-blue-400 mb-1">
                  <ArrowUpRight size={14} />
                  <span className="text-xs uppercase tracking-wide">Upload</span>
                </div>
                <p className="text-lg font-semibold text-dark-100">{formatRate(totalSentRate)}</p>
                <p className="text-xs text-dark-500">Total: {formatBytes(totalBytesSent)}</p>
              </div>
              <div className="bg-dark-900/50 rounded-lg p-3">
                <div className="flex items-center gap-1.5 text-green-400 mb-1">
                  <ArrowDownLeft size={14} />
                  <span className="text-xs uppercase tracking-wide">Download</span>
                </div>
                <p className="text-lg font-semibold text-dark-100">{formatRate(totalRecvRate)}</p>
                <p className="text-xs text-dark-500">Total: {formatBytes(totalBytesRecv)}</p>
              </div>
            </div>
          </div>
        )}

        {/* Details */}
        <div className="space-y-4">
          {service.pod_ip && (
            <DetailRow 
              icon={<Globe size={16} />}
              label="Pod IP"
              value={service.pod_ip}
              mono
            />
          )}

          {service.node && (
            <DetailRow 
              icon={<Server size={16} />}
              label="Node"
              value={service.node}
            />
          )}

          {service.namespace && (
            <DetailRow 
              icon={<Hash size={16} />}
              label="Namespace"
              value={service.namespace}
            />
          )}

          <DetailRow 
            icon={<Clock size={16} />}
            label="Last Seen"
            value={formatTime(service.last_seen)}
          />
        </div>

        {/* Outgoing Connections */}
        {outgoingConnections.length > 0 && (
          <div>
            <div className="flex items-center gap-2 mb-3">
              <ArrowUpRight size={16} className="text-blue-400" />
              <span className="text-sm font-medium text-dark-300">
                Outgoing ({outgoingConnections.length})
              </span>
            </div>
            <div className="space-y-2">
              {outgoingConnections.map(conn => (
                <ConnectionCard key={conn.id} connection={conn} direction="outgoing" />
              ))}
            </div>
          </div>
        )}

        {/* Incoming Connections */}
        {incomingConnections.length > 0 && (
          <div>
            <div className="flex items-center gap-2 mb-3">
              <ArrowDownLeft size={16} className="text-green-400" />
              <span className="text-sm font-medium text-dark-300">
                Incoming ({incomingConnections.length})
              </span>
            </div>
            <div className="space-y-2">
              {incomingConnections.map(conn => (
                <ConnectionCard key={conn.id} connection={conn} direction="incoming" />
              ))}
            </div>
          </div>
        )}

        {/* Labels */}
        {service.labels && Object.keys(service.labels).length > 0 && (
          <div>
            <div className="flex items-center gap-2 mb-3">
              <Tag size={16} className="text-dark-400" />
              <span className="text-sm font-medium text-dark-300">Labels</span>
            </div>
            <div className="space-y-2">
              {Object.entries(service.labels).map(([key, value]) => (
                <div 
                  key={key}
                  className="flex items-center gap-2 text-xs bg-dark-800 rounded-lg px-3 py-2"
                >
                  <span className="text-dark-400 truncate">{key}:</span>
                  <span className="text-dark-200 font-mono truncate">{value}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

interface DetailRowProps {
  icon: React.ReactNode
  label: string
  value: string
  mono?: boolean
}

function DetailRow({ icon, label, value, mono }: DetailRowProps) {
  return (
    <div className="flex items-start gap-3">
      <div className="text-dark-500 mt-0.5">{icon}</div>
      <div className="flex-1 min-w-0">
        <p className="text-xs text-dark-500 uppercase tracking-wide">{label}</p>
        <p className={`text-sm text-dark-200 truncate ${mono ? 'font-mono' : ''}`}>
          {value}
        </p>
      </div>
    </div>
  )
}

interface ConnectionCardProps {
  connection: Connection
  direction: 'incoming' | 'outgoing'
}

// Helper component for service type icons
function ServiceTypeIcon({ type }: { type?: string }) {
  switch (type) {
    case 'database':
      return <Database size={20} />
    case 'cache':
      return <HardDrive size={20} />
    case 'message_queue':
      return <MessageSquare size={20} />
    case 'web_server':
      return <Globe size={20} />
    case 'proxy':
      return <Shield size={20} />
    case 'monitoring':
      return <ActivityIcon size={20} />
    case 'application':
      return <Cpu size={20} />
    default:
      return <Server size={20} />
  }
}

function ConnectionCard({ connection, direction }: ConnectionCardProps) {
  const hasRate = (connection.bytes_sent_rate && connection.bytes_sent_rate > 0) || 
                  (connection.bytes_recv_rate && connection.bytes_recv_rate > 0)

  return (
    <div className="bg-dark-800 rounded-lg px-3 py-2 text-xs">
      <div className="flex items-center justify-between mb-1">
        <span className="text-dark-300 font-mono truncate">
          {direction === 'outgoing' ? connection.target_id : connection.source_id}
        </span>
        <span className="text-dark-500">:{connection.port}</span>
      </div>
      {hasRate && (
        <div className="flex items-center gap-3 text-dark-400">
          {connection.bytes_sent_rate !== undefined && connection.bytes_sent_rate > 0 && (
            <span className="flex items-center gap-1">
              <ArrowUpRight size={10} className="text-blue-400" />
              {formatRate(connection.bytes_sent_rate)}
            </span>
          )}
          {connection.bytes_recv_rate !== undefined && connection.bytes_recv_rate > 0 && (
            <span className="flex items-center gap-1">
              <ArrowDownLeft size={10} className="text-green-400" />
              {formatRate(connection.bytes_recv_rate)}
            </span>
          )}
        </div>
      )}
    </div>
  )
}
