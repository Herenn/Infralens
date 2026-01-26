import { useState, useEffect } from 'react'
import { 
  X, 
  Server, 
  Globe, 
  Clock, 
  Hash, 
  ArrowUpRight, 
  ArrowDownLeft, 
  Zap, 
  Cpu, 
  Database, 
  HardDrive, 
  MessageSquare, 
  Shield, 
  Activity as ActivityIcon,
  Sparkles,
  MemoryStick,
  Network,
  FileCode,
  BrainCircuit,
  Loader2,
  Terminal,
  Package,
  FileText,
  Folder,
  ExternalLink,
  CheckCircle2,
  XCircle,
  Settings
} from 'lucide-react'
import { Service, Connection, ServiceInspection, formatBytes, TypeColors } from '../types'
import MarkdownRenderer from './MarkdownRenderer'

// Format bytes/second to human readable rate
function formatRate(bytesPerSec: number): string {
  if (bytesPerSec === 0) return '0 B/s'
  const k = 1024
  const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const i = Math.floor(Math.log(bytesPerSec) / Math.log(k))
  const idx = Math.min(i, sizes.length - 1)
  return parseFloat((bytesPerSec / Math.pow(k, idx)).toFixed(1)) + ' ' + sizes[idx]
}

// Get usage color class
function getUsageColor(percent: number | undefined): string {
  if (percent === undefined) return 'bg-slate-600'
  if (percent < 60) return 'bg-emerald-500'
  if (percent < 85) return 'bg-amber-500'
  return 'bg-red-500'
}

function getUsageTextColor(percent: number | undefined): string {
  if (percent === undefined) return 'text-slate-500'
  if (percent < 60) return 'text-emerald-400'
  if (percent < 85) return 'text-amber-400'
  return 'text-red-400'
}

// Props for the drawer
interface ServiceDrawerProps {
  isOpen: boolean
  onClose: () => void
  // Service node data
  service?: Service | null
  connections?: Connection[]
  ports?: number[]
  // Server node data
  serverData?: {
    serverName: string
    serviceCount: number
    totalConnections: number
    cpuPercent?: number
    memPercent?: number
    memUsed?: number
    memTotal?: number
  } | null
  // Node type
  nodeType: 'service' | 'server' | null
}

type TabType = 'overview' | 'ai'

export default function ServiceDrawer({ 
  isOpen, 
  onClose, 
  service, 
  connections = [],
  ports = [],
  serverData,
  nodeType
}: ServiceDrawerProps) {
  const [activeTab, setActiveTab] = useState<TabType>('overview')
  const [isAnimating, setIsAnimating] = useState(false)

  // Handle animation states
  useEffect(() => {
    if (isOpen) {
      setIsAnimating(true)
    }
  }, [isOpen])

  // Reset tab when drawer opens
  useEffect(() => {
    if (isOpen) {
      setActiveTab('overview')
    }
  }, [isOpen, service?.id, serverData?.serverName])

  const handleTransitionEnd = () => {
    if (!isOpen) {
      setIsAnimating(false)
    }
  }

  // Don't render if not open and not animating - MUST be after all hooks
  if (!isOpen && !isAnimating) return null

  const formatTime = (isoString: string) => {
    const date = new Date(isoString)
    return date.toLocaleString()
  }

  // Find connections related to this service
  const outgoingConnections = service ? connections.filter(c => c.source_id === service.id) : []
  const incomingConnections = service ? connections.filter(c => c.target_id === service.id) : []

  // Calculate total throughput rates
  const totalSentRate = outgoingConnections.reduce((sum, c) => sum + (c.bytes_sent_rate || 0), 0)
  const totalRecvRate = incomingConnections.reduce((sum, c) => sum + (c.bytes_recv_rate || 0), 0)

  return (
    <>
      {/* Backdrop */}
      <div 
        className={`
          fixed inset-0 bg-black/40 backdrop-blur-sm z-40
          transition-opacity duration-300
          ${isOpen ? 'opacity-100' : 'opacity-0 pointer-events-none'}
        `}
        onClick={onClose}
      />

      {/* Drawer Panel */}
      <div 
        className={`
          fixed top-0 right-0 h-full w-[420px] z-50
          bg-slate-900/95 backdrop-blur-xl border-l border-slate-700/50
          shadow-2xl shadow-black/50
          transform transition-transform duration-300 ease-out
          ${isOpen ? 'translate-x-0' : 'translate-x-full'}
        `}
        onTransitionEnd={handleTransitionEnd}
      >
        {/* Header */}
        <div className="h-16 px-5 flex items-center justify-between border-b border-slate-700/50">
          <div className="flex items-center gap-3">
            {nodeType === 'server' ? (
              <>
                <div className="p-2 rounded-lg bg-slate-800 border border-slate-700">
                  <Server size={18} className="text-slate-400" />
                </div>
                <div>
                  <h2 className="font-semibold text-slate-100">{serverData?.serverName}</h2>
                  <p className="text-xs text-slate-500">Infrastructure Node</p>
                </div>
              </>
            ) : service ? (
              <>
                <div 
                  className="p-2 rounded-lg"
                  style={{ 
                    backgroundColor: `${TypeColors[service.type || 'unknown'] || TypeColors.unknown}15`,
                    color: TypeColors[service.type || 'unknown'] || TypeColors.unknown
                  }}
                >
                  <ServiceTypeIcon type={service.type} size={18} />
                </div>
                <div>
                  <h2 className="font-semibold text-slate-100">{service.name}</h2>
                  <div className="flex items-center gap-2">
                    {service.tech && (
                      <span 
                        className="text-xs font-medium"
                        style={{ color: TypeColors[service.type || 'unknown'] || TypeColors.unknown }}
                      >
                        {service.tech}
                      </span>
                    )}
                    <span className={`w-2 h-2 rounded-full ${service.healthy !== false ? 'bg-emerald-500' : 'bg-red-500'}`} />
                    <span className="text-xs text-slate-500">
                      {service.healthy !== false ? 'Online' : 'Offline'}
                    </span>
                  </div>
                </div>
              </>
            ) : null}
          </div>
          <button
            onClick={onClose}
            className="p-2 rounded-lg hover:bg-slate-800 text-slate-400 hover:text-slate-200 transition-colors"
          >
            <X size={18} />
          </button>
        </div>

        {/* Tabs */}
        <div className="flex border-b border-slate-700/50">
          <TabButton 
            active={activeTab === 'overview'} 
            onClick={() => setActiveTab('overview')}
            icon={<ActivityIcon size={14} />}
            label="Overview"
          />
          <TabButton 
            active={activeTab === 'ai'} 
            onClick={() => setActiveTab('ai')}
            icon={<Sparkles size={14} />}
            label="AI Docs"
            badge="Beta"
          />
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto h-[calc(100%-120px)]">
          {activeTab === 'overview' ? (
            nodeType === 'server' ? (
              <ServerOverview serverData={serverData} />
            ) : (
              <ServiceOverview 
                service={service}
                connections={connections}
                ports={ports}
                outgoingConnections={outgoingConnections}
                incomingConnections={incomingConnections}
                totalSentRate={totalSentRate}
                totalRecvRate={totalRecvRate}
                formatTime={formatTime}
              />
            )
          ) : (
            <AIDocsTab service={service} serverData={serverData} nodeType={nodeType} />
          )}
        </div>
      </div>
    </>
  )
}

