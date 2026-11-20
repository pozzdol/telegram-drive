// Gallery: every photo and video in the drive, grouped by the day it was taken
// (EXIF date for JPEGs, upload date otherwise), newest first.
import { useEffect, useRef, useState } from 'react'
import { Images, Play } from 'lucide-react'
import { api } from './api.js'
import { MissingBadge, PhotoSkeleton, Thumb, kind } from './ui.jsx'

const PAGE = 100

function dayLabel(ts) {
  const d = new Date(ts * 1000)
  const today = new Date()
  const yesterday = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1)
  if (d.toDateString() === today.toDateString()) return 'Hari ini'
  if (d.toDateString() === yesterday.toDateString()) return 'Kemarin'
  return d.toLocaleDateString('id-ID', {
    weekday: 'long', day: 'numeric', month: 'long',
    year: d.getFullYear() === today.getFullYear() ? undefined : 'numeric',
  })
}

export default function Photos({ version, onOpen, onUpload, onError }) {
  const [files, setFiles] = useState(null)
  const [next, setNext] = useState('') // page token of the next page, '' at the end
  const busy = useRef(false)
  const sentinel = useRef(null)

  const load = async token => {
    if (busy.current) return
    busy.current = true
    try {
      const d = await api(`/api/media?limit=${PAGE}${token ? `&page_token=${encodeURIComponent(token)}` : ''}`)
      setFiles(fs => (token && fs ? [...fs, ...d.files] : d.files))
      setNext(d.next_page_token || '')
    } catch (e) {
      onError(e.message)
    } finally {
      busy.current = false
    }
  }

  useEffect(() => { load('') }, [version]) // eslint-disable-line react-hooks/exhaustive-deps

  // Infinite scroll: fetch the next page well before the end comes into view.
  useEffect(() => {
    if (!next || !sentinel.current) return
    const io = new IntersectionObserver(es => { if (es[0].isIntersecting) load(next) }, { rootMargin: '1200px' })
    io.observe(sentinel.current)
    return () => io.disconnect()
  }, [next, files]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!files) return <section className="panel photos"><PhotoSkeleton /><PhotoSkeleton tiles={9} /></section>

  if (!files.length) {
    return (
      <section className="panel photos">
        <div className="empty">
          <Images size={40} aria-hidden />
          <h2>Belum ada foto atau video</h2>
          <p className="muted">Semua foto dan video yang kamu upload, di folder mana pun, tampil di sini.</p>
          <button className="btn primary" onClick={onUpload}>Upload foto</button>
        </div>
      </section>
    )
  }

  // Consecutive files from the same day form one group; indexes stay global for the lightbox.
  const groups = []
  files.forEach((f, i) => {
    const label = dayLabel(f.taken_at ?? f.created_at)
    if (groups.at(-1)?.label !== label) groups.push({ label, items: [] })
    groups.at(-1).items.push({ f, i })
  })

  return (
    <section className="panel photos" aria-label="Foto">
      {groups.map(g => (
        <div key={g.label} className="day">
          <h2 className="day-title">{g.label}</h2>
          <ul className="photo-grid">
            {g.items.map(({ f, i }) => (
              <li key={f.id}>
                <button className={`tile${f.missing ? ' missing' : ''}`} onClick={() => onOpen(files, i)} aria-label={f.name}>
                  <Thumb file={f} iconSize={32} />
                  {f.missing && <span className="tile-warn"><MissingBadge compact /></span>}
                  {kind(f.mime) === 'video' && !f.missing && <span className="play" aria-hidden><Play size={16} fill="currentColor" /></span>}
                </button>
              </li>
            ))}
          </ul>
        </div>
      ))}
      {next && <div ref={sentinel}><PhotoSkeleton tiles={9} title={false} /></div>}
    </section>
  )
}
