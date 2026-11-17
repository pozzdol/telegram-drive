// "Drive saya" and search results: folders first, then files, as a list or a grid.
// Selection works like a desktop file manager: Ctrl/Cmd+A selects everything here,
// Ctrl/Cmd+click toggles one item, Shift+click selects a range, Delete removes, Esc clears.
// On touch screens a long press selects, and while something is selected a tap toggles.
import { useEffect, useRef, useState } from 'react'
import { EllipsisVertical, LayoutGrid, List, Play, Search, Trash2, Upload, X } from 'lucide-react'
import { Crumbs, FolderIcon, GridSkeleton, KindIcon, ListSkeleton, MissingBadge, Thumb, fmtDate, fmtSize, kind } from './ui.jsx'

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

// Keyboard shortcuts must not steal Ctrl+A or Delete from text fields or open dialogs.
const typingOrModal = e =>
  e.target.closest?.('input, textarea, select, [contenteditable="true"]') || document.querySelector('dialog[open], .menu')

export default function Drive({ data, query, view, sort, onView, onSort, onGo, onOpenFile, onMenu, onUpload, onDelete }) {
  const folders = data ? [...data.folders].sort(SORTS[sort === 'size' ? 'name' : sort].cmp) : []
  const files = data ? [...data.files].sort(SORTS[sort].cmp) : []
  const empty = data && !folders.length && !files.length

  // Display order, so Shift+click ranges match what is on screen.
  const order = [...folders.map(f => ({ type: 'folder', item: f, key: `folder:${f.id}` })),
    ...files.map(f => ({ type: 'file', item: f, key: `file:${f.id}` }))]
  const [sel, setSel] = useState(() => new Set())
  // The sticky column titles sit right under the sticky header, whose height changes when it wraps.
  const head = useRef(null)
  useEffect(() => {
    const el = head.current
    const ro = new ResizeObserver(() => el.parentElement.style.setProperty('--head-h', `${el.offsetHeight}px`))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const anchor = useRef(0)
  useEffect(() => setSel(new Set()), [data?.key])

  const selected = order.filter(o => sel.has(o.key))
  const latest = useRef({})
  latest.current = { order, selected, onDelete }
  useEffect(() => {
    const onKey = e => {
      if (typingOrModal(e)) return
      const { order, selected, onDelete } = latest.current
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a' && order.length) {
        e.preventDefault()
        setSel(new Set(order.map(o => o.key)))
      } else if (e.key === 'Escape') {
        setSel(new Set())
      } else if (e.key === 'Delete' && selected.length) {
        e.preventDefault()
        onDelete(selected)
      }
    }
    addEventListener('keydown', onKey)
    return () => removeEventListener('keydown', onKey)
  }, [])

  // Handles a click with Ctrl/Cmd/Shift as selection; returns false for a plain click (open).
  const pick = (e, i) => {
    if (e.shiftKey) {
      const [a, b] = [Math.min(anchor.current, i), Math.max(anchor.current, i)]
      setSel(new Set(order.slice(a, b + 1).map(o => o.key)))
    } else if (e.ctrlKey || e.metaKey || sel.size > 0) {
      const k = order[i].key
      setSel(s => { const n = new Set(s); n.has(k) ? n.delete(k) : n.add(k); return n })
      anchor.current = i
    } else {
      setSel(new Set())
      anchor.current = i
      return false
    }
    e.preventDefault()
    e.stopPropagation()
    return true
  }
  const indexOf = (type, it) => order.findIndex(o => o.key === `${type}:${it.id}`)
  const open = (type, it) => e => {
    if (pick(e, indexOf(type, it))) return
    type === 'folder' ? onGo(it.id) : onOpenFile(it, files)
  }

  // A long press on a touch screen fires contextmenu: select instead of opening the menu.
  const touch = useRef(false)
  const item = (type, it) => ({
    className: sel.has(`${type}:${it.id}`) ? 'selected' : undefined,
    'aria-selected': sel.has(`${type}:${it.id}`),
    onPointerDown: e => { touch.current = e.pointerType === 'touch' },
    onContextMenu: e => {
      e.preventDefault()
      if (touch.current) {
        const k = `${type}:${it.id}`
        setSel(s => new Set(s).add(k))
        anchor.current = indexOf(type, it)
        navigator.vibrate?.(15)
        return
      }
      // Right-click inside a multi-selection acts on the whole selection, like a file manager.
      if (selected.length > 1 && sel.has(`${type}:${it.id}`)) onMenu(null, selected, { x: e.clientX, y: e.clientY })
      else onMenu(type, it, { x: e.clientX, y: e.clientY })
    },
  })
  const more = (type, it) => (
    <button className="icon-btn more" onClick={e => { e.stopPropagation(); onMenu(type, it, menuFromButton(e)) }}
      aria-label={`Tindakan untuk ${it.name}`} aria-haspopup="menu">
      <EllipsisVertical size={18} />
    </button>
  )

  return (
    <section className="panel" aria-labelledby="panel-title">
      <div className="panel-head" ref={head}>
        <h1 id="panel-title" className="panel-title">
          {query ? <>Hasil untuk “{query}”</> : <Crumbs path={data?.path || []} onGo={onGo} />}
        </h1>
        {selected.length > 0 ? (
          <div className="tools selbar" role="toolbar" aria-label="Item terpilih">
            <button className="icon-btn" onClick={() => setSel(new Set())} aria-label="Batalkan pilihan" title="Batalkan pilihan (Esc)"><X size={18} /></button>
            <strong>{selected.length} dipilih</strong>
            <button className="btn ghost danger" onClick={() => onDelete(selected)} title="Hapus (Delete)"><Trash2 size={18} aria-hidden /> Hapus</button>
          </div>
        ) : (
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
        )}
      </div>

      {!data ? (view === 'list' ? <ListSkeleton /> : <GridSkeleton />) : empty ? (
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
                <td><button className="name" onClick={open('folder', f)}><FolderIcon />
                  <span className="name-col"><span className="nm">{f.name}</span><span className="meta">{fmtDate(f.created_at)}</span></span>
                </button></td>
                <td className="hide-sm muted">{fmtDate(f.created_at)}</td>
                <td className="num hide-xs muted">—</td>
                <td className="act">{more('folder', f)}</td>
              </tr>
            ))}
            {files.map(f => (
              <tr key={`f${f.id}`} {...item('file', f)}>
                <td><button className={`name${f.missing ? ' missing' : ''}`} onClick={open('file', f)}><KindIcon mime={f.mime} />
                  <span className="name-col">
                    <span className="nm-row"><span className="nm">{f.name}</span>{f.missing && <MissingBadge />}</span>
                    <span className="meta">{f.missing ? 'Hilang di Telegram' : `${fmtDate(f.created_at)} · ${fmtSize(f.size)}`}</span>
                  </span>
                </button></td>
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
                  <li key={f.id} {...item('folder', f)} className={`folder-chip${sel.has(`folder:${f.id}`) ? ' selected' : ''}`}>
                    <button className="name" onClick={open('folder', f)}><FolderIcon /> <span>{f.name}</span></button>
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
                  <li key={f.id} {...item('file', f)} className={`file-card${sel.has(`file:${f.id}`) ? ' selected' : ''}`}>
                    <button className="card-open" onClick={open('file', f)} aria-label={`Buka ${f.name}`}>
                      <Thumb file={f} />
                      {f.missing ? <span className="badge warn"><MissingBadge /></span>
                        : kind(f.mime) === 'video' && <span className="badge"><Play size={14} aria-hidden /> Video</span>}
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
