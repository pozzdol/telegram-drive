// Video previews made in the browser: it already decodes every codec it can play,
// so the server needs no ffmpeg. A frame is drawn to a canvas and stored as the file's JPEG thumb.
import { ApiError, withGuest } from './api.js'

const BASE = (import.meta.env.VITE_API_URL || '').replace(/\/$/, '')
const SIZE = 480 // same long edge as the server's image thumbs

// Grabs one frame (10% in, at most 1 s, past the usual black first frame) as a JPEG Blob.
export function videoFrame(src, timeoutMs = 60_000) {
  return new Promise((resolve, reject) => {
    const v = document.createElement('video')
    let timer
    const finish = (err, blob) => {
      clearTimeout(timer)
      v.removeAttribute('src')
      v.load() // stop downloading
      err ? reject(err) : resolve(blob)
    }
    timer = setTimeout(() => finish(new Error('video preview timed out')), timeoutMs)
    v.muted = true
    v.playsInline = true
    v.preload = 'auto'
    v.crossOrigin = 'use-credentials' // API on another origin: keeps the canvas readable
    v.onerror = () => finish(new Error('browser cannot decode this video'))
    v.onloadedmetadata = () => {
      v.currentTime = Number.isFinite(v.duration) ? Math.min(1, v.duration / 10) : 0
    }
    v.onseeked = () => {
      if (!v.videoWidth) return finish(new Error('video has no picture'))
      const s = Math.min(1, SIZE / Math.max(v.videoWidth, v.videoHeight))
      const c = document.createElement('canvas')
      c.width = Math.round(v.videoWidth * s)
      c.height = Math.round(v.videoHeight * s)
      c.getContext('2d').drawImage(v, 0, 0, c.width, c.height)
      c.toBlob(b => (b ? finish(null, b) : finish(new Error('could not encode preview'))), 'image/jpeg', 0.82)
    }
    v.src = src
  })
}

// ponytail: one preview at a time; each one streams part of a video through Telegram.
let queue = Promise.resolve()
const tried = new Set()

// Makes and stores the preview for a video file. src: local object URL or the file's stream URL.
// Resolves true when a preview was saved; each file is tried at most once per page load.
export function makeVideoThumb(fileId, src) {
  if (tried.has(fileId)) return Promise.resolve(false)
  tried.add(fileId)
  const job = queue.then(async () => {
    const blob = await videoFrame(src)
    const res = await fetch(withGuest(`${BASE}/api/files/${fileId}/thumb`), {
      method: 'PUT', credentials: 'include', headers: { 'Content-Type': 'image/jpeg' }, body: blob,
    })
    if (!res.ok) throw new ApiError(res.status, `HTTP ${res.status}`)
    return true
  })
  queue = job.catch(() => {})
  return job.catch(() => false)
}
