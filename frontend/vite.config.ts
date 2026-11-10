import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 3000,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        // Keep the browser's Host header (no changeOrigin): with UI login
        // enabled the backend checks the WebSocket's Origin against Host, and
        // Vite's WebSocket proxy sends no X-Forwarded-Host to compare with.
        changeOrigin: false,
        ws: true,
      },
    },
  },
})
