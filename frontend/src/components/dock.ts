// The one place secondary panels open: docked at the map's top-left, beside
// the panel rail, one at a time. The map frames what it shows in the space
// the open panel leaves free (see App.tsx).

export const DOCK_WIDTH = 400

export const DOCK_CLASS = `absolute top-3 left-3 z-20 w-[400px] max-w-[calc(100%-1.5rem)] max-h-[calc(100%-1.5rem)]
  flex flex-col rounded-xl bg-dark-900/95 backdrop-blur-md border border-dark-700 shadow-2xl shadow-black/50 overflow-hidden`

export type PanelId = 'changes' | 'idle' | 'critical' | 'orphans' | 'stale' | 'compare'