// Tab Button Component
function TabButton({ 
  active, 
  onClick, 
  icon, 
  label, 
  badge 
}: { 
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
  badge?: string
}) {
  return (
    <button
      onClick={onClick}
      className={`
        flex-1 flex items-center justify-center gap-2 px-4 py-3 text-sm font-medium
        transition-colors border-b-2 -mb-[1px]
        ${active 
          ? 'text-slate-100 border-blue-500 bg-slate-800/30' 
          : 'text-slate-500 border-transparent hover:text-slate-300 hover:bg-slate-800/20'
        }
      `}
    >
      {icon}
      {label}
      {badge && (
        <span className="text-[9px] px-1.5 py-0.5 rounded bg-blue-500/20 text-blue-400 font-semibold">
          {badge}
        </span>
      )}
    </button>
  )
}

// Server Overview Tab
function ServerOverview({ serverData }: { serverData?: ServiceDrawerProps['serverData'] }) {
  if (!serverData) return null

  return (
    <div className="p-5 space-y-6">
      {/* Resource Usage */}
      <div className="space-y-4">
        <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider">
          Resource Usage
        </h3>
        
        {/* CPU */}
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <Cpu size={16} className="text-blue-400" />
              <span className="text-sm font-medium text-slate-300">CPU Usage</span>
            </div>
            <span className={`text-lg font-mono font-bold ${getUsageTextColor(serverData.cpuPercent)}`}>
              {serverData.cpuPercent !== undefined ? `${serverData.cpuPercent.toFixed(1)}%` : '--'}
            </span>
          </div>
          <div className="h-3 bg-slate-700/50 rounded-full overflow-hidden">
            <div 
              className={`h-full rounded-full transition-all duration-500 ${getUsageColor(serverData.cpuPercent)}`}
              style={{ width: `${Math.min(serverData.cpuPercent ?? 0, 100)}%` }}
            />
          </div>
        </div>

        {/* Memory */}
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <MemoryStick size={16} className="text-purple-400" />
              <span className="text-sm font-medium text-slate-300">Memory Usage</span>
            </div>
            <div className="text-right">
              <span className={`text-lg font-mono font-bold ${getUsageTextColor(serverData.memPercent)}`}>
                {serverData.memPercent !== undefined ? `${serverData.memPercent.toFixed(1)}%` : '--'}
              </span>
              {serverData.memUsed !== undefined && serverData.memTotal !== undefined && (
                <p className="text-xs text-slate-500">
                  {formatBytes(serverData.memUsed)} / {formatBytes(serverData.memTotal)}
                </p>
              )}
            </div>
          </div>
          <div className="h-3 bg-slate-700/50 rounded-full overflow-hidden">
            <div 
              className={`h-full rounded-full transition-all duration-500 ${getUsageColor(serverData.memPercent)}`}
              style={{ width: `${Math.min(serverData.memPercent ?? 0, 100)}%` }}
            />
          </div>
        </div>
      </div>

      {/* Statistics */}
      <div className="space-y-4">
        <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider">
          Statistics
        </h3>
        <div className="grid grid-cols-2 gap-3">
          <StatCard 
            icon={<Server size={16} className="text-cyan-400" />}
            label="Services"
            value={serverData.serviceCount}
          />
          <StatCard 
            icon={<Network size={16} className="text-green-400" />}
            label="Connections"
            value={serverData.totalConnections}
          />
        </div>
      </div>
    </div>
  )
}

