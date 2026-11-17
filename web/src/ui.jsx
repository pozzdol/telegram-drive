// Shared building blocks: formatting, file icons, thumbnails, menus, dialogs, upload panel.
import { Fragment, useEffect, useLayoutEffect, useRef, useState } from 'react'
import {
  ChevronDown, ChevronRight, ChevronUp, CircleAlert, CircleCheck, Eye, EyeOff, File as FileIcon, FileArchive,
  FileAudio, FileImage, FileText, FileVideo, Folder, TriangleAlert, X,
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
// Text files are often stored as application/octet-stream (browsers do not know .go, .md, ...),
// so the name decides too.
const TEXT_EXT = /\.(md|markdown|txt|log|csv|tsv|json|ya?ml|toml|ini|conf|cfg|env|xml|sql|sh|bash|go|jsx?|tsx?|py|rb|java|kt|c|h|cpp|hpp|cs|rs|php|css|scss|html?|vue|svelte|gitignore|dockerfile)$/i
const TEXT_MIME = /^text\/|^application\/(json|xml|x-yaml|yaml|javascript|x-sh|sql|toml)/
export const textKind = f =>
  /\.(md|markdown)$/i.test(f.name) || f.mime === 'text/markdown' ? 'markdown'
    : TEXT_MIME.test(f.mime) || TEXT_EXT.test(f.name) ? 'text' : null
export const isViewable = f => ['image', 'video', 'audio'].includes(kind(f.mime)) || textKind(f) !== null
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
  const [making, setMaking] = useState(false)
  if (!hasPreview(file.mime) || failed) {
    return <div className={`thumb-fallback k-bg-${kind(file.mime)}`}><KindIcon mime={file.mime} size={iconSize} /></div>
  }
  const onError = async () => {
    if (kind(file.mime) !== 'video' || retry) return setFailed(true)
    // Making the preview can take a while (it streams part of the video): show a placeholder,
    // not the browser's broken-image icon.
    setMaking(true)
    const ok = await makeVideoThumb(file.id, fileURL(file.id, true))
    setMaking(false)
    ok ? setRetry(1) : setFailed(true)
  }
  if (making) return <div className="thumb sk" aria-hidden />
  const src = thumbURL(file) + (retry ? '&r=1' : '')
  // Shimmers until the picture arrives (thumbnails may come from Telegram on first view).
  return <img className="thumb sk" src={src} alt="" loading="lazy" decoding="async" draggable={false} onError={onError}
    onLoad={e => e.currentTarget.classList.remove('sk')} />
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
  // On phones the path is one sideways-scrolling line; show its end (the current folder).
  const ref = useRef(null)
  useLayoutEffect(() => { ref.current.scrollLeft = ref.current.scrollWidth }, [path])
  return (
    <nav ref={ref} className={`crumbs${small ? ' small' : ''}`} aria-label="Lokasi folder">
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

// items: [{ type, item }]. Starts in the folder the items are in.
export function MoveDialog({ items, onMove, onClose }) {
  const ref = useModal()
  const [at, setAt] = useState(() => (items[0].type === 'file' ? items[0].item.folder_id : items[0].item.parent_id) ?? 0)
  const [d, setD] = useState(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setD(null)
    api(`/api/list?folder_id=${at}`).then(setD, e => setErr(e.message))
  }, [at])

  // A folder cannot go into itself (so it is not offered); the backend also blocks its subfolders.
  const moving = new Set(items.filter(x => x.type === 'folder').map(x => x.item.id))
  const folders = d?.folders.filter(f => !moving.has(f.id)) || []
  const from = items[0].type === 'file' ? items[0].item.folder_id : items[0].item.parent_id
  const same = items.every(x => (x.type === 'file' ? x.item.folder_id : x.item.parent_id) === from)
  const here = same && (from ?? 0) === at // already there: nothing to do
  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      await onMove(at)
      ref.current.close()
    } catch (e) {
      setErr(e.status === 400 ? 'Folder tidak bisa dipindahkan ke dalam dirinya sendiri atau ke subfoldernya.' : e.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <dialog ref={ref} className="sheet move" onClose={onClose}>
      <h2>{items.length === 1 ? <>Pindahkan “{items[0].item.name}”</> : `Pindahkan ${items.length} item`}</h2>
      <Crumbs path={d?.path || []} onGo={setAt} small />
      <ul className="picker">
        {!d ? [70, 45, 60].map((w, i) => <li key={i} className="picker-sk"><Sk w={18} h={18} r={4} /><Sk w={`${w}%`} /></li>)
          : folders.length ? folders.map(f => (
          <li key={f.id}><button onClick={() => setAt(f.id)}><FolderIcon size={18} /> {f.name}</button></li>
        )) : <li className="muted">Tidak ada subfolder di sini</li>}
      </ul>
      {err && <p className="err" role="alert">{err}</p>}
      <footer>
        <button className="btn ghost" onClick={() => ref.current.close()}>Batal</button>
        <button className="btn primary" onClick={submit} disabled={busy || !d || here} title={here ? 'Item sudah ada di folder ini' : undefined}>
          {busy ? 'Memindahkan…' : 'Pindahkan ke sini'}
        </button>
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

// ---- skeletons: grey shapes in the layout of what is loading, so nothing jumps when it arrives ----

// Fixed widths: random ones would change on every render and look like flicker.
const WIDTHS = ['62%', '44%', '71%', '38%', '55%', '66%', '48%', '58%', '35%', '69%']

export const Sk = ({ w = '100%', h = 12, r, className = '' }) => (
  <span className={`sk sk-bar ${className}`} style={{ width: w, height: h, borderRadius: r }} aria-hidden />
)

// Same columns as the file list, so the header and widths stay put when rows arrive.
export function ListSkeleton({ rows = 8 }) {
  return (
    <table className="list" aria-busy="true" aria-label="Memuat isi folder">
      <thead>
        <tr><th>Nama</th><th className="hide-sm">Ditambahkan</th><th className="num hide-xs">Ukuran</th><th><span className="sr">Tindakan</span></th></tr>
      </thead>
      <tbody>
        {Array.from({ length: rows }, (_, i) => (
          <tr key={i} className="sk-row">
            <td><div className="sk-name"><Sk w={20} h={20} r={4} /><Sk w={WIDTHS[i % WIDTHS.length]} /></div></td>
            <td className="hide-sm"><Sk w={80} /></td>
            <td className="num hide-xs"><Sk w={48} /></td>
            <td className="act" />
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export function GridSkeleton() {
  return (
    <div className="grid-view" aria-busy="true" aria-label="Memuat isi folder">
      <h2 className="group-title"><Sk w={64} h={14} /></h2>
      <ul className="folder-grid">
        {[0, 1, 2].map(i => <li key={i} className="folder-chip"><Sk w={20} h={20} r={4} /><Sk w={WIDTHS[i]} /></li>)}
      </ul>
      <h2 className="group-title"><Sk w={40} h={14} /></h2>
      <ul className="file-grid">
        {Array.from({ length: 8 }, (_, i) => (
          <li key={i} className="file-card">
            <div className="card-open sk" />
            <div className="card-foot"><Sk w={18} h={18} r={4} /><Sk w={WIDTHS[i]} /></div>
          </li>
        ))}
      </ul>
    </div>
  )
}

// A day heading and a block of square tiles, like the photo timeline.
export function PhotoSkeleton({ tiles = 18, title = true }) {
  return (
    <div className="day" aria-busy="true" aria-label="Memuat foto">
      {title && <h2 className="day-title"><Sk w={180} h={22} /></h2>}
      <ul className="photo-grid">
        {Array.from({ length: tiles }, (_, i) => <li key={i}><div className="tile sk" /></li>)}
      </ul>
    </div>
  )
}

// Shown on files whose message was deleted in Telegram: only the record is left.
export function MissingBadge({ compact }) {
  return (
    <span className="missing-badge" title="File ini sudah tidak ada di Telegram">
      <TriangleAlert size={14} aria-hidden />{compact ? <span className="sr">Hilang di Telegram</span> : ' Hilang di Telegram'}
    </span>
  )
}
