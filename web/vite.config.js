import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In dev, /api is proxied to the Go backend, so no CORS is needed.
// Set API_PROXY if the backend runs on another port.
export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': process.env.API_PROXY || 'http://localhost:5758' } },
})