// Service Overview Tab
function ServiceOverview({ 
  service, 
  connections: _connections,
  ports,
  outgoingConnections,
  incomingConnections,
  totalSentRate,
  totalRecvRate,
  formatTime
}: {
  service?: Service | null
  connections: Connection[]
  ports: number[]
  outgoingConnections: Connection[]
  incomingConnections: Connection[]
  totalSentRate: number
  totalRecvRate: number
  formatTime: (s: string) => string
}) {
  if (!service) return null

  const totalBytesSent = outgoingConnections.reduce((sum, c) => sum + (c.bytes_sent || 0), 0)
  const totalBytesRecv = incomingConnections.reduce((sum, c) => sum + (c.bytes_recv || 0), 0)

  return (
    <div className="p-5 space-y-6">
      {/* Listening Ports */}
      {ports.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {ports.map(port => (
            <span 
              key={port}
              className="text-xs font-mono bg-slate-800 text-slate-300 px-2.5 py-1 rounded-lg border border-slate-700"
            >
              :{port}
            </span>
          ))}
        </div>
      )}

      {/* Throughput */}
      {(totalSentRate > 0 || totalRecvRate > 0 || totalBytesSent > 0 || totalBytesRecv > 0) && (
        <div className="space-y-3">
          <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider flex items-center gap-2">
            <Zap size={14} className="text-yellow-400" />
            Throughput
          </h3>
          <div className="grid grid-cols-2 gap-3">
            <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
              <div className="flex items-center gap-1.5 text-blue-400 mb-2">
                <ArrowUpRight size={14} />
                <span className="text-xs uppercase tracking-wide font-medium">Upload</span>
              </div>
              <p className="text-xl font-bold text-slate-100">{formatRate(totalSentRate)}</p>
              <p className="text-xs text-slate-500 mt-1">Total: {formatBytes(totalBytesSent)}</p>
            </div>
            <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
              <div className="flex items-center gap-1.5 text-green-400 mb-2">
                <ArrowDownLeft size={14} />
                <span className="text-xs uppercase tracking-wide font-medium">Download</span>
              </div>
              <p className="text-xl font-bold text-slate-100">{formatRate(totalRecvRate)}</p>
              <p className="text-xs text-slate-500 mt-1">Total: {formatBytes(totalBytesRecv)}</p>
            </div>
          </div>
        </div>
      )}

      {/* Details */}
      <div className="space-y-4">
        <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider">
          Details
        </h3>
        <div className="space-y-3">
          {service.pod_ip && (
            <DetailRow icon={<Globe size={14} />} label="IP Address" value={service.pod_ip} mono />
          )}
          {service.node && (
            <DetailRow icon={<Server size={14} />} label="Node" value={service.node} />
          )}
          {service.namespace && (
            <DetailRow icon={<Hash size={14} />} label="Namespace" value={service.namespace} />
          )}
          {service.type && (
            <DetailRow icon={<ServiceTypeIcon type={service.type} size={14} />} label="Type" value={service.type} />
          )}
          <DetailRow icon={<Clock size={14} />} label="Last Seen" value={formatTime(service.last_seen)} />
        </div>
      </div>

      {/* Connections */}
      {(outgoingConnections.length > 0 || incomingConnections.length > 0) && (
        <div className="space-y-4">
          <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider">
            Connections
          </h3>
          
          {outgoingConnections.length > 0 && (
            <div>
              <div className="flex items-center gap-2 mb-2">
                <ArrowUpRight size={12} className="text-blue-400" />
                <span className="text-xs text-slate-400">Outgoing ({outgoingConnections.length})</span>
              </div>
              <div className="space-y-2">
                {outgoingConnections.slice(0, 5).map(conn => (
                  <ConnectionCard key={conn.id} connection={conn} direction="outgoing" />
                ))}
                {outgoingConnections.length > 5 && (
                  <p className="text-xs text-slate-500 text-center">
                    +{outgoingConnections.length - 5} more
                  </p>
                )}
              </div>
            </div>
          )}

          {incomingConnections.length > 0 && (
            <div>
              <div className="flex items-center gap-2 mb-2">
                <ArrowDownLeft size={12} className="text-green-400" />
                <span className="text-xs text-slate-400">Incoming ({incomingConnections.length})</span>
              </div>
              <div className="space-y-2">
                {incomingConnections.slice(0, 5).map(conn => (
                  <ConnectionCard key={conn.id} connection={conn} direction="incoming" />
                ))}
                {incomingConnections.length > 5 && (
                  <p className="text-xs text-slate-500 text-center">
                    +{incomingConnections.length - 5} more
                  </p>
                )}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Deep Inspection Data */}
      {service.inspection && (
        <InspectionSection inspection={service.inspection} />
      )}
    </div>
  )
}

// Deep Inspection Section
function InspectionSection({ inspection }: { inspection: ServiceInspection }) {
  const hasHttpInfo = inspection.http_info && (
    inspection.http_info.server_header || 
    inspection.http_info.endpoints?.length
  )
  const hasDbInfo = inspection.db_info?.type
  const hasDeps = inspection.dependencies && inspection.dependencies.length > 0
  const hasConfigs = inspection.config_files && inspection.config_files.length > 0
  const hasEnvVars = inspection.env_var_names && inspection.env_var_names.length > 0

  return (
    <div className="space-y-4">
      <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider flex items-center gap-2">
        <Terminal size={14} className="text-purple-400" />
        Deep Inspection
      </h3>

      {/* Process Info */}
      {(inspection.process_name || inspection.command_line?.length) && (
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center gap-2 mb-2">
            <Settings size={14} className="text-slate-400" />
            <span className="text-xs text-slate-400 uppercase tracking-wide">Process</span>
          </div>
          {inspection.process_name && (
            <p className="text-sm text-slate-200 font-mono">{inspection.process_name}</p>
          )}
          {inspection.command_line && inspection.command_line.length > 0 && (
            <p className="text-xs text-slate-500 font-mono mt-1 truncate" title={inspection.command_line.join(' ')}>
              {inspection.command_line.join(' ')}
            </p>
          )}
          {inspection.working_dir && (
            <div className="flex items-center gap-1 mt-2 text-xs text-slate-500">
              <Folder size={10} />
              <span className="font-mono truncate">{inspection.working_dir}</span>
            </div>
          )}
        </div>
      )}

      {/* HTTP Endpoints */}
      {hasHttpInfo && inspection.http_info && (
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center gap-2 mb-3">
            <Globe size={14} className="text-green-400" />
            <span className="text-xs text-slate-400 uppercase tracking-wide">HTTP Endpoints</span>
          </div>
          
          {inspection.http_info.server_header && (
            <div className="flex items-center gap-2 mb-2">
              <span className="text-xs text-slate-500">Server:</span>
              <span className="text-xs text-slate-300 font-mono bg-slate-700/50 px-2 py-0.5 rounded">
                {inspection.http_info.server_header}
              </span>
            </div>
          )}
          
          {inspection.http_info.health_endpoint && (
            <div className="flex items-center gap-2 mb-2">
              {inspection.http_info.health_status === 200 ? (
                <CheckCircle2 size={12} className="text-green-400" />
              ) : (
                <XCircle size={12} className="text-red-400" />
              )}
              <span className="text-xs font-mono text-slate-300">
                {inspection.http_info.health_endpoint}
              </span>
              <span className="text-xs text-slate-500">
                ({inspection.http_info.health_status})
              </span>
            </div>
          )}
          
          {inspection.http_info.endpoints && inspection.http_info.endpoints.length > 0 && (
            <div className="flex flex-wrap gap-1.5 mt-2">
              {inspection.http_info.endpoints.map((ep, i) => (
                <span 
                  key={i} 
                  className="text-xs font-mono bg-slate-700/50 text-slate-300 px-2 py-0.5 rounded flex items-center gap-1"
                >
                  <ExternalLink size={10} className="text-slate-500" />
                  {ep}
                </span>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Database Info */}
      {hasDbInfo && inspection.db_info && (
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center gap-2 mb-2">
            <Database size={14} className="text-blue-400" />
            <span className="text-xs text-slate-400 uppercase tracking-wide">Database</span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-sm text-slate-200 capitalize font-medium">
              {inspection.db_info.type}
            </span>
            {inspection.db_info.version && (
              <span className="text-xs text-slate-500 font-mono bg-slate-700/50 px-2 py-0.5 rounded">
                {inspection.db_info.version}
              </span>
            )}
          </div>
          {inspection.db_info.databases && inspection.db_info.databases.length > 0 && (
            <div className="mt-2">
              <span className="text-xs text-slate-500">Databases:</span>
              <div className="flex flex-wrap gap-1 mt-1">
                {inspection.db_info.databases.map((db, i) => (
                  <span key={i} className="text-xs font-mono bg-blue-500/10 text-blue-400 px-2 py-0.5 rounded">
                    {db}
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Dependencies */}
      {hasDeps && inspection.dependencies && (
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <Package size={14} className="text-amber-400" />
              <span className="text-xs text-slate-400 uppercase tracking-wide">Dependencies</span>
            </div>
            <span className="text-xs text-slate-500">{inspection.dependencies.length} packages</span>
          </div>
          <div className="flex flex-wrap gap-1.5">
            {inspection.dependencies.slice(0, 12).map((dep, i) => (
              <span 
                key={i} 
                className="text-xs bg-slate-700/50 text-slate-300 px-2 py-0.5 rounded"
                title={dep.version ? `${dep.name}@${dep.version}` : dep.name}
              >
                {dep.name}
                {dep.version && <span className="text-slate-500 ml-1">@{dep.version}</span>}
              </span>
            ))}
            {inspection.dependencies.length > 12 && (
              <span className="text-xs text-slate-500 px-2 py-0.5">
                +{inspection.dependencies.length - 12} more
              </span>
            )}
          </div>
        </div>
      )}

      {/* Config Files */}
      {hasConfigs && inspection.config_files && (
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <FileText size={14} className="text-cyan-400" />
              <span className="text-xs text-slate-400 uppercase tracking-wide">Config Files</span>
            </div>
            <span className="text-xs text-slate-500">{inspection.config_files.length} files</span>
          </div>
          <div className="space-y-1">
            {inspection.config_files.slice(0, 6).map((file, i) => (
              <div key={i} className="flex items-center gap-2 text-xs">
                <FileCode size={12} className="text-slate-500" />
                <span className="font-mono text-slate-300 truncate">{file}</span>
              </div>
            ))}
            {inspection.config_files.length > 6 && (
              <span className="text-xs text-slate-500">
                +{inspection.config_files.length - 6} more files
              </span>
            )}
          </div>
        </div>
      )}

      {/* Environment Variables */}
      {hasEnvVars && inspection.env_var_names && (
        <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <Terminal size={14} className="text-orange-400" />
              <span className="text-xs text-slate-400 uppercase tracking-wide">Environment</span>
            </div>
            <span className="text-xs text-slate-500">{inspection.env_var_names.length} vars</span>
          </div>
          <div className="flex flex-wrap gap-1.5">
            {inspection.env_var_names.slice(0, 10).map((env, i) => (
              <span key={i} className="text-xs font-mono bg-slate-700/50 text-slate-400 px-2 py-0.5 rounded">
                {env}
              </span>
            ))}
            {inspection.env_var_names.length > 10 && (
              <span className="text-xs text-slate-500 px-2 py-0.5">
                +{inspection.env_var_names.length - 10} more
              </span>
            )}
          </div>
        </div>
      )}

      {/* Inspection timestamp */}
      {inspection.inspected_at && (
        <div className="text-xs text-slate-600 text-center">
          Inspected {new Date(inspection.inspected_at).toLocaleString()}
        </div>
      )}
    </div>
  )
}

// Cache key prefix for localStorage
const AI_DOCS_CACHE_PREFIX = 'infralens_ai_docs_'
const AI_DOCS_CACHE_TTL = 24 * 60 * 60 * 1000 // 24 hours

// Get cached docs from localStorage
function getCachedDocs(serviceId: string): { content: string; provider: string; timestamp: number } | null {
  try {
    const cached = localStorage.getItem(AI_DOCS_CACHE_PREFIX + serviceId)
    if (!cached) return null
    const parsed = JSON.parse(cached)
    // Check if cache is still valid
    if (Date.now() - parsed.timestamp < AI_DOCS_CACHE_TTL) {
      return parsed
    }
    // Cache expired, remove it
    localStorage.removeItem(AI_DOCS_CACHE_PREFIX + serviceId)
    return null
  } catch {
    return null
  }
}

// Save docs to localStorage cache
function cacheDocs(serviceId: string, content: string, provider: string) {
  try {
    localStorage.setItem(AI_DOCS_CACHE_PREFIX + serviceId, JSON.stringify({
      content,
      provider,
      timestamp: Date.now()
    }))
  } catch {
    // localStorage might be full, ignore
  }
}

// AI Docs Tab
function AIDocsTab({ 
  service, 
  serverData: _serverData, 
  nodeType 
}: { 
  service?: Service | null
  serverData?: ServiceDrawerProps['serverData']
  nodeType: 'service' | 'server' | null
}) {
  const [aiStatus, setAiStatus] = useState<{ enabled: boolean; providers: Record<string, boolean> } | null>(null)
  const [_loading, setLoading] = useState(false)
  const [generating, setGenerating] = useState(false)
  const [docs, setDocs] = useState<string | null>(null)
  const [provider, setProvider] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [question, setQuestion] = useState('')
  const [answer, setAnswer] = useState<string | null>(null)
  const [showSettings, setShowSettings] = useState(false)
  const [fromCache, setFromCache] = useState(false)
  const [config, setConfig] = useState({
    openai_api_key: '',
    anthropic_api_key: '',
    gemini_api_key: '',
    ollama_url: 'http://localhost:11434',
    default_provider: 'openai'
  })
  const [savingConfig, setSavingConfig] = useState(false)

  // Name for display (used in AI prompts internally)
  // const name = nodeType === 'server' ? serverData?.serverName : service?.name
  const serviceId = service?.id

  // Fetch AI status on mount
  useEffect(() => {
    fetchAIStatus()
  }, [])

  // Load cached docs when serviceId changes
  useEffect(() => {
    if (serviceId) {
      const cached = getCachedDocs(serviceId)
      if (cached) {
        setDocs(cached.content)
        setProvider(cached.provider)
        setFromCache(true)
        setError(null)
      } else {
        // Reset when switching to a new service without cache
        setDocs(null)
        setProvider(null)
        setFromCache(false)
      }
    }
  }, [serviceId])

  const fetchAIStatus = async () => {
    try {
      setLoading(true)
      const backendHost = window.location.hostname
      const resp = await fetch(`http://${backendHost}:8080/api/v1/ai/status`, {
        signal: AbortSignal.timeout(5000) // 5 second timeout
      })
      const data = await resp.json()
      setAiStatus(data)
    } catch (err) {
      console.error('Failed to fetch AI status:', err)
      setAiStatus({ enabled: false, providers: {} })
    } finally {
      setLoading(false)
    }
  }

  const handleGenerateDocs = async (forceRefresh = false) => {
    if (!serviceId) return
    
    // If we have cached docs and not forcing refresh, don't regenerate
    if (!forceRefresh && docs && fromCache) {
      return
    }
    
    try {
      setGenerating(true)
      setError(null)
      if (forceRefresh) {
        setDocs(null)
        setProvider(null)
        setFromCache(false)
      }
      const backendHost = window.location.hostname
      
      // Use AbortController for timeout
      const controller = new AbortController()
      const timeoutId = setTimeout(() => controller.abort(), 90000) // 90 second timeout
      
      const resp = await fetch(`http://${backendHost}:8080/api/v1/ai/docs?serviceId=${encodeURIComponent(serviceId)}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
        signal: controller.signal
      })
      clearTimeout(timeoutId)
      
      if (!resp.ok) {
        const errText = await resp.text()
        throw new Error(errText || `Server error: ${resp.status}`)
      }
      const data = await resp.json()
      setDocs(data.content)
      setProvider(data.provider || null)
      setFromCache(false)
      
      // Cache the docs
      cacheDocs(serviceId, data.content, data.provider || '')
    } catch (err: any) {
      if (err.name === 'AbortError') {
        setError('Request timed out. The service has too much data - try a simpler service.')
      } else if (err.message === 'Failed to fetch') {
        setError('Network error: Could not reach backend. Check if backend is running.')
      } else {
        setError(err.message || 'Failed to generate documentation')
      }
    } finally {
      setGenerating(false)
    }
  }

  const handleAskQuestion = async () => {
    if (!serviceId || !question.trim()) return
    try {
      setGenerating(true)
      setError(null)
      setAnswer(null)
      const backendHost = window.location.hostname
      
      // Use AbortController for timeout
      const controller = new AbortController()
      const timeoutId = setTimeout(() => controller.abort(), 60000) // 60 second timeout
      
      const resp = await fetch(`http://${backendHost}:8080/api/v1/ai/ask?serviceId=${encodeURIComponent(serviceId)}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ question }),
        signal: controller.signal
      })
      clearTimeout(timeoutId)
      
      if (!resp.ok) {
        const errText = await resp.text()
        throw new Error(errText || `Server error: ${resp.status}`)
      }
      const data = await resp.json()
      setAnswer(data.content)
    } catch (err: any) {
      if (err.name === 'AbortError') {
        setError('Request timed out. Try a shorter question.')
      } else if (err.message === 'Failed to fetch') {
        setError('Network error: Could not reach backend.')
      } else {
        setError(err.message || 'Failed to get answer')
      }
    } finally {
      setGenerating(false)
    }
  }

  const handleSaveConfig = async () => {
    try {
      setSavingConfig(true)
      setError(null)
      const backendHost = window.location.hostname
      const resp = await fetch(`http://${backendHost}:8080/api/v1/ai/config`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config)
      })
      if (!resp.ok) {
        const errText = await resp.text()
        throw new Error(errText)
      }
      await fetchAIStatus()
      setShowSettings(false)
    } catch (err: any) {
      setError(err.message || 'Failed to save configuration')
    } finally {
      setSavingConfig(false)
    }
  }

  const getProviderIcon = (provider: string) => {
    const icons: Record<string, string> = {
      openai: '🤖',
      anthropic: '🧠',
      gemini: '✨',
      ollama: '🦙',
      lmstudio: '💻'
    }
    return icons[provider] || '🔌'
  }

  // Show settings panel
  if (showSettings) {
    return (
      <div className="p-5 space-y-6">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider flex items-center gap-2">
            <Settings size={14} className="text-blue-400" />
            AI Configuration
          </h3>
          <button
            onClick={() => setShowSettings(false)}
            className="p-1.5 rounded hover:bg-slate-800 text-slate-400 hover:text-slate-200"
          >
            <X size={16} />
          </button>
        </div>

        <div className="space-y-4">
          {/* OpenAI */}
          <div className="space-y-2">
            <label className="text-xs text-slate-400 flex items-center gap-2">
              {getProviderIcon('openai')} OpenAI API Key
            </label>
            <input
              type="password"
              placeholder="sk-..."
              value={config.openai_api_key}
              onChange={e => setConfig({ ...config, openai_api_key: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-slate-800 border border-slate-700 text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-blue-500"
            />
          </div>

          {/* Anthropic */}
          <div className="space-y-2">
            <label className="text-xs text-slate-400 flex items-center gap-2">
              {getProviderIcon('anthropic')} Anthropic API Key
            </label>
            <input
              type="password"
              placeholder="sk-ant-..."
              value={config.anthropic_api_key}
              onChange={e => setConfig({ ...config, anthropic_api_key: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-slate-800 border border-slate-700 text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-blue-500"
            />
          </div>

          {/* Gemini */}
          <div className="space-y-2">
            <label className="text-xs text-slate-400 flex items-center gap-2">
              {getProviderIcon('gemini')} Google Gemini API Key
            </label>
            <input
              type="password"
              placeholder="AIza..."
              value={config.gemini_api_key}
              onChange={e => setConfig({ ...config, gemini_api_key: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-slate-800 border border-slate-700 text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-blue-500"
            />
          </div>

          {/* Ollama */}
          <div className="space-y-2">
            <label className="text-xs text-slate-400 flex items-center gap-2">
              {getProviderIcon('ollama')} Ollama URL (local)
            </label>
            <input
              type="text"
              placeholder="http://localhost:11434"
              value={config.ollama_url}
              onChange={e => setConfig({ ...config, ollama_url: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-slate-800 border border-slate-700 text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-blue-500"
            />
          </div>

          {/* Default Provider */}
          <div className="space-y-2">
            <label className="text-xs text-slate-400">Default Provider</label>
            <select
              value={config.default_provider}
              onChange={e => setConfig({ ...config, default_provider: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-slate-800 border border-slate-700 text-sm text-slate-200 focus:outline-none focus:border-blue-500"
            >
              <option value="openai">OpenAI</option>
              <option value="anthropic">Anthropic</option>
              <option value="gemini">Google Gemini</option>
              <option value="ollama">Ollama (Local)</option>
              <option value="lmstudio">LM Studio (Local)</option>
            </select>
          </div>

          {error && (
            <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/20 text-red-400 text-sm">
              {error}
            </div>
          )}

          <button
            onClick={handleSaveConfig}
            disabled={savingConfig}
            className="w-full py-2.5 rounded-lg bg-blue-500 hover:bg-blue-600 disabled:bg-blue-500/50 text-white font-medium text-sm transition-colors flex items-center justify-center gap-2"
          >
            {savingConfig ? (
              <>
                <Loader2 size={14} className="animate-spin" />
                Saving...
              </>
            ) : (
              'Save Configuration'
            )}
          </button>
        </div>
      </div>
    )
  }

  // Main AI Docs UI
  return (
    <div className="p-5 space-y-6">
      {/* Header Card */}
      <div className="bg-gradient-to-br from-blue-500/10 via-purple-500/10 to-pink-500/10 rounded-xl p-5 border border-slate-700/30">
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-3">
            <div className="p-2.5 rounded-lg bg-gradient-to-br from-blue-500/20 to-purple-500/20 border border-blue-500/20">
              <BrainCircuit size={20} className="text-blue-400" />
            </div>
            <div>
              <h3 className="font-semibold text-slate-100">AI Documentation</h3>
              <p className="text-xs text-slate-500">
                {aiStatus?.enabled ? 'Multi-Provider AI' : 'Not Configured'}
              </p>
            </div>
          </div>
          <button
            onClick={() => setShowSettings(true)}
            className="p-2 rounded-lg hover:bg-slate-800/50 text-slate-400 hover:text-slate-200 transition-colors"
            title="Configure AI"
          >
            <Settings size={16} />
          </button>
        </div>
        
        {/* Provider Status */}
        {aiStatus && (
          <div className="flex flex-wrap gap-2 mt-3">
            {Object.entries(aiStatus.providers).map(([provider, enabled]) => (
              <span
                key={provider}
                className={`text-xs px-2 py-1 rounded-full flex items-center gap-1 ${
                  enabled 
                    ? 'bg-emerald-500/20 text-emerald-400' 
                    : 'bg-slate-700/50 text-slate-500'
                }`}
              >
                {getProviderIcon(provider)}
                {provider}
                {enabled ? <CheckCircle2 size={10} /> : <XCircle size={10} />}
              </span>
            ))}
          </div>
        )}
      </div>

      {/* Not configured state */}
      {!aiStatus?.enabled && (
        <div className="text-center py-8">
          <div className="p-4 rounded-full bg-slate-800 inline-block mb-4">
            <Settings size={32} className="text-slate-500" />
          </div>
          <h4 className="text-slate-200 font-medium mb-2">Configure AI Providers</h4>
          <p className="text-slate-500 text-sm mb-4">
            Add your API keys to enable AI documentation
          </p>
          <button
            onClick={() => setShowSettings(true)}
            className="px-4 py-2 rounded-lg bg-blue-500 hover:bg-blue-600 text-white text-sm font-medium transition-colors"
          >
            Open Settings
          </button>
        </div>
      )}

      {/* Configured state */}
      {aiStatus?.enabled && service && (
        <>
          {/* Generate Docs Button */}
          <button
            onClick={() => handleGenerateDocs()}
            disabled={generating}
            className="w-full py-3 rounded-lg bg-gradient-to-r from-blue-500 to-purple-500 hover:from-blue-600 hover:to-purple-600 disabled:opacity-50 text-white font-medium text-sm transition-all flex items-center justify-center gap-2"
          >
            {generating ? (
              <>
                <Loader2 size={16} className="animate-spin" />
                Generating Documentation...
              </>
            ) : (
              <>
                <Sparkles size={16} />
                Generate AI Documentation
              </>
            )}
          </button>

          {/* Generated Docs */}
          {docs && (
            <div className="bg-gradient-to-br from-slate-800/60 to-slate-900/60 rounded-xl p-5 border border-slate-700/30 shadow-xl">
              <div className="flex items-center justify-between mb-4 pb-3 border-b border-slate-700/50">
                <h4 className="text-sm font-semibold text-slate-200 flex items-center gap-2">
                  <BrainCircuit size={16} className="text-purple-400" />
                  AI-Generated Documentation
                </h4>
                <div className="flex items-center gap-2">
                  {fromCache && (
                    <span className="text-xs text-amber-400 bg-amber-900/30 px-2 py-1 rounded flex items-center gap-1">
                      <svg className="w-3 h-3" fill="currentColor" viewBox="0 0 20 20">
                        <path d="M10 12a2 2 0 100-4 2 2 0 000 4z"/>
                        <path fillRule="evenodd" d="M.458 10C1.732 5.943 5.522 3 10 3s8.268 2.943 9.542 7c-1.274 4.057-5.064 7-9.542 7S1.732 14.057.458 10zM14 10a4 4 0 11-8 0 4 4 0 018 0z" clipRule="evenodd"/>
                      </svg>
                      Cached
                    </span>
                  )}
                  <span className="text-xs text-slate-500 bg-slate-700/50 px-2 py-1 rounded">
                    {provider || 'AI'}
                  </span>
                  <button
                    onClick={() => handleGenerateDocs(true)}
                    disabled={generating}
                    className="text-xs text-slate-400 hover:text-slate-200 bg-slate-700/50 hover:bg-slate-600/50 px-2 py-1 rounded flex items-center gap-1 transition-colors disabled:opacity-50"
                    title="Refresh documentation"
                  >
                    <svg className={`w-3 h-3 ${generating ? 'animate-spin' : ''}`} fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                    </svg>
                    Refresh
                  </button>
                </div>
              </div>
              <MarkdownRenderer content={docs} />
            </div>
          )}

          {/* Ask Question */}
          <div className="space-y-3">
            <h4 className="text-sm font-semibold text-slate-300 flex items-center gap-2">
              <MessageSquare size={14} />
              Ask a Question
            </h4>
            <div className="flex gap-2">
              <input
                type="text"
                placeholder="e.g., What services connect to this?"
                value={question}
                onChange={e => setQuestion(e.target.value)}
                onKeyPress={e => e.key === 'Enter' && handleAskQuestion()}
                className="flex-1 px-3 py-2 rounded-lg bg-slate-800 border border-slate-700 text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-blue-500"
              />
              <button
                onClick={handleAskQuestion}
                disabled={generating || !question.trim()}
                className="px-4 py-2 rounded-lg bg-blue-500 hover:bg-blue-600 disabled:bg-blue-500/50 text-white text-sm font-medium transition-colors"
              >
                Ask
              </button>
            </div>
            {answer && (
              <div className="bg-slate-800/50 rounded-lg p-4 border border-slate-700/30">
                <MarkdownRenderer content={answer} />
              </div>
            )}
          </div>

          {error && (
            <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/20 text-red-400 text-sm">
              {error}
            </div>
          )}
        </>
      )}

      {/* Server node - no AI docs available */}
      {aiStatus?.enabled && nodeType === 'server' && (
        <div className="text-center py-8 text-slate-500">
          <p>AI Documentation is only available for service nodes.</p>
          <p className="text-xs mt-2">Click on a service to analyze it.</p>
        </div>
      )}
    </div>
  )
}

// Helper Components
function StatCard({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <div className="bg-slate-800/50 rounded-xl p-4 border border-slate-700/30">
      <div className="flex items-center gap-2 mb-1">
        {icon}
        <span className="text-xs text-slate-500 uppercase tracking-wide">{label}</span>
      </div>
      <p className="text-2xl font-bold text-slate-100">{value}</p>
    </div>
  )
}

function DetailRow({ icon, label, value, mono }: { icon: React.ReactNode; label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center gap-3 py-2 px-3 bg-slate-800/30 rounded-lg">
      <div className="text-slate-500">{icon}</div>
      <div className="flex-1 min-w-0">
        <p className="text-[10px] text-slate-500 uppercase tracking-wide">{label}</p>
        <p className={`text-sm text-slate-200 truncate ${mono ? 'font-mono' : ''}`}>{value}</p>
      </div>
    </div>
  )
}

function ConnectionCard({ connection, direction }: { connection: Connection; direction: 'incoming' | 'outgoing' }) {
  return (
    <div className="bg-slate-800/50 rounded-lg px-3 py-2 text-xs border border-slate-700/20">
      <div className="flex items-center justify-between">
        <span className="text-slate-300 font-mono truncate">
          {direction === 'outgoing' ? connection.target_id : connection.source_id}
        </span>
        <span className="text-slate-500 font-mono">:{connection.port}</span>
      </div>
      {(connection.bytes_sent_rate || connection.bytes_recv_rate) && (
        <div className="flex items-center gap-3 mt-1 text-slate-500">
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

// FeatureCard component - reserved for future use
// function FeatureCard({ icon, title, description, status }: { 
//   icon: React.ReactNode; title: string; description: string; status: 'available' | 'coming_soon' 
// }) { ... }

// Service Type Icon Component
function ServiceTypeIcon({ type, size = 20 }: { type?: string; size?: number }) {
  switch (type) {
    case 'database':
      return <Database size={size} />
    case 'cache':
      return <HardDrive size={size} />
    case 'message_queue':
      return <MessageSquare size={size} />
    case 'web_server':
      return <Globe size={size} />
    case 'proxy':
      return <Shield size={size} />
    case 'monitoring':
      return <ActivityIcon size={size} />
    case 'application':
      return <Cpu size={size} />
    default:
      return <Server size={size} />
  }
}
