// Deep inspection data from protocol probing
export interface ServiceInspection {
  pid?: number
  process_name?: string
  command_line?: string[]
  working_dir?: string
  env_var_names?: string[]
  listen_ports?: number[]
  config_files?: string[]
  dependencies?: Dependency[]
  http_info?: HTTPProbeInfo
  db_info?: DBProbeInfo
  k8s_metadata?: K8sMetadataInfo
  inspected_at?: string
}

export interface Dependency {
  name: string
  version?: string
  type: string  // npm, pip, go, maven
}

export interface HTTPProbeInfo {
  server_header?: string
  x_powered_by?: string
  health_endpoint?: string
  health_status?: number
  endpoints?: string[]
  response_headers?: Record<string, string>
}

export interface DBProbeInfo {
  type: string        // postgres, mysql, redis, mongodb
  version?: string
  databases?: string[]
  table_count?: number
}

export interface K8sMetadataInfo {
  pod_name?: string
  namespace?: string
  service_account?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
}

export interface Service {
  id: string
  name: string
  display_name?: string    // K8s resolved name (e.g., "Pod: default/nginx-abc123")
  resolved_name?: string   // Full K8s path
  type?: string            // Service type (database, cache, web_server, etc.)
  tech?: string            // Specific technology (PostgreSQL, Redis, Nginx, etc.)
  icon?: string            // Icon identifier
  namespace?: string
  node?: string
  pod_ip?: string
  labels?: Record<string, string>
  last_seen: string
  healthy?: boolean
  inspection?: ServiceInspection  // Deep inspection data
  original_id?: string     // Original service ID (for aggregated services)
  aggregated_count?: number // Number of services aggregated (for display)
}

// A decommission candidate: a service's most recent observation, for
// services not seen since before a requested cutoff.
export interface StaleService {
  id: string
  name?: string
  type?: string
  tech?: string
  namespace?: string
  node?: string
  last_seen: string
}

// A service's rank in the blast-radius-size ranking: how many other
// services (transitively) depend on it.
export interface CriticalityEntry {
  id: string
  name?: string
  type?: string
  node?: string
  blast_radius: number
}

// Service type constants
export const ServiceTypes = {
  DATABASE: 'database',
  CACHE: 'cache',
  MESSAGE_QUEUE: 'message_queue',
  WEB_SERVER: 'web_server',
  PROXY: 'proxy',
  APP: 'application',
  MONITORING: 'monitoring',
  UNKNOWN: 'unknown',
} as const

// Type colors for visualization
export const TypeColors: Record<string, string> = {
  database: '#3b82f6',      // blue
  cache: '#ef4444',         // red
  message_queue: '#f59e0b', // amber
  web_server: '#22c55e',    // green
  proxy: '#8b5cf6',         // purple
  application: '#06b6d4',   // cyan
  monitoring: '#ec4899',    // pink
  unknown: '#6b7280',       // gray
}

export interface Connection {
  id: string
  source_id: string
  target_id: string
  port: number
  protocol?: string // "tcp" (default) or "udp"
  count: number
  bytes_sent?: number
  bytes_recv?: number
  bytes_sent_rate?: number  // Bytes/second
  bytes_recv_rate?: number  // Bytes/second
  packets_sent?: number
  packets_recv?: number
  last_seen: string
  latency_ms?: number
}

// NodeMetrics represents CPU/RAM usage for a physical node/server
export interface NodeMetrics {
  node_name: string
  cpu_percent: number
  mem_percent: number
  mem_used: number
  mem_total: number
  last_seen: string
}

export interface Topology {
  services: Service[]
  connections: Connection[]
  node_metrics?: Record<string, NodeMetrics>
  updated_at: string
}

export interface Stats {
  services: number
  connections: number
}

// ============================================================================
// AI Documentation Types
// ============================================================================

export interface AIProvider {
  id: string
  name: string
  description: string
  models: string[]
  requires: 'api_key' | 'local_server'
}

export interface AIConfig {
  openai_api_key?: string
  openai_model?: string
  anthropic_api_key?: string
  anthropic_model?: string
  gemini_api_key?: string
  gemini_model?: string
  ollama_url?: string
  ollama_model?: string
  lmstudio_url?: string
  lmstudio_model?: string
  default_provider?: string
}

export interface AIStatus {
  enabled: boolean
  providers: Record<string, boolean>
  message?: string
}

export interface AIDocsResponse {
  content: string
  provider: string
  model: string
  tokens_used?: number
}

// Helper to format bytes to human readable
export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}
