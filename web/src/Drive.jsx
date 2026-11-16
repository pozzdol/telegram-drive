// "Drive saya" and search results: folders first, then files, as a list or a grid.
import { EllipsisVertical, LayoutGrid, List, Play, Search, Upload } from 'lucide-react'
import { Crumbs, FolderIcon, KindIcon, Thumb, fmtDate, fmtSize, kind } from './ui.jsx'

const SORTS = {
  name: { label: 'Nama', cmp: (a, b) => a.name.localeCompare(b.name, 'id', { numeric: true, sensitivity: 'base' }) },
  date: { label: 'Terakhir ditambahkan', cmp: (a, b) => b.created_at - a.created_at },
  size: { label: 'Ukuran', cmp: (a, b) => (b.size ?? 0) - (a.size ?? 0) },
}

// Right-click and the ⋮ button open the same menu.
const menuFromButton = e => {
  const r = e.currentTarget.getBoundingClientRect()
  return { x: r.left, y: r.bottom + 4 }
}

export default function Drive({ data, query, view, sort, onView, onSort, onGo, onOpenFile, onMenu, onUpload }) {
  const folders = data ? [...data.folders].sort(SORTS[sort === 'size' ? 'name' : sort].cmp) : []
  const files = data ? [...data.files].sort(SORTS[sort].cmp) : []
  const empty = data && !folders.length && !files.length

  const item = (type, it) => ({
    onContextMenu: e => { e.preventDefault(); onMenu(type, it, { x: e.clientX, y: e.clientY }) },
  })
  const more = (type, it) => (
    <button className="icon-btn more" onClick={e => { e.stopPropagation(); onMenu(type, it, menuFromButton(e)) }}
      aria-label={`Tindakan untuk ${it.name}`} aria-haspopup="menu">
      <EllipsisVertical size={18} />
    </button>
  )

  return (
    <section className="panel" aria-labelledby="panel-title">
      <div className="panel-head">
        <h1 id="panel-title" className="panel-title">
          {query ? <>Hasil untuk “{query}”</> : <Crumbs path={data?.path || []} onGo={onGo} />}
        </h1>
        <div className="tools">
          <label className="sort">
            <span className="sr">Urutkan</span>
            <select value={sort} onChange={e => onSort(e.target.value)}>
              {Object.entries(SORTS).map(([k, s]) => <option key={k} value={k}>{s.label}</option>)}
            </select>
          </label>
          <div className="seg" role="group" aria-label="Tampilan">
            <button aria-pressed={view === 'list'} onClick={() => onView('list')} aria-label="Daftar" title="Daftar"><List size={18} /></button>
            <button aria-pressed={view === 'grid'} onClick={() => onView('grid')} aria-label="Petak" title="Petak"><LayoutGrid size={18} /></button>
          </div>
        </div>
      </div>

      {!data ? <p className="muted pad">Memuat…</p> : empty ? (
        query ? (
          <div className="empty">
            <Search size={40} aria-hidden />
            <h2>Tidak ada yang cocok</h2>
            <p className="muted">Coba kata lain, atau sebagian nama filenya saja.</p>
          </div>
        ) : (
          <div className="empty">
            <Upload size={40} aria-hidden />
            <h2>Folder ini masih kosong</h2>
            <p className="muted">Tarik file ke sini, atau upload dari perangkatmu.</p>
            <button className="btn primary" onClick={onUpload}>Upload file</button>
          </div>
        )
      ) : view === 'list' ? (
        <table className="list">
          <thead>
            <tr><th>Nama</th><th className="hide-sm">Ditambahkan</th><th className="num hide-xs">Ukuran</th><th><span className="sr">Tindakan</span></th></tr>
          </thead>
          <tbody>
            {folders.map(f => (
              <tr key={`d${f.id}`} {...item('folder', f)}>
                <td><button className="name" onClick={() => onGo(f.id)}><FolderIcon /> <span>{f.name}</span></button></td>
                <td className="hide-sm muted">{fmtDate(f.created_at)}</td>
                <td className="num hide-xs muted">—</td>
                <td className="act">{more('folder', f)}</td>
              </tr>
            ))}
            {files.map(f => (
              <tr key={`f${f.id}`} {...item('file', f)}>
                <td><button className="name" onClick={() => onOpenFile(f, files)}><KindIcon mime={f.mime} /> <span>{f.name}</span></button></td>
                <td className="hide-sm muted">{fmtDate(f.created_at)}</td>
                <td className="num hide-xs muted">{fmtSize(f.size)}</td>
                <td className="act">{more('file', f)}</td>
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
                {folders.map(f => (
                  <li key={f.id} className="folder-chip" {...item('folder', f)}>
                    <button className="name" onClick={() => onGo(f.id)}><FolderIcon /> <span>{f.name}</span></button>
                    {more('folder', f)}
                  </li>
                ))}
              </ul>
            </>
          )}
          {files.length > 0 && (
            <>
              <h2 className="group-title">File</h2>
              <ul className="file-grid">
                {files.map(f => (
                  <li key={f.id} className="file-card" {...item('file', f)}>
                    <button className="card-open" onClick={() => onOpenFile(f, files)} aria-label={`Buka ${f.name}`}>
                      <Thumb file={f} />
                      {kind(f.mime) === 'video' && <span className="badge"><Play size={14} aria-hidden /> Video</span>}
                    </button>
                    <div className="card-foot">
                      <KindIcon mime={f.mime} size={18} />
                      <span className="fname" title={f.name}>{f.name}</span>
                      {more('file', f)}
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
