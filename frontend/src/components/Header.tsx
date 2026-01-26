import { Activity, Wifi, WifiOff, Server, GitBranch, Eye, EyeOff, Filter } from 'lucide-react'
import { Stats } from '../types'

interface FilterState {
  hideLocalhost: boolean
  minConnections: number
  collapseExternal: boolean
}

interface HeaderProps {
  isConnected: boolean
  stats: Stats
  filters: FilterState
  onFiltersChange: (filters: FilterState) => void
  filteredStats?: Stats
}

export default function Header({ isConnected, stats, filters, onFiltersChange, filteredStats }: HeaderProps) {
  const displayStats = filteredStats || stats
  const hasFilters = filters.hideLocalhost || filters.minConnections > 1 || filters.collapseExternal

  return (
    <header className="h-14 px-6 flex items-center justify-between border-b border-dark-800 bg-dark-900/80 backdrop-blur-sm">
      {/* Logo */}
      <div className="flex items-center gap-3">
        <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-lens-500 to-lens-600 flex items-center justify-center">
          <Activity size={18} className="text-white" />
        </div>
        <div>
          <h1 className="text-lg font-semibold tracking-tight text-dark-100">
            InfraLens
          </h1>
        </div>
      </div>

      {/* Filters */}
      <div className="flex items-center gap-2">
        {/* Hide Localhost Toggle */}
        <button
          onClick={() => onFiltersChange({ ...filters, hideLocalhost: !filters.hideLocalhost })}
          className={`
            flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all
            ${filters.hideLocalhost 
              ? 'bg-amber-500/20 text-amber-400 border border-amber-500/30' 
              : 'bg-dark-800 text-dark-400 border border-dark-700 hover:text-dark-300'
            }
          `}
          title="Hide localhost (127.0.0.1) traffic"
        >
          {filters.hideLocalhost ? <EyeOff size={14} /> : <Eye size={14} />}
          <span>Localhost</span>
        </button>

        {/* Min Connections Filter */}
        <div className="relative group">
          <button
            className={`
              flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all
              ${filters.minConnections > 1 
                ? 'bg-blue-500/20 text-blue-400 border border-blue-500/30' 
                : 'bg-dark-800 text-dark-400 border border-dark-700 hover:text-dark-300'
              }
            `}
            title="Minimum connections to show"
          >
            <Filter size={14} />
            <span>Min: {filters.minConnections}</span>
          </button>
          {/* Dropdown */}
          <div className="absolute top-full left-0 mt-1 bg-dark-800 border border-dark-700 rounded-lg shadow-xl opacity-0 invisible group-hover:opacity-100 group-hover:visible transition-all z-50 py-1">
            {[1, 2, 3, 5, 10].map(n => (
              <button
                key={n}
                onClick={() => onFiltersChange({ ...filters, minConnections: n })}
                className={`
                  w-full px-4 py-1.5 text-xs text-left hover:bg-dark-700 transition-colors
                  ${filters.minConnections === n ? 'text-blue-400' : 'text-dark-300'}
                `}
              >
                {n === 1 ? 'Show all' : `${n}+ connections`}
              </button>
            ))}
          </div>
        </div>

        {/* Collapse External Toggle */}
        <button
          onClick={() => onFiltersChange({ ...filters, collapseExternal: !filters.collapseExternal })}
          className={`
            flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all
            ${filters.collapseExternal 
              ? 'bg-purple-500/20 text-purple-400 border border-purple-500/30' 
              : 'bg-dark-800 text-dark-400 border border-dark-700 hover:text-dark-300'
            }
          `}
          title="Collapse external endpoints"
        >
          <span>🗂️</span>
          <span>Collapse</span>
        </button>

        {/* Clear filters */}
        {hasFilters && (
          <button
            onClick={() => onFiltersChange({ hideLocalhost: false, minConnections: 1, collapseExternal: false })}
            className="px-2 py-1.5 rounded-lg text-xs text-dark-500 hover:text-dark-300 hover:bg-dark-800 transition-colors"
            title="Clear all filters"
          >
            ✕
          </button>
        )}
      </div>

      {/* Stats */}
      <div className="flex items-center gap-6">
        <div className="flex items-center gap-2 text-sm">
          <Server size={16} className="text-dark-400" />
          <span className="text-dark-300">{displayStats.services}</span>
          <span className="text-dark-500">services</span>
          {hasFilters && stats.services !== displayStats.services && (
            <span className="text-dark-600 text-xs">/ {stats.services}</span>
          )}
        </div>
        
        <div className="flex items-center gap-2 text-sm">
          <GitBranch size={16} className="text-dark-400" />
          <span className="text-dark-300">{displayStats.connections}</span>
          <span className="text-dark-500">connections</span>
          {hasFilters && stats.connections !== displayStats.connections && (
            <span className="text-dark-600 text-xs">/ {stats.connections}</span>
          )}
        </div>

        {/* Connection status */}
        <div className={`
          flex items-center gap-2 px-3 py-1.5 rounded-full text-xs font-medium
          ${isConnected 
            ? 'bg-lens-500/10 text-lens-400 border border-lens-500/20' 
            : 'bg-red-500/10 text-red-400 border border-red-500/20'
          }
        `}>
          {isConnected ? (
            <>
              <Wifi size={14} />
              <span>Live</span>
            </>
          ) : (
            <>
              <WifiOff size={14} />
              <span>Disconnected</span>
            </>
          )}
        </div>
      </div>
    </header>
  )
}
