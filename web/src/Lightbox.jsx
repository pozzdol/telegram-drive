// Full-screen viewer for photos, videos and audio, with prev/next, keys and swipe.
import { useEffect, useRef, useState } from 'react'
import { ArrowLeft, ChevronLeft, ChevronRight, Download, Info, Trash2 } from 'lucide-react'
import { fileURL, thumbURL } from './api.js'
import { fmtDate, fmtDateTime, fmtSize, hasThumb, kind, useModal } from './ui.jsx'

export default function Lightbox({ items, start, onClose, onDelete }) {
  const ref = useModal()
  const [i, setI] = useState(start)
  const [loaded, setLoaded] = useState(false)
  const [info, setInfo] = useState(false)
  const swipe = useRef(null)
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
  const k = kind(f.mime)

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
        className="lb-stage"
        onPointerDown={e => { swipe.current = e.clientX }}
        onPointerUp={e => {
          const dx = e.clientX - (swipe.current ?? e.clientX)
          if (Math.abs(dx) > 60 && e.pointerType !== 'mouse') go(dx < 0 ? 1 : -1)
          swipe.current = null
        }}
      >
        {k === 'image' && (
          <>
            {hasThumb(f.mime) && !loaded && <img className="lb-img lb-low" src={thumbURL(f)} alt="" />}
            <img key={f.id} className={`lb-img${loaded ? '' : ' loading'}`} src={fileURL(f.id, true)} alt={f.name} onLoad={() => setLoaded(true)} />
          </>
        )}
        {k === 'video' && <video key={f.id} className="lb-img" src={fileURL(f.id, true)} poster={thumbURL(f)} controls autoPlay playsInline />}
        {k === 'audio' && <audio key={f.id} src={fileURL(f.id, true)} controls autoPlay />}
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
