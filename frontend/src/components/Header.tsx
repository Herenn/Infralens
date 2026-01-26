import { useState, useRef, useEffect } from 'react'
import { createPortal } from 'react-dom'
import { Activity, Wifi, WifiOff, Server, GitBranch, Eye, EyeOff, Filter, Download, ChevronDown, FileJson, Image, Layers, Box } from 'lucide-react'
import { Stats, ViewMode } from '../types'

interface FilterState {
  hideLocalhost: boolean
  minConnections: number
  collapseExternal: boolean
  namespace: string
}

interface HeaderProps {
  isConnected: boolean
  stats: Stats
  filters: FilterState
  onFiltersChange: (filters: FilterState) => void
  filteredStats?: Stats
  onExportPng?: () => void
  onExportJson?: () => void
  viewMode: ViewMode
  onViewModeChange: (mode: ViewMode) => void
  availableNamespaces: string[]
}

export default function Header({ isConnected, stats, filters, onFiltersChange, filteredStats, onExportPng, onExportJson, viewMode, onViewModeChange, availableNamespaces }: HeaderProps) {
  const [filterDropdownOpen, setFilterDropdownOpen] = useState(false)
  const [exportDropdownOpen, setExportDropdownOpen] = useState(false)
  const [namespaceDropdownOpen, setNamespaceDropdownOpen] = useState(false)
  const filterButtonRef = useRef<HTMLButtonElement>(null)
  const exportButtonRef = useRef<HTMLButtonElement>(null)
  const namespaceButtonRef = useRef<HTMLButtonElement>(null)
  
  const displayStats = filteredStats || stats
  const hasFilters = filters.hideLocalhost || filters.minConnections > 1 || filters.collapseExternal || filters.namespace !== 'all'

  // Close dropdowns when clicking outside
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      const target = event.target as Node
      
      // Check filter dropdown
      if (filterButtonRef.current && !filterButtonRef.current.contains(target)) {
        const filterDropdown = document.getElementById('filter-dropdown')
        if (filterDropdown && !filterDropdown.contains(target)) {
          setFilterDropdownOpen(false)
        }
      }
      
      // Check export dropdown
      if (exportButtonRef.current && !exportButtonRef.current.contains(target)) {
        const exportDropdown = document.getElementById('export-dropdown')
        if (exportDropdown && !exportDropdown.contains(target)) {
          setExportDropdownOpen(false)
        }
      }
      
      // Check namespace dropdown
      if (namespaceButtonRef.current && !namespaceButtonRef.current.contains(target)) {
        const namespaceDropdown = document.getElementById('namespace-dropdown')
        if (namespaceDropdown && !namespaceDropdown.contains(target)) {
          setNamespaceDropdownOpen(false)
        }
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

  // Get button position for portal dropdown
  const getDropdownPosition = (buttonRef: React.RefObject<HTMLButtonElement>) => {
    if (!buttonRef.current) return { top: 0, left: 0 }
    const rect = buttonRef.current.getBoundingClientRect()
    return {
      top: rect.bottom + 4,
      left: rect.left,
    }
  }

  const filterPos = getDropdownPosition(filterButtonRef)
  const exportPos = getDropdownPosition(exportButtonRef)
  const namespacePos = getDropdownPosition(namespaceButtonRef)

  return (
    <header className="h-14 px-6 flex items-center justify-between border-b border-dark-800 bg-dark-900/80 backdrop-blur-sm relative z-50">
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

      {/* View Mode Toggle & Filters */}
      <div className="flex items-center gap-2">
        {/* View Mode Toggle */}
        <div className="flex items-center bg-dark-800 rounded-lg border border-dark-700 p-0.5">
          <button
            onClick={() => onViewModeChange('physical')}
            className={`
              flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium transition-all
              ${viewMode === 'physical' 
                ? 'bg-lens-500/20 text-lens-400' 
                : 'text-dark-400 hover:text-dark-300'
              }
            `}
            title="Physical View - Group by Server/Node"
          >
            <Server size={14} />
            <span>Physical</span>
          </button>
          <button
            onClick={() => onViewModeChange('logical')}
            className={`
              flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium transition-all
              ${viewMode === 'logical' 
                ? 'bg-lens-500/20 text-lens-400' 
                : 'text-dark-400 hover:text-dark-300'
              }
            `}
            title="Logical View - Group by Namespace"
          >
            <Layers size={14} />
            <span>Logical</span>
          </button>
        </div>

        {/* Separator */}
        <div className="w-px h-6 bg-dark-700" />

        {/* Namespace Filter */}
        {availableNamespaces.length > 0 && (
          <button
            ref={namespaceButtonRef}
            onClick={() => {
              setNamespaceDropdownOpen(!namespaceDropdownOpen)
              setFilterDropdownOpen(false)
              setExportDropdownOpen(false)
            }}
            className={`
              flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all
              ${filters.namespace !== 'all' 
                ? 'bg-cyan-500/20 text-cyan-400 border border-cyan-500/30' 
                : 'bg-dark-800 text-dark-400 border border-dark-700 hover:text-dark-300'
              }
            `}
            title="Filter by Namespace"
          >
            <Box size={14} />
            <span>{filters.namespace === 'all' ? 'All Namespaces' : filters.namespace}</span>
            <ChevronDown size={12} className={`transition-transform ${namespaceDropdownOpen ? 'rotate-180' : ''}`} />
          </button>
        )}

        {/* Namespace Dropdown Portal */}
        {namespaceDropdownOpen && createPortal(
          <div 
            id="namespace-dropdown"
            className="fixed bg-dark-800 border border-dark-700 rounded-lg shadow-2xl py-1 min-w-[180px] max-h-[300px] overflow-y-auto"
            style={{ 
              top: namespacePos.top, 
              left: namespacePos.left,
              zIndex: 99999,
            }}
          >
            <button
              onClick={() => {
                onFiltersChange({ ...filters, namespace: 'all' })
                setNamespaceDropdownOpen(false)
              }}
              className={`
                w-full px-4 py-2.5 text-sm text-left hover:bg-dark-700 transition-colors cursor-pointer
                ${filters.namespace === 'all' ? 'text-cyan-400 bg-dark-700/50' : 'text-dark-300'}
              `}
            >
              All Namespaces
            </button>
            <div className="border-t border-dark-700 my-1" />
            {availableNamespaces.map(ns => (
              <button
                key={ns}
                onClick={() => {
                  onFiltersChange({ ...filters, namespace: ns })
                  setNamespaceDropdownOpen(false)
                }}
                className={`
                  w-full px-4 py-2.5 text-sm text-left hover:bg-dark-700 transition-colors cursor-pointer
                  ${filters.namespace === ns ? 'text-cyan-400 bg-dark-700/50' : 'text-dark-300'}
                `}
              >
                {ns === 'external' ? '🌐 External' : ns}
              </button>
            ))}
          </div>,
          document.body
        )}

        {/* Separator */}
        <div className="w-px h-6 bg-dark-700" />

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
        <button
          ref={filterButtonRef}
          onClick={() => {
            setFilterDropdownOpen(!filterDropdownOpen)
            setExportDropdownOpen(false)
          }}
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
          <ChevronDown size={12} className={`transition-transform ${filterDropdownOpen ? 'rotate-180' : ''}`} />
        </button>

        {/* Filter Dropdown Portal */}
        {filterDropdownOpen && createPortal(
          <div 
            id="filter-dropdown"
            className="fixed bg-dark-800 border border-dark-700 rounded-lg shadow-2xl py-1 min-w-[150px]"
            style={{ 
              top: filterPos.top, 
              left: filterPos.left,
              zIndex: 99999,
            }}
          >
            {minConnectionOptions.map(opt => (
              <button
                key={opt.value}
                onClick={() => {
                  onFiltersChange({ ...filters, minConnections: opt.value })
                  setFilterDropdownOpen(false)
                }}
                className={`
                  w-full px-4 py-2.5 text-sm text-left hover:bg-dark-700 transition-colors cursor-pointer
                  ${filters.minConnections === opt.value ? 'text-blue-400 bg-dark-700/50' : 'text-dark-300'}
                `}
              >
                {opt.label}
              </button>
            ))}
          </div>,
          document.body
        )}

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

        {/* Export Button with Dropdown */}
        <button
          ref={exportButtonRef}
          onClick={() => {
            setExportDropdownOpen(!exportDropdownOpen)
            setFilterDropdownOpen(false)
          }}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all bg-dark-800 text-dark-400 border border-dark-700 hover:text-dark-300 hover:bg-dark-700"
          title="Export topology"
        >
          <Download size={14} />
          <span>Export</span>
          <ChevronDown size={12} className={`transition-transform ${exportDropdownOpen ? 'rotate-180' : ''}`} />
        </button>

        {/* Export Dropdown Portal */}
        {exportDropdownOpen && createPortal(
          <div 
            id="export-dropdown"
            className="fixed bg-dark-800 border border-dark-700 rounded-lg shadow-2xl py-1 min-w-[160px]"
            style={{ 
              top: exportPos.top, 
              left: exportPos.left,
              zIndex: 99999,
            }}
          >
            <button
              onClick={() => {
                onExportPng?.()
                setExportDropdownOpen(false)
              }}
              className="w-full px-4 py-2.5 text-sm text-left hover:bg-dark-700 transition-colors cursor-pointer text-dark-300 flex items-center gap-2"
            >
              <Image size={16} />
              Export as PNG
            </button>
            <button
              onClick={() => {
                onExportJson?.()
                setExportDropdownOpen(false)
              }}
              className="w-full px-4 py-2.5 text-sm text-left hover:bg-dark-700 transition-colors cursor-pointer text-dark-300 flex items-center gap-2"
            >
              <FileJson size={16} />
              Export as JSON
            </button>
          </div>,
          document.body
        )}

        {/* Clear filters */}
        {hasFilters && (
          <button
            onClick={() => onFiltersChange({ hideLocalhost: false, minConnections: 1, collapseExternal: false, namespace: 'all' })}
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
