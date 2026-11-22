// Version history of a document, beside the viewer. Selecting a version shows it in the viewer
// (read-only); an editor restores or names it, the owner deletes it.
import { useEffect, useState } from 'react'
import { Download, MoreVertical } from 'lucide-react'
import { api, versionURL } from './api.js'
import { fmtSize, Menu } from './ui.jsx'

const day = s => new Date(s * 1000).toLocaleDateString('id-ID', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })
const time = s => new Date(s * 1000).toLocaleTimeString('id-ID', { hour: '2-digit', minute: '2-digit' })

export default function Versions({ file, viewing, onView, onRestored, onAsk, reload, canDownload }) {
  const [d, setD] = useState(null) // { versions, role }
  const [err, setErr] = useState('')
  const [menu, setMenu] = useState(null)

  const load = () => api(`/api/files/${file.id}/versions`).then(setD, e => setErr(e.message))
  useEffect(() => { setD(null); setErr(''); load() }, [file.id, reload]) // eslint-disable-line react-hooks/exhaustive-deps

  const edit = d && ['owner', 'editor'].includes(d.role)
  const run = async fn => {
    setErr('')
    try { await fn(); await load() } catch (e) { setErr(e.message) }
  }
  const restore = v => run(async () => {
    await api(`/api/files/${file.id}/versions/${v.id}/restore`, { method: 'POST' })
    onView(null)
    onRestored()
  })
  const name = async v => {
    const label = await onAsk({ title: v.label ? 'Ganti nama versi' : 'Beri nama versi', input: true, value: v.label || '', confirm: 'Simpan',
      message: 'Versi yang diberi nama tidak dihapus otomatis.' })
    if (label != null) run(() => api(`/api/files/${file.id}/versions/${v.id}`, { method: 'PATCH', body: { label: label || null } }))
  }
  const remove = async v => {
    const ok = await onAsk({ title: 'Hapus versi ini?', message: `Versi ${day(v.created_at)} ${time(v.created_at)} dihapus untuk selamanya.`, confirm: 'Hapus', danger: true })
    if (ok) run(async () => {
      await api(`/api/files/${file.id}/versions/${v.id}`, { method: 'DELETE' })
      if (viewing?.id === v.id) onView(null)
    })
  }
  const open = (e, v) => {
    const r = e.currentTarget.getBoundingClientRect()
    const items = [
      !v.current && edit && { label: 'Pulihkan versi ini', onSelect: () => restore(v) },
      edit && { label: v.label ? 'Ganti nama' : 'Beri nama', onSelect: () => name(v) },
      v.label && edit && { label: 'Hapus nama', onSelect: () => run(() => api(`/api/files/${file.id}/versions/${v.id}`, { method: 'PATCH', body: { label: null } })) },
      canDownload && { label: 'Download', icon: Download, onSelect: () => location.assign(versionURL(file.id, v.id)) },
      !v.current && d.role === 'owner' && '-',
      !v.current && d.role === 'owner' && { label: 'Hapus versi', danger: true, onSelect: () => remove(v) },
    ].filter(Boolean)
    setMenu({ at: { x: r.right - 220, y: r.bottom + 4 }, items })
  }

  // Grouped by day, newest first.
  const groups = []
  for (const v of d?.versions ?? []) {
    const k = day(v.created_at)
    if (groups.at(-1)?.day !== k) groups.push({ day: k, items: [] })
    groups.at(-1).items.push(v)
  }

  return (
    <aside className="lb-comments lb-versions" aria-label="Riwayat versi">
      <h2 className="lb-side-title">Riwayat versi</h2>
      <div className="cmt-list">
        {!d ? <p className="muted small">Memuat…</p> : groups.map(g => (
          <section key={g.day} className="ver-day">
            <h3 className="muted small">{g.day}</h3>
            <ul>
              {g.items.map(v => {
                const on = viewing ? viewing.id === v.id : v.current
                return (
                  <li key={v.id} className={`ver${on ? ' on' : ''}`}>
                    <button className="ver-main" aria-pressed={on} onClick={() => onView(v.current ? null : v)}>
                      <span className="ver-when">{v.label || time(v.created_at)}{v.current && <span className="ver-tag">Versi sekarang</span>}</span>
                      {v.label && <span className="muted small">{time(v.created_at)}</span>}
                      <span className="muted small">{v.by} · {fmtSize(v.size)}</span>
                    </button>
                    <button className="icon-btn" onClick={e => open(e, v)} aria-label="Pilihan versi" aria-haspopup="menu"><MoreVertical size={18} /></button>
                  </li>
                )
              })}
            </ul>
          </section>
        ))}
      </div>
      {err && <p className="err small" role="alert">{err}</p>}
      {d && <p className="muted small">Versi tanpa nama disimpan 30 hari, paling banyak 100.</p>}
      {menu && <Menu at={menu.at} items={menu.items} onClose={() => setMenu(null)} />}
    </aside>
  )
}
