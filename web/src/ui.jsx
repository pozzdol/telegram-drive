// Shared building blocks: formatting, file icons, thumbnails, menus, dialogs, upload panel.
import { Fragment, useEffect, useLayoutEffect, useRef, useState } from 'react'
import {
  ChevronDown, ChevronRight, ChevronUp, CircleAlert, CircleCheck, Eye, EyeOff, File as FileIcon, FileArchive,
  FileAudio, FileImage, FileText, FileVideo, Folder, X,
} from 'lucide-react'
import { api, fileURL, thumbURL } from './api.js'
import { makeVideoThumb } from './videoThumb.js'

export const fmtSize = n => {
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${i ? n.toLocaleString('id-ID', { maximumFractionDigits: 1 }) : n} ${u[i]}`
}
export const fmtDate = s => new Date(s * 1000).toLocaleDateString('id-ID', { day: 'numeric', month: 'short', year: 'numeric' })
export const fmtDateTime = s => new Date(s * 1000).toLocaleString('id-ID', { dateStyle: 'long', timeStyle: 'short' })

// Same rule as the backend's inlineSafe: what the browser may show instead of download.
export const kind = m =>
  m.startsWith('image/') && !m.includes('svg') ? 'image'
    : m.startsWith('video/') ? 'video'
      : m.startsWith('audio/') ? 'audio'
        : m === 'application/pdf' || m.startsWith('text/plain') ? 'doc'
          : 'other'
export const isViewable = f => ['image', 'video', 'audio'].includes(kind(f.mime))
export const hasThumb = m => ['image/jpeg', 'image/png', 'image/gif', 'image/webp'].includes(m)
const hasPreview = m => hasThumb(m) || m.startsWith('video/')

export function KindIcon({ mime, size = 20 }) {
  const k = kind(mime)
  const Icon = k === 'image' ? FileImage : k === 'video' ? FileVideo : k === 'audio' ? FileAudio
    : k === 'doc' ? FileText : /zip|rar|7z|tar|gzip/.test(mime) ? FileArchive : FileIcon
  return <Icon size={size} className={`ic k-${k}`} aria-hidden />
}

export function FolderIcon({ size = 20 }) {
  return <Folder size={size} className="ic k-folder" aria-hidden />
}

// Stored preview; falls back to the type icon when there is none.
// A video without one gets it made here, from a frame the browser decodes.
export function Thumb({ file, iconSize = 36 }) {
  const [failed, setFailed] = useState(false)
  const [retry, setRetry] = useState(0)
  if (!hasPreview(file.mime) || failed) {
    return <div className={`thumb-fallback k-bg-${kind(file.mime)}`}><KindIcon mime={file.mime} size={iconSize} /></div>
  }
  const onError = async () => {
    if (kind(file.mime) === 'video' && !retry && await makeVideoThumb(file.id, fileURL(file.id, true))) setRetry(1)
    else setFailed(true)
  }
  const src = thumbURL(file) + (retry ? '&r=1' : '')
  return <img className="thumb" src={src} alt="" loading="lazy" decoding="async" onError={onError} />
}

// Opens a native <dialog> as modal: Esc, focus trap and backdrop come for free.
export function useModal() {
  const ref = useRef(null)
  useEffect(() => { if (!ref.current.open) ref.current.showModal() }, [])
  return ref
}

// Context menu at a viewport point. items: { label, icon, onSelect, danger } or '-' for a divider.
export function Menu({ at, items, onClose }) {
  const ref = useRef(null)
  const [pos, setPos] = useState({ left: at.x, top: at.y, visibility: 'hidden' })

  useLayoutEffect(() => {
    const r = ref.current.getBoundingClientRect()
    const top = at.y + r.height > innerHeight - 8 ? at.y - r.height : at.y
    setPos({ left: Math.max(8, Math.min(at.x, innerWidth - r.width - 8)), top: Math.max(8, top) })
    ref.current.querySelector('button')?.focus()
  }, [at])

  useEffect(() => {
    const down = e => { if (!ref.current?.contains(e.target)) onClose() }
    const key = e => { if (e.key === 'Escape') onClose() }
    addEventListener('pointerdown', down, true)
    addEventListener('keydown', key)
    addEventListener('resize', onClose)
    return () => {
      removeEventListener('pointerdown', down, true)
      removeEventListener('keydown', key)
      removeEventListener('resize', onClose)
    }
  }, [onClose])

  const arrows = e => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const bs = [...ref.current.querySelectorAll('button')]
    const i = bs.indexOf(document.activeElement)
    bs[(i + (e.key === 'ArrowDown' ? 1 : -1) + bs.length) % bs.length].focus()
  }

  return (
    <div ref={ref} className="menu" role="menu" style={pos} onKeyDown={arrows}>
      {items.map((it, i) => it === '-' ? <hr key={i} /> : (
        <button key={i} role="menuitem" className={it.danger ? 'danger' : undefined} onClick={() => { onClose(); it.onSelect() }}>
          <it.icon size={18} aria-hidden /> {it.label}
        </button>
      ))}
    </div>
  )
}

// Prompt or confirm. ask: { title, message?, input?, password?, value?, selectBase?, confirm, danger? }.
// password: the value is hidden, with an eye button to show it.
// onDone gets the trimmed text (input), true (confirm) or null (cancelled).
export function AskDialog({ ask, onDone }) {
  const ref = useModal()
  const result = useRef(null)
  const input = useRef(null)
  const [value, setValue] = useState(ask.value ?? '')
  const [shown, setShown] = useState(false)

  useEffect(() => {
    if (!ask.input) return
    // Select the name without its extension, so typing replaces just the name.
    const dot = value.lastIndexOf('.')
    input.current.setSelectionRange(0, ask.selectBase && dot > 0 ? dot : value.length)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const submit = e => {
    e.preventDefault()
    result.current = ask.input ? value.trim() : true
    ref.current.close()
  }
  return (
    <dialog ref={ref} className="sheet ask" onClose={() => onDone(result.current)}>
      <form onSubmit={submit}>
        <h2>{ask.title}</h2>
        {ask.message && <p className="muted">{ask.message}</p>}
        {ask.input && !ask.password && (
          <input ref={input} autoFocus value={value} onChange={e => setValue(e.target.value)} aria-label={ask.title} maxLength={255} />
        )}
        {ask.input && ask.password && (
          <div className="pw-field">
            <input ref={input} autoFocus value={value} onChange={e => setValue(e.target.value)} aria-label={ask.title}
              type={shown ? 'text' : 'password'} autoComplete="new-password" maxLength={255} />
            <button type="button" className="icon-btn" onClick={() => setShown(s => !s)}
              aria-label={shown ? 'Sembunyikan password' : 'Tampilkan password'} aria-pressed={shown}>
              {shown ? <EyeOff size={18} /> : <Eye size={18} />}
            </button>
          </div>
        )}
        <footer>
          <button type="button" className="btn ghost" onClick={() => ref.current.close()}>Batal</button>
          <button className={`btn ${ask.danger ? 'danger-fill' : 'primary'}`} disabled={ask.input && !value.trim()}>{ask.confirm}</button>
        </footer>
      </form>
    </dialog>
  )
}

export function Crumbs({ path, onGo, root = 'Drive saya', small }) {
  return (
    <nav className={`crumbs${small ? ' small' : ''}`} aria-label="Lokasi folder">
      <button onClick={() => onGo(0)} aria-current={path.length ? undefined : 'page'}>{root}</button>
      {path.map((c, i) => (
        <Fragment key={c.id}>
          <ChevronRight size={small ? 14 : 18} className="sep" aria-hidden />
          <button onClick={() => onGo(c.id)} aria-current={i === path.length - 1 ? 'page' : undefined}>{c.name}</button>
        </Fragment>
      ))}
    </nav>
  )
}

export function MoveDialog({ target, onMove, onClose }) {
  const ref = useModal()
  const [at, setAt] = useState(0)
  const [d, setD] = useState(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setD(null)
    api(`/api/list?folder_id=${at}`).then(setD, e => setErr(e.message))
  }, [at])

  // A folder cannot go into itself; the backend also blocks moving into its subfolders.
  const folders = d?.folders.filter(f => !(target.type === 'folder' && f.id === target.item.id)) || []
  const submit = async () => {
    setBusy(true)
    setErr('')
    try { await onMove(at); ref.current.close() } catch (e) { setErr(e.message) } finally { setBusy(false) }
  }

  return (
    <dialog ref={ref} className="sheet move" onClose={onClose}>
      <h2>Pindahkan “{target.item.name}”</h2>
      <Crumbs path={d?.path || []} onGo={setAt} small />
      <ul className="picker">
        {!d ? <li className="muted">Memuat…</li> : folders.length ? folders.map(f => (
          <li key={f.id}><button onClick={() => setAt(f.id)}><FolderIcon size={18} /> {f.name}</button></li>
        )) : <li className="muted">Tidak ada subfolder di sini</li>}
      </ul>
      {err && <p className="err" role="alert">{err}</p>}
      <footer>
        <button className="btn ghost" onClick={() => ref.current.close()}>Batal</button>
        <button className="btn primary" onClick={submit} disabled={busy || !d}>Pindahkan ke sini</button>
      </footer>
    </dialog>
  )
}

export function UploadPanel({ uploads, onClear }) {
  const [open, setOpen] = useState(true)
  if (!uploads.length) return null
  const active = uploads.filter(u => u.status === 'active').length
  const failed = uploads.filter(u => u.status === 'error').length
  const title = active ? `Mengupload ${active} file`
    : failed ? `${failed} upload gagal` : `${uploads.length} upload selesai`
  return (
    <aside className="uploads" aria-label="Upload" aria-live="polite">
      <header>
        <strong>{title}</strong>
        <button className="icon-btn" onClick={() => setOpen(o => !o)} aria-label={open ? 'Ciutkan' : 'Bentangkan'} aria-expanded={open}>
          {open ? <ChevronDown size={18} /> : <ChevronUp size={18} />}
        </button>
        {!active && <button className="icon-btn" onClick={onClear} aria-label="Tutup"><X size={18} /></button>}
      </header>
      {open && (
        <ul>
          {uploads.map(u => {
            const pct = u.status === 'done' ? 100 : u.total ? Math.floor((u.done / u.total) * 100) : 0
            const label = u.status === 'error' ? u.error
              : u.status === 'done' ? fmtSize(u.size)
                : u.phase === 'queued' ? 'Menunggu giliran'
                  : u.phase === 'telegram' ? `Menyimpan ke Telegram ${pct}%`
                    : `Mengirim ke server ${pct}%`
            return (
              <li key={u.key}>
                <div className="u-row">
                  <span className="fname" title={u.name}>{u.name}</span>
                  {u.status === 'done' && <CircleCheck size={18} className="ok" aria-label="Selesai" />}
                  {u.status === 'error' && <CircleAlert size={18} className="bad" aria-label="Gagal" />}
                </div>
                <div className="bar" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={u.name}>
                  <div className={`fill ${u.status} ${u.phase}`} style={{ width: `${pct}%` }} />
                </div>
                <div className={`small ${u.status === 'error' ? 'bad' : 'muted'}`}>{label}</div>
              </li>
            )
          })}
        </ul>
      )}
    </aside>
  )
}
