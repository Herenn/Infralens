import { useState, useRef, useEffect } from 'react'
import { Activity, Wifi, WifiOff, Server, GitBranch, Eye, EyeOff, Filter, Download, ChevronDown } from 'lucide-react'
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
  onExport?: () => void
}

export default function Header({ isConnected, stats, filters, onFiltersChange, filteredStats, onExport }: HeaderProps) {
  const [dropdownOpen, setDropdownOpen] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)
  
  const displayStats = filteredStats || stats
  const hasFilters = filters.hideLocalhost || filters.minConnections > 1 || filters.collapseExternal

  // Close dropdown when clicking outside
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setDropdownOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  const minConnectionOptions = [
    { value: 1, label: 'Show all' },
    { value: 2, label: '2+ connections' },
    { value: 3, label: '3+ connections' },
    { value: 5, label: '5+ connections' },
    { value: 10, label: '10+ connections' },
  ]

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

        {/* Min Connections Filter - Click-based dropdown */}
        <div className="relative" ref={dropdownRef}>
          <button
            onClick={() => setDropdownOpen(!dropdownOpen)}
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
            <ChevronDown size={12} className={`transition-transform ${dropdownOpen ? 'rotate-180' : ''}`} />
          </button>
          
          {/* Dropdown Menu - high z-index to appear above ReactFlow */}
          {dropdownOpen && (
            <div 
              className="absolute top-full left-0 mt-1 bg-dark-800 border border-dark-700 rounded-lg shadow-xl py-1 min-w-[140px]"
              style={{ zIndex: 9999 }}
            >
              {minConnectionOptions.map(opt => (
                <button
                  key={opt.value}
                  onClick={(e) => {
                    e.stopPropagation()
                    onFiltersChange({ ...filters, minConnections: opt.value })
                    setDropdownOpen(false)
                  }}
                  className={`
                    w-full px-4 py-2 text-xs text-left hover:bg-dark-700 transition-colors cursor-pointer
                    ${filters.minConnections === opt.value ? 'text-blue-400 bg-dark-700/50' : 'text-dark-300'}
                  `}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          )}
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
          title="Collapse external endpoints by type"
        >
          <span>🗂️</span>
          <span>Collapse</span>
        </button>

        {/* Export Button */}
        {onExport && (
          <button
            onClick={onExport}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all bg-dark-800 text-dark-400 border border-dark-700 hover:text-dark-300 hover:bg-dark-700"
            title="Export topology as PNG"
          >
            <Download size={14} />
            <span>Export</span>
          </button>
        )}

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
