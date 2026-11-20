// Trash: deleted items wait here before Telegram loses them for good.
import { useEffect, useState } from 'react'
import { EllipsisVertical, LayoutGrid, List, Play, RotateCcw, Trash2 } from 'lucide-react'
import { api } from './api.js'
import { FolderIcon, GridSkeleton, KindIcon, ListSkeleton, Thumb, fmtDate, fmtSize, isViewable, kind } from './ui.jsx'

const DAY = 86400

// "dihapus permanen dalam 3 hari", from when the item was deleted.
const left = (trashedAt, days) => {
  const d = Math.ceil((trashedAt + days * DAY - Date.now() / 1000) / DAY)
  return d <= 1 ? 'Dihapus permanen hari ini' : `Dihapus permanen dalam ${d} hari`
}

// view: 'list' | 'grid', shared with the drive. onOpen(items, start) previews trashed files.
export default function Trash({ version, view, onView, onOpen, onMenu, onAsk, onError, onChanged }) {
  const [d, setD] = useState(null) // { items, days }
  useEffect(() => {
    api('/api/trash').then(setD, e => onError(e.message))
  }, [version]) // eslint-disable-line react-hooks/exhaustive-deps

  const act = async (path, body) => {
    try { await api(path, { method: 'POST', body }); onChanged() } catch (e) { onError(e.message); onChanged() }
  }
  const restore = it => act('/api/trash/restore', { ids: [it.id] })
  const purge = async it => {
    const ok = await onAsk({
      title: `Hapus permanen “${it.name}”?`,
      message: (it.type === 'folder' ? 'Folder dan semua isinya dihapus' : 'File ini dihapus')
        + ' dari channel Telegram dan tidak bisa dikembalikan lagi.',
      confirm: 'Hapus permanen',
      danger: true,
    })
    if (ok) act('/api/trash/purge', { ids: [it.id] })
  }
  const purgeAll = async () => {
    const ok = await onAsk({
      title: 'Kosongkan sampah?',
      message: `${d.items.length} item di sampah dihapus dari channel Telegram dan tidak bisa dikembalikan lagi.`,
      confirm: 'Kosongkan sampah',
      danger: true,
    })
    if (ok) act('/api/trash/purge', { all: true })
  }
  const menu = (it, at) =>
    onMenu(at ?? { x: innerWidth / 2 - 110, y: innerHeight / 3 }, [
      { label: 'Pulihkan', icon: RotateCcw, onSelect: () => restore(it) },
      '-',
      { label: 'Hapus permanen', icon: Trash2, danger: true, onSelect: () => purge(it) },
    ])
  const menuAt = (it, e) => {
    const r = e.currentTarget.getBoundingClientRect()
    menu(it, { x: r.right - 220, y: r.bottom + 4 })
  }

  const files = d ? d.items.filter(it => it.type === 'file') : []
  const folders = d ? d.items.filter(it => it.type === 'folder') : []
  // Files open in the viewer (read only: restore first to change them); folders cannot be opened.
  const open = it => {
    if (it.type !== 'file' || !isViewable(it)) return menu(it, null)
    const items = files.filter(isViewable)
    onOpen(items, items.findIndex(x => x.id === it.id))
  }
  const ctx = it => e => { e.preventDefault(); menu(it, { x: e.clientX, y: e.clientY }) }
  const more = it => (
    <button className="icon-btn more" onClick={e => { e.stopPropagation(); menuAt(it, e) }} aria-label={`Tindakan untuk ${it.name}`} aria-haspopup="menu"><EllipsisVertical size={18} /></button>
  )

  return (
    <section className="panel trash-panel" aria-labelledby="panel-title">
      <div className="panel-head">
        <h1 id="panel-title" className="panel-title">Sampah</h1>
        <div className="tools">
          {d?.items.length > 0 && <button className="btn ghost" onClick={purgeAll}>Kosongkan sampah</button>}
          <div className="seg" role="group" aria-label="Tampilan">
            <button aria-pressed={view === 'list'} onClick={() => onView('list')} aria-label="Daftar" data-tip="Daftar"><List size={18} /></button>
            <button aria-pressed={view === 'grid'} onClick={() => onView('grid')} aria-label="Petak" data-tip="Petak"><LayoutGrid size={18} /></button>
          </div>
        </div>
      </div>
      {d?.items.length > 0 && (
        <p className="muted pad-x">Item di sampah dihapus permanen setelah {d.days} hari, termasuk dari channel Telegram. Pulihkan dulu untuk mengubahnya.</p>
      )}

      {!d ? (view === 'list' ? <ListSkeleton rows={5} /> : <GridSkeleton />) : !d.items.length ? (
        <div className="empty">
          <Trash2 size={40} aria-hidden />
          <h2>Sampah kosong</h2>
          <p className="muted">File dan folder yang kamu hapus muncul di sini selama {d.days} hari. Selama itu, semuanya masih bisa dipulihkan.</p>
        </div>
      ) : view === 'list' ? (
        <table className="list trash">
          <thead>
            <tr><th>Nama</th><th className="hide-sm">Dihapus</th><th className="num hide-xs">Ukuran</th><th className="act"><span className="sr">Tindakan</span></th></tr>
          </thead>
          <tbody>
            {d.items.map(it => (
              <tr key={it.id} onContextMenu={ctx(it)}>
                <td>
                  <button className="name" onClick={() => open(it)}>
                    {it.type === 'folder' ? <FolderIcon /> : <KindIcon mime={it.mime} />}
                    <span className="name-col">
                      <span className="nm" data-tip={it.name} data-tip-overflow>{it.name}</span>
                      <span className="meta">{left(it.trashed_at, d.days)}</span>
                    </span>
                  </button>
                </td>
                <td className="hide-sm muted">{fmtDate(it.trashed_at)}</td>
                <td className="num hide-xs muted">
                  {it.type === 'folder' ? (it.files ? fmtSize(it.size) : '—') : fmtSize(it.size)}
                </td>
                <td className="act">
                  <span className="trash-acts">
                    <button className="icon-btn hide-xs" onClick={() => restore(it)} aria-label={`Pulihkan ${it.name}`} data-tip="Pulihkan"><RotateCcw size={18} /></button>
                    {more(it)}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <div className="grid-view">
          {folders.length > 0 && (
            <>
              <h2 className="group-title">Folder</h2>
              <ul className="folder-grid">
                {folders.map(it => (
                  <li key={it.id} className="folder-chip" onContextMenu={ctx(it)}>
                    <button className="name" onClick={() => open(it)}><FolderIcon /> <span>{it.name}</span></button>
                    {more(it)}
                  </li>
                ))}
              </ul>
            </>
          )}
          {files.length > 0 && (
            <>
              <h2 className="group-title">File</h2>
              <ul className="file-grid">
                {files.map(it => (
                  <li key={it.id} className="file-card" onContextMenu={ctx(it)}>
                    <button className="card-open" onClick={() => open(it)} aria-label={`Buka ${it.name}`}>
                      <Thumb file={it} />
                      {kind(it.mime) === 'video' && <span className="badge"><Play size={14} aria-hidden /> Video</span>}
                      <span className="badge trash-left">{left(it.trashed_at, d.days).replace('Dihapus permanen ', '')}</span>
                    </button>
                    <div className="card-foot">
                      <KindIcon mime={it.mime} size={18} />
                      <span className="fname" data-tip={it.name} data-tip-overflow>{it.name}</span>
                      {more(it)}
                    </div>
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </section>
  )
}
