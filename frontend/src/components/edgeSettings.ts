import { createContext } from 'react'

// Graph-wide edge drawing settings, so toggling them does not mean rewriting
// every edge object.
export const EdgeSettingsContext = createContext<{ animate: boolean }>({ animate: false })
