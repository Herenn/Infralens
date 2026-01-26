import { Activity, Wifi, WifiOff, Server, GitBranch } from 'lucide-react'
import { Stats } from '../types'

interface HeaderProps {
  isConnected: boolean
  stats: Stats
}

export default function Header({ isConnected, stats }: HeaderProps) {
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

      {/* Stats */}
      <div className="flex items-center gap-6">
        <div className="flex items-center gap-2 text-sm">
          <Server size={16} className="text-dark-400" />
          <span className="text-dark-300">{stats.services}</span>
          <span className="text-dark-500">services</span>
        </div>
        
        <div className="flex items-center gap-2 text-sm">
          <GitBranch size={16} className="text-dark-400" />
          <span className="text-dark-300">{stats.connections}</span>
          <span className="text-dark-500">connections</span>
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
