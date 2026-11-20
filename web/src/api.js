// Backend URL. Empty in dev (Vite proxy); https://api.example.com in production.
const BASE = (import.meta.env.VITE_API_URL || '').replace(/\/$/, '')

export class ApiError extends Error {
  constructor(status, message) {
    super(message)
    this.status = status
  }
}

// A guest of a public link (/s/<token>) has no login: their guest session rides on every call,
// as the X-Guest header, or as ?g= where no header can go (<img src>, <video src>, downloads).
let guest = ''
export const setGuest = t => { guest = t || '' }
export const withGuest = url => (guest ? `${url}${url.includes('?') ? '&' : '?'}g=${encodeURIComponent(guest)}` : url)

// JSON API call. Auth rides on the HttpOnly cookie set by /api/auth/login.
export async function api(path, { method = 'GET', body } = {}) {
  const headers = {}
  if (body) headers['Content-Type'] = 'application/json'
  if (guest) headers['X-Guest'] = guest
  const res = await fetch(BASE + path, {
    method,
    credentials: 'include',
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return null
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, data.error || `HTTP ${res.status}`)
  return data
}

export const fileURL = (id, inline) => withGuest(`${BASE}/api/files/${id}${inline ? '?inline=1' : ''}`)
// ?v= keeps the browser's immutable cache honest if a deleted file's id is ever reused.
export const thumbURL = f => withGuest(`${BASE}/api/files/${f.id}/thumb?v=${f.created_at}`)

const sleep = ms => new Promise(r => setTimeout(r, ms))

// XHR instead of fetch: fetch has no upload progress events.
function putChunk(id, offset, blob, onProgress) {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest()
    x.open('PUT', `${BASE}/api/uploads/${id}?offset=${offset}`)
    x.withCredentials = true
    if (guest) x.setRequestHeader('X-Guest', guest)
    x.upload.onprogress = e => onProgress(e.loaded)
    x.onload = () => {
      let d = {}
      try { d = JSON.parse(x.responseText) } catch { /* empty body */ }
      x.status < 300 ? resolve(d) : reject(new ApiError(x.status, d.error || `HTTP ${x.status}`))
    }
    x.onerror = () => reject(new ApiError(0, 'Koneksi terputus'))
    x.send(blob)
  })
}

// Uploads a File in chunks (Cloudflare caps request bodies at 100 MB), then waits while
// the server forwards it to Telegram. onState gets {phase: 'upload'|'telegram', done, total}.
export async function uploadFile(file, folderId, onState) {
  const u = await api('/api/uploads', {
    method: 'POST',
    body: { name: file.name, size: file.size, mime: file.type, folder_id: folderId },
  })
  let offset = 0
  let fails = 0
  while (offset < file.size) {
    try {
      const r = await putChunk(u.id, offset, file.slice(offset, offset + u.chunk_size),
        loaded => onState({ phase: 'upload', done: offset + loaded, total: file.size }))
      offset = r.received
      fails = 0
    } catch (e) {
      if (e.status === 400 || e.status === 404 || ++fails > 5) throw e
      await sleep(2000 * fails)
      // The chunk may have arrived even though the response did not: ask where to continue.
      offset = (await api(`/api/uploads/${u.id}`)).received
    }
  }
  for (;;) {
    const s = await api(`/api/uploads/${u.id}`)
    if (s.status === 'done') return s
    if (s.status === 'error') throw new Error(s.error || 'Gagal mengirim ke Telegram')
    onState({ phase: 'telegram', done: s.sent, total: s.size })
    await sleep(1000)
  }
}
