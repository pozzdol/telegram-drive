// Full-screen viewer for photos, videos and audio, with prev/next, keys and swipe.
import { Suspense, lazy, useEffect, useRef, useState } from 'react'
import {
  ArrowLeft, ChevronLeft, ChevronRight, Download, FlipHorizontal2, MessageSquare, FlipVertical2, Info, RotateCcw, RotateCw,
  Trash2, TriangleAlert, ZoomIn, ZoomOut,
} from 'lucide-react'
import { api, fileURL, thumbURL } from './api.js'
import { fmtDate, fmtDateTime, fmtSize, hasThumb, kind, textKind, useModal } from './ui.jsx'
import Player from './Player.jsx'
import Comments from './Comments.jsx'
// Markdown libraries load only when a text file is opened.
const TextPreview = lazy(() => import('./TextPreview.jsx'))

// readOnly: previewing from the trash, so no comments (they work again after a restore).
export default function Lightbox({ items, start, onClose, onDelete, onMissing, onChanged, canDownload = true, readOnly = false }) {
  const ref = useModal()
  const [i, setI] = useState(start)
  const [side, setSide] = useState(null) // 'info' | 'comments'
  const toggle = p => setSide(s => (s === p ? null : p))
  const [counts, setCounts] = useState({}) // comment counts seen in the panel, newer than items
  const swipe = useRef(null)
  const [gone, setGone] = useState(() => new Set(items.filter(x => x.missing).map(x => x.id)))
  // The image or video failed: ask whether the message still exists in Telegram.
  const failed = async file => {
    try {
      if ((await api(`/api/files/${file.id}/check`, { method: 'POST' })).missing) {
        setGone(s => new Set(s).add(file.id))
        onMissing?.(file)
      }
    } catch { /* keep showing the normal error state */ }
  }
  const f = items[Math.min(i, items.length - 1)]

  // The back button (browser, Android) closes the viewer instead of leaving the page, as in
  // Google Drive: opening it adds a history entry, and closing it some other way removes it.
  const byBack = useRef(false)
  // showModal() focuses the first button (back), and Safari then draws its focus ring even for a
  // tap. Focus the viewer itself instead; Tab still reaches the buttons, with the ring.
  useEffect(() => { ref.current?.focus({ preventScroll: true }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!history.state?.lightbox) history.pushState({ ...history.state, lightbox: true }, '')
    const onPop = () => { byBack.current = true; ref.current?.close() }
    addEventListener('popstate', onPop)
    return () => removeEventListener('popstate', onPop)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const closed = () => {
    if (!byBack.current && history.state?.lightbox) history.back()
    onClose()
  }

  // The list shrinks when a file is deleted from here.
  useEffect(() => {
    if (!items.length) ref.current.close()
    else if (i >= items.length) setI(items.length - 1)
  }, [items, i]) // eslint-disable-line react-hooks/exhaustive-deps

  // Preview of the neighbours is cheap (thumbnails), so warm them up.
  useEffect(() => {
    for (const n of [items[i - 1], items[i + 1]]) if (n && hasThumb(n.mime)) new Image().src = thumbURL(n)
  }, [i, items])

  if (!f) return <dialog ref={ref} className="lightbox" onClose={closed} />

  const go = d => setI(x => Math.min(items.length - 1, Math.max(0, x + d)))
  const media = kind(f.mime)
  const k = ['image', 'video', 'audio'].includes(media) ? media : textKind(f) ? 'text' : media

  return (
    <dialog
      ref={ref}
      tabIndex={-1}
      className="lightbox"
      onClose={closed}
      onKeyDown={e => {
        if (e.target.closest?.('textarea, input')) return // typing a comment
        if (e.key === 'ArrowLeft') go(-1)
        if (e.key === 'ArrowRight') go(1)
      }}
    >
      <header className="lb-bar">
        <button className="icon-btn" onClick={() => ref.current.close()} aria-label="Kembali"><ArrowLeft size={22} /></button>
        <span className="lb-name" data-tip={f.name} data-tip-overflow>{f.name}</span>
        {!readOnly && <button className="icon-btn lb-cmt-btn" onClick={() => toggle('comments')} aria-label={`Komentar (${counts[f.id] ?? f.comments ?? 0})`} aria-pressed={side === 'comments'} data-tip="Komentar">
          <MessageSquare size={20} />{(counts[f.id] ?? f.comments) > 0 && <span className="lb-cmt-n">{counts[f.id] ?? f.comments}</span>}
        </button>}
        <button className="icon-btn" onClick={() => toggle('info')} aria-label="Info" aria-pressed={side === 'info'}><Info size={20} /></button>
        {canDownload && <a className="icon-btn" href={fileURL(f.id)} aria-label="Download"><Download size={20} /></a>}
        {onDelete && <button className="icon-btn" onClick={() => onDelete(f)} aria-label="Pindahkan ke sampah" data-tip="Pindahkan ke sampah"><Trash2 size={20} /></button>}
      </header>

      <div
        onPointerDown={e => { swipe.current = e.clientX }}
        className={`lb-stage${k === 'text' || k === 'image' ? ` ${k}` : ''}`}
        onPointerUp={e => {
          if (k === 'text') return // let text scroll sideways without changing file
          if (e.target.closest?.('.player-bar')) return // dragging the seek bar is not a swipe
          const dx = e.clientX - (swipe.current ?? e.clientX)
          if (Math.abs(dx) > 60 && e.pointerType !== 'mouse') go(dx < 0 ? 1 : -1)
          swipe.current = null
        }}
      >
        {gone.has(f.id) && (
          <div className="lb-missing" role="alert">
            <TriangleAlert size={32} aria-hidden />
            <h2>File ini sudah tidak ada di Telegram</h2>
            <p>Pesannya terhapus dari channel penyimpanan, jadi isinya tidak bisa dibuka lagi. Yang tersisa hanya catatannya di aplikasi.</p>
            {onDelete && <button className="btn danger-fill" onClick={() => onDelete(f)}><Trash2 size={18} aria-hidden /> Hapus dari aplikasi</button>}
          </div>
        )}
        {!gone.has(f.id) && k === 'text' && (
          <Suspense fallback={<div className="doc-sheet" aria-busy="true" />}>
            <TextPreview key={f.id} file={f} markdown={textKind(f) === 'markdown'}
              onMissing={() => { setGone(s => new Set(s).add(f.id)); onMissing?.(f) }} />
          </Suspense>
        )}
        {!gone.has(f.id) && k === 'image' && <ZoomImage key={f.id} file={f} onError={() => failed(f)} />}
        {!gone.has(f.id) && (k === 'video' || k === 'audio') && (
          <Player key={f.id} src={fileURL(f.id, true)} poster={k === 'video' ? thumbURL(f) : undefined}
            audio={k === 'audio'} label={f.name} onError={() => failed(f)} />
        )}
      </div>

      {i > 0 && <button className="lb-nav prev" onClick={() => go(-1)} aria-label="Sebelumnya"><ChevronLeft size={28} /></button>}
      {i < items.length - 1 && <button className="lb-nav next" onClick={() => go(1)} aria-label="Berikutnya"><ChevronRight size={28} /></button>}

      {side === 'comments' && <Comments file={f} onChanged={onChanged} onCount={n => setCounts(c => ({ ...c, [f.id]: n }))} />}
      {side === 'info' && (
        <aside className="lb-info" aria-label="Info file">
          <dl>
            <dt>Nama</dt><dd>{f.name}</dd>
            {f.taken_at && <><dt>Diambil</dt><dd>{fmtDateTime(f.taken_at)}</dd></>}
            <dt>Ditambahkan</dt><dd>{fmtDate(f.created_at)}</dd>
            <dt>Ukuran</dt><dd>{fmtSize(f.size)}</dd>
            <dt>Jenis</dt><dd>{f.mime}</dd>
          </dl>
        </aside>
      )}
      <p className={`lb-count${k === 'image' && !gone.has(f.id) ? ' up' : ''}`} aria-live="polite">{i + 1} dari {items.length}</p>
    </dialog>
  )
}

// Image with zoom (wheel, pinch, double-click), pan, rotate and flip, and a toolbar for the same.
const MAX_ZOOM = 8
const START = { s: 1, r: 0, fx: 1, fy: 1, x: 0, y: 0 }

function ZoomImage({ file, onError }) {
  const [loaded, setLoaded] = useState(false)
  const [t, setT] = useState(START)
  const [dragging, setDragging] = useState(false)
  const box = useRef(null)
  const now = useRef(t) // latest value for pointer handlers
  now.current = t
  const pts = useRef(new Map()) // pointers down: id -> [x, y]
  const g = useRef(null) // gesture in progress
  const pan = useRef(null)
  const img = useRef(null)
  // Turned a quarter, a wide photo is taller than the screen: shrink it to fit again.
  const [fit, setFit] = useState(1)
  const quarter = Math.abs(t.r / 90) % 2 === 1
  useEffect(() => {
    const measure = () => {
      const el = img.current, box = pan.current
      if (!quarter || !el?.offsetWidth) return setFit(1)
      setFit(Math.min(1, box.clientWidth / el.offsetHeight, box.clientHeight / el.offsetWidth))
    }
    measure()
    addEventListener('resize', measure)
    return () => removeEventListener('resize', measure)
  }, [quarter, loaded])

  // Point relative to the centre of the stage, where the transform origin is.
  const rel = (cx, cy) => {
    const r = box.current.getBoundingClientRect(), p = pan.current // offsets ignore the transform
    return [cx - r.left - p.offsetLeft - p.offsetWidth / 2, cy - r.top - p.offsetTop - p.offsetHeight / 2]
  }
  // Zoom so that point p stays under the cursor or fingers.
  const zoomed = (o, s, [px, py]) => {
    s = Math.min(MAX_ZOOM, Math.max(1, s))
    if (s === 1) return { ...o, s, x: 0, y: 0 }
    const k = s / o.s
    return { ...o, s, x: px - (px - o.x) * k, y: py - (py - o.y) * k }
  }
  const zoomBy = f => setT(o => zoomed(o, o.s * f, [0, 0]))
  // Flipped once, a visual clockwise turn is a counter-clockwise one in the element's own axes.
  const rotate = d => setT(o => ({ ...o, r: o.r + d * o.fx * o.fy }))
  const reset = () => setT(START)

  useEffect(() => {
    const el = box.current
    const wheel = e => {
      e.preventDefault()
      const p = rel(e.clientX, e.clientY)
      setT(o => zoomed(o, o.s * Math.exp(-e.deltaY * 0.002), p))
    }
    const keys = e => {
      if (e.target.closest?.('input, textarea')) return
      if (e.key === '+' || e.key === '=') zoomBy(1.5)
      else if (e.key === '-') zoomBy(1 / 1.5)
      else if (e.key === '0') reset()
      else if (e.key === 'r') rotate(90)
    }
    el.addEventListener('wheel', wheel, { passive: false }) // React's wheel listener is passive
    document.addEventListener('keydown', keys)
    return () => { el.removeEventListener('wheel', wheel); document.removeEventListener('keydown', keys) }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const begin = multi => {
    const p = [...pts.current.values()]
    const o = now.current
    if (p.length >= 2) {
      const mid = rel((p[0][0] + p[1][0]) / 2, (p[0][1] + p[1][1]) / 2)
      g.current = { pinch: true, multi: true, d: Math.hypot(p[0][0] - p[1][0], p[0][1] - p[1][1]), o, mid }
    } else if (p.length === 1) {
      g.current = { multi, px: p[0][0], py: p[0][1], o }
    } else g.current = null
  }
  const down = e => {
    e.currentTarget.setPointerCapture(e.pointerId)
    pts.current.set(e.pointerId, [e.clientX, e.clientY])
    begin(g.current?.multi || pts.current.size > 1)
  }
  const move = e => {
    if (!pts.current.has(e.pointerId)) return
    pts.current.set(e.pointerId, [e.clientX, e.clientY])
    const G = g.current
    if (!G) return
    if (G.pinch) {
      const [a, b] = [...pts.current.values()]
      const s = Math.min(MAX_ZOOM, Math.max(1, G.o.s * Math.hypot(a[0] - b[0], a[1] - b[1]) / G.d))
      const [mx, my] = rel((a[0] + b[0]) / 2, (a[1] + b[1]) / 2)
      const k = s / G.o.s
      setT(s === 1 ? { ...G.o, s, x: 0, y: 0 } : { ...G.o, s, x: mx - (G.mid[0] - G.o.x) * k, y: my - (G.mid[1] - G.o.y) * k })
      setDragging(true)
    } else if (G.o.s > 1) {
      setT({ ...G.o, x: G.o.x + e.clientX - G.px, y: G.o.y + e.clientY - G.py })
      setDragging(true)
    }
  }
  const up = e => {
    if (!pts.current.delete(e.pointerId)) return
    // A pan or pinch is not a swipe to the next file.
    if (g.current?.multi || now.current.s > 1) e.stopPropagation()
    begin(g.current?.multi)
    if (!pts.current.size) setDragging(false)
  }

  const { s, r, fx, fy, x, y } = t
  const changed = s !== 1 || r % 360 !== 0 || fx !== 1 || fy !== 1
  return (
    <>
      <div ref={box} className={`lb-zoom${s > 1 ? ' zoomed' : ''}${dragging ? ' dragging' : ''}`}
        onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up}
        onDoubleClick={e => setT(o => (o.s > 1 ? { ...o, s: 1, x: 0, y: 0 } : zoomed(o, 2.5, rel(e.clientX, e.clientY))))}>
        <div ref={pan} className="lb-pan" style={{ transform: `translate(${x}px, ${y}px) scale(${s * fit}) scale(${fx}, ${fy}) rotate(${r}deg)` }}>
          {hasThumb(file.mime) && !loaded && <img className="lb-img lb-low" src={thumbURL(file)} alt="" draggable={false} />}
          <img ref={img} className={`lb-img${loaded ? '' : ' loading'}`} src={fileURL(file.id, true)} alt={file.name} draggable={false}
            onLoad={() => setLoaded(true)} onError={onError} />
        </div>
      </div>
      <div className="lb-tools" role="toolbar" aria-label="Atur tampilan gambar">
        <button className="icon-btn" onClick={() => setT(o => ({ ...o, fx: -o.fx }))} data-tip="Balik kiri-kanan" aria-label="Balik kiri-kanan"><FlipHorizontal2 size={18} /></button>
        <button className="icon-btn" onClick={() => setT(o => ({ ...o, fy: -o.fy }))} data-tip="Balik atas-bawah" aria-label="Balik atas-bawah"><FlipVertical2 size={18} /></button>
        <button className="icon-btn" onClick={() => rotate(-90)} data-tip="Putar ke kiri" aria-label="Putar ke kiri"><RotateCcw size={18} /></button>
        <button className="icon-btn" onClick={() => rotate(90)} data-tip="Putar ke kanan (R)" aria-label="Putar ke kanan"><RotateCw size={18} /></button>
        <span className="lb-sep" aria-hidden />
        <button className="icon-btn" onClick={() => zoomBy(1 / 1.5)} disabled={s <= 1} data-tip="Perkecil (−)" aria-label="Perkecil"><ZoomOut size={18} /></button>
        <button className="lb-pct" onClick={reset} disabled={!changed} data-tip="Kembalikan ke awal (0)" aria-label={`Zoom ${Math.round(s * 100)}%, kembalikan ke awal`}>
          {Math.round(s * 100)}%
        </button>
        <button className="icon-btn" onClick={() => zoomBy(1.5)} disabled={s >= MAX_ZOOM} data-tip="Perbesar (+)" aria-label="Perbesar"><ZoomIn size={18} /></button>
      </div>
    </>
  )
}
