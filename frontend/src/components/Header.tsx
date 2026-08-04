import { useState, useRef, useEffect } from 'react'
import { createPortal } from 'react-dom'
import { Activity, Wifi, WifiOff, Server, GitBranch, Eye, EyeOff, Filter, Download, ChevronDown, FileJson, Image, Search, X, FileCode, Share2 } from 'lucide-react'
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
  searchQuery: string
  onSearchChange: (query: string) => void
  searchMatchCount?: number
  onExportPng?: () => void
  onExportJson?: () => void
  onExportMermaid?: () => void
  onExportDot?: () => void
}

export default function Header({ isConnected, stats, filters, onFiltersChange, filteredStats, searchQuery, onSearchChange, searchMatchCount, onExportPng, onExportJson, onExportMermaid, onExportDot }: HeaderProps) {
  const [filterDropdownOpen, setFilterDropdownOpen] = useState(false)
  const [exportDropdownOpen, setExportDropdownOpen] = useState(false)
  const filterButtonRef = useRef<HTMLButtonElement>(null)
  const exportButtonRef = useRef<HTMLButtonElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  
  const displayStats = filteredStats || stats
  const hasFilters = filters.hideLocalhost || filters.minConnections > 1 || filters.collapseExternal

  // Focus search with "/" from anywhere, clear with Escape
  useEffect(() => {
    function handleKeyDown(event: KeyboardEvent) {
      const target = event.target as HTMLElement
      const inInput = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA'
      if (event.key === '/' && !inInput) {
        event.preventDefault()
        searchInputRef.current?.focus()
      } else if (event.key === 'Escape' && target === searchInputRef.current) {
        onSearchChange('')
        searchInputRef.current?.blur()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onSearchChange])

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

      {/* Search */}
      <div className="flex items-center">
        <div className="relative">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-dark-500 pointer-events-none" />
          <input
            ref={searchInputRef}
            type="text"
            value={searchQuery}
            onChange={(e) => onSearchChange(e.target.value)}
            placeholder="Search services...  ( / )"
            className="w-56 pl-9 pr-8 py-1.5 rounded-lg text-xs bg-dark-800 text-dark-200 border border-dark-700 
              placeholder:text-dark-500 focus:outline-none focus:border-lens-500/50 focus:ring-1 focus:ring-lens-500/30 transition-all"
          />
          {searchQuery && (
            <button
              onClick={() => onSearchChange('')}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-dark-500 hover:text-dark-300"
              title="Clear search"
            >
              <X size={13} />
            </button>
          )}
        </div>
        {searchQuery && (
          <span className={`ml-2 text-xs ${searchMatchCount ? 'text-lens-400' : 'text-red-400'}`}>
            {searchMatchCount || 0} match{searchMatchCount === 1 ? '' : 'es'}
          </span>
        )}
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
            <button
              onClick={() => {
                onExportMermaid?.()
                setExportDropdownOpen(false)
              }}
              className="w-full px-4 py-2.5 text-sm text-left hover:bg-dark-700 transition-colors cursor-pointer text-dark-300 flex items-center gap-2"
            >
              <FileCode size={16} />
              Export as Mermaid
            </button>
            <button
              onClick={() => {
                onExportDot?.()
                setExportDropdownOpen(false)
              }}
              className="w-full px-4 py-2.5 text-sm text-left hover:bg-dark-700 transition-colors cursor-pointer text-dark-300 flex items-center gap-2"
            >
              <Share2 size={16} />
              Export as DOT
            </button>
          </div>,
          document.body
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
