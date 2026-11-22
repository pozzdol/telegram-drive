import { writeFileSync } from 'node:fs'
import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// Security headers for static hosts that read a _headers file (Cloudflare Pages, Netlify).
// Built from VITE_API_URL so the page may talk only to its own API.
function securityHeaders(api) {
  const csp = [
    "default-src 'self'",
    "script-src 'self' https://telegram.org", // Telegram login widget
    "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
    'font-src https://fonts.gstatic.com',
    `img-src 'self' data: blob: ${api} https://*.telegram.org https://t.me`,
    `media-src 'self' blob: ${api}`,
    `connect-src 'self' ${api}`,
    'frame-src https://oauth.telegram.org',
    "frame-ancestors 'none'",
    "base-uri 'self'",
    "form-action 'self'",
    "object-src 'none'",
  ].join('; ')
  return {
    name: 'security-headers',
    apply: 'build',
    writeBundle(opts) {
      writeFileSync(`${opts.dir}/_headers`, `/*
  Content-Security-Policy: ${csp}
  Strict-Transport-Security: max-age=31536000; includeSubDomains
  X-Content-Type-Options: nosniff
  Referrer-Policy: strict-origin-when-cross-origin
  Permissions-Policy: camera=(), microphone=(), geolocation=()
`)
    },
  }
}

// In dev, /api is proxied to the Go backend, so no CORS is needed.
// Set API_PROXY if the backend runs on another port.
export default defineConfig(({ mode }) => ({
  plugins: [react(), securityHeaders(loadEnv(mode, process.cwd(), 'VITE_').VITE_API_URL || '')],
  build: { chunkSizeWarningLimit: 600 }, // the editor chunk (TipTap) loads only when a document opens
  server: { proxy: { '/api': process.env.API_PROXY || 'http://localhost:5758' } },
}))
