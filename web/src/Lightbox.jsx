// Full-screen viewer for photos, videos and audio, with prev/next, keys and swipe.
import { Suspense, lazy, useEffect, useRef, useState } from 'react'
import { ArrowLeft, ChevronLeft, ChevronRight, Download, Info, Trash2, TriangleAlert } from 'lucide-react'
import { api, fileURL, thumbURL } from './api.js'
import { fmtDate, fmtDateTime, fmtSize, hasThumb, kind, textKind, useModal } from './ui.jsx'
// Markdown libraries load only when a text file is opened.
const TextPreview = lazy(() => import('./TextPreview.jsx'))

export default function Lightbox({ items, start, onClose, onDelete, onMissing }) {
  const ref = useModal()
  const [i, setI] = useState(start)
  const [loaded, setLoaded] = useState(false)
  const [info, setInfo] = useState(false)
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

  // The list shrinks when a file is deleted from here.
  useEffect(() => {
    if (!items.length) ref.current.close()
    else if (i >= items.length) setI(items.length - 1)
  }, [items, i]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => setLoaded(false), [f?.id])

  // Preview of the neighbours is cheap (thumbnails), so warm them up.
  useEffect(() => {
    for (const n of [items[i - 1], items[i + 1]]) if (n && hasThumb(n.mime)) new Image().src = thumbURL(n)
  }, [i, items])

  if (!f) return <dialog ref={ref} className="lightbox" onClose={onClose} />

  const go = d => setI(x => Math.min(items.length - 1, Math.max(0, x + d)))
  const media = kind(f.mime)
  const k = ['image', 'video', 'audio'].includes(media) ? media : textKind(f) ? 'text' : media

  return (
    <dialog
      ref={ref}
      className="lightbox"
      onClose={onClose}
      onKeyDown={e => {
        if (e.key === 'ArrowLeft') go(-1)
        if (e.key === 'ArrowRight') go(1)
      }}
    >
      <header className="lb-bar">
        <button className="icon-btn" onClick={() => ref.current.close()} aria-label="Kembali"><ArrowLeft size={22} /></button>
        <span className="lb-name" title={f.name}>{f.name}</span>
        <button className="icon-btn" onClick={() => setInfo(v => !v)} aria-label="Info" aria-pressed={info}><Info size={20} /></button>
        <a className="icon-btn" href={fileURL(f.id)} aria-label="Download"><Download size={20} /></a>
        <button className="icon-btn" onClick={() => onDelete(f)} aria-label="Hapus"><Trash2 size={20} /></button>
      </header>

      <div
        onPointerDown={e => { swipe.current = e.clientX }}
        className={`lb-stage${k === 'text' ? ' text' : ''}`}
        onPointerUp={e => {
          if (k === 'text') return // let text scroll sideways without changing file
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
            <button className="btn danger-fill" onClick={() => onDelete(f)}><Trash2 size={18} aria-hidden /> Hapus dari aplikasi</button>
          </div>
        )}
        {!gone.has(f.id) && k === 'text' && (
          <Suspense fallback={<div className="doc-sheet" aria-busy="true" />}>
            <TextPreview key={f.id} file={f} markdown={textKind(f) === 'markdown'}
              onMissing={() => { setGone(s => new Set(s).add(f.id)); onMissing?.(f) }} />
          </Suspense>
        )}
        {!gone.has(f.id) && k === 'image' && (
          <>
            {hasThumb(f.mime) && !loaded && <img className="lb-img lb-low" src={thumbURL(f)} alt="" />}
            <img key={f.id} className={`lb-img${loaded ? '' : ' loading'}`} src={fileURL(f.id, true)} alt={f.name} onLoad={() => setLoaded(true)} onError={() => failed(f)} />
          </>
        )}
        {!gone.has(f.id) && k === 'video' && <video key={f.id} className="lb-img" src={fileURL(f.id, true)} poster={thumbURL(f)} controls autoPlay playsInline onError={() => failed(f)} />}
        {!gone.has(f.id) && k === 'audio' && <audio key={f.id} src={fileURL(f.id, true)} controls autoPlay onError={() => failed(f)} />}
      </div>

      {i > 0 && <button className="lb-nav prev" onClick={() => go(-1)} aria-label="Sebelumnya"><ChevronLeft size={28} /></button>}
      {i < items.length - 1 && <button className="lb-nav next" onClick={() => go(1)} aria-label="Berikutnya"><ChevronRight size={28} /></button>}

      {info && (
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
      <p className="lb-count" aria-live="polite">{i + 1} dari {items.length}</p>
    </dialog>
  )
}
