import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Download, FolderInput, FolderOpen, FolderPlus, HardDrive, Images, KeyRound, LogOut, Pencil, Plus, Search,
  Trash2, Upload, Users as UsersIcon, X,
} from 'lucide-react'
import { api, fileURL, uploadFile } from './api.js'
import { makeVideoThumb } from './videoThumb.js'
import Drive from './Drive.jsx'
import Photos from './Photos.jsx'
import Lightbox from './Lightbox.jsx'
import Users, { Avatar, PasswordDialog } from './Users.jsx'
import { AskDialog, Menu, MoveDialog, UploadPanel, fmtSize, isViewable, kind } from './ui.jsx'

// ---- URL state: ?view=photos | ?view=users | ?folder=12 | ?q=text ----
const VIEWS = ['photos', 'users']
const readRoute = () => {
  const p = new URLSearchParams(location.search)
  const v = p.get('view')
  return { view: VIEWS.includes(v) ? v : 'drive', folder: Number(p.get('folder')) || 0, q: p.get('q') || '' }
}
const routeURL = r => {
  const p = new URLSearchParams()
  if (r.view !== 'drive') p.set('view', r.view)
  if (r.folder) p.set('folder', r.folder)
  if (r.q) p.set('q', r.q)
  return p.size ? `?${p}` : location.pathname
}

// Per-browser preferences; storage may be unavailable (private mode), so never rely on it.
const pref = (k, def) => { try { return localStorage.getItem(k) || def } catch { return def } }
const savePref = (k, v) => { try { localStorage.setItem(k, v) } catch { /* not persisted */ } }

export default function App() {
  const [authed, setAuthed] = useState(null) // null = checking
  const [route, setRoute] = useState(readRoute)
  const [data, setData] = useState(null)
  const [usage, setUsage] = useState(null)
  const [offline, setOffline] = useState(false)
  const [me, setMe] = useState(null) // { id, username, is_admin }
  const [pwOpen, setPwOpen] = useState(false)
  const [version, setVersion] = useState(0) // bump to reload lists after a change
  const [error, setError] = useState('')
  const [uploads, setUploads] = useState([])
  const [menu, setMenu] = useState(null) // { at, items }
  const [ask, setAsk] = useState(null)
  const [moving, setMoving] = useState(null)
  const [lightbox, setLightbox] = useState(null) // { items, start }
  const [dragging, setDragging] = useState(false)
  const [view, setView] = useState(() => pref('view', 'list'))
  const [sort, setSort] = useState(() => pref('sort', 'name'))
  const [query, setQuery] = useState(route.q)
  const inputRef = useRef(null)
  const queue = useRef(Promise.resolve())

  const nav = (r, replace) => {
    const next = { view: 'drive', folder: 0, q: '', ...r }
    history[replace ? 'replaceState' : 'pushState'](null, '', routeURL(next))
    setRoute(next)
    setQuery(next.q)
  }
  useEffect(() => {
    const f = () => { const r = readRoute(); setRoute(r); setQuery(r.q) }
    addEventListener('popstate', f)
    return () => removeEventListener('popstate', f)
  }, [])

  // Only admins have the users page; anyone else lands in their drive.
  useEffect(() => {
    if (me && !me.is_admin && route.view === 'users') nav({}, true)
  }, [me, route.view]) // eslint-disable-line react-hooks/exhaustive-deps

  const refresh = () => setVersion(v => v + 1)
  const closeMenu = useCallback(() => setMenu(null), [])
  const askUser = opts => new Promise(resolve => setAsk({ ...opts, resolve }))

  // Who is logged in (also the session check), then storage usage for the sidebar.
  useEffect(() => {
    if (authed === false) return
    api('/api/me').then(setMe, () => {})
  }, [authed])
  useEffect(() => {
    api('/api/usage').then(u => { setUsage(u); setAuthed(true); setOffline(false) }, e => {
      if (e.status === 401) setAuthed(false)
      // No HTTP status at all: the API could not be reached (offline, DNS, blocked).
      else if (!e.status && authed === null) setOffline(true)
      else { setAuthed(true); setError(e.message) }
    })
  }, [version, authed])

  // Folder contents or search results.
  useEffect(() => {
    if (!authed || route.view !== 'drive') return
    let live = true
    const url = route.q ? `/api/search?q=${encodeURIComponent(route.q)}` : `/api/list?folder_id=${route.folder}`
    setData(d => (d?.key === url ? d : null))
    api(url).then(d => { if (live) setData({ ...d, key: url }) }, e => {
      if (!live) return
      if (e.status === 401) setAuthed(false)
      else if (e.status === 404) nav({}, true)
      else setError(e.message)
    })
    return () => { live = false }
  }, [authed, route.view, route.folder, route.q, version]) // eslint-disable-line react-hooks/exhaustive-deps

  // Search as you type, without filling the history with every keystroke.
  useEffect(() => {
    const q = query.trim()
    if (q === route.q) return
    const t = setTimeout(() => nav({ folder: route.folder, q }, route.q !== '' && q !== ''), 300)
    return () => clearTimeout(t)
  }, [query]) // eslint-disable-line react-hooks/exhaustive-deps

  const run = async fn => {
    try { await fn(); refresh() } catch (e) { setError(e.message) }
  }

  // ---- actions ----
  const newFolder = async () => {
    const name = await askUser({ title: 'Folder baru', input: true, value: 'Folder tanpa judul', confirm: 'Buat' })
    if (name) run(() => api('/api/folders', { method: 'POST', body: { name, parent_id: route.view === 'drive' && !route.q ? route.folder : 0 } }))
  }
  const rename = async (type, it) => {
    const name = await askUser({ title: 'Ganti nama', input: true, value: it.name, selectBase: type === 'file', confirm: 'Simpan' })
    if (name && name !== it.name) run(() => api(`/api/${type}s/${it.id}`, { method: 'PATCH', body: { name } }))
  }
  // Deletes files and folders (with everything inside) in one request. items: [{ type, item }].
  const removeMany = async items => {
    const folders = items.filter(x => x.type === 'folder')
    const one = items.length === 1 ? items[0].item.name : null
    const ok = await askUser({
      title: one ? `Hapus “${one}”?` : `Hapus ${items.length} item?`,
      message: (folders.length ? 'Folder dihapus beserta semua file dan subfolder di dalamnya. ' : '')
        + 'File dihapus permanen dari channel Telegram dan tidak bisa dikembalikan.',
      confirm: 'Hapus',
      danger: true,
    })
    if (!ok) return false
    try {
      await api('/api/delete', {
        method: 'POST',
        body: { files: items.filter(x => x.type === 'file').map(x => x.item.id), folders: folders.map(x => x.item.id) },
      })
      return true
    } catch (e) {
      setError(e.message)
      return false
    } finally {
      refresh()
    }
  }
  const remove = (type, it) => removeMany([{ type, item: it }])
  const move = async (target, to) => {
    const body = target.type === 'file' ? { folder_id: to } : { parent_id: to }
    await api(`/api/${target.type}s/${target.item.id}`, { method: 'PATCH', body })
    refresh()
  }
  const openFile = (f, siblings) => {
    if (isViewable(f)) {
      const items = siblings.filter(isViewable)
      setLightbox({ items, start: items.findIndex(x => x.id === f.id) })
    } else if (kind(f.mime) === 'doc') window.open(fileURL(f.id, true), '_blank', 'noopener')
    else location.assign(fileURL(f.id))
  }
  const deleteFromLightbox = async f => {
    if (await remove('file', f)) setLightbox(lb => ({ ...lb, items: lb.items.filter(x => x.id !== f.id) }))
  }
  const logout = async () => {
    await api('/api/auth/logout', { method: 'POST' }).catch(() => {})
    setAuthed(false)
    setData(null)
    setMe(null)
    setUsage(null)
    if (route.view === 'users') nav({}, true)
  }
  const accountMenu = e => {
    const r = e.currentTarget.getBoundingClientRect()
    setMenu({
      at: { x: r.right - 220, y: r.bottom + 6 },
      items: [
        { label: 'Ganti password', icon: KeyRound, onSelect: () => setPwOpen(true) },
        ...(me?.is_admin ? [{ label: 'Kelola pengguna', icon: UsersIcon, onSelect: () => nav({ view: 'users' }) }] : []),
        '-',
        { label: 'Keluar', icon: LogOut, onSelect: logout },
      ],
    })
  }

  // type null: right-click on a multi-selection; it is then the selected [{ type, item }].
  const itemMenu = (type, it, at) => setMenu(type === null ? {
    at,
    items: [{ label: `Hapus ${it.length} item`, icon: Trash2, danger: true, onSelect: () => removeMany(it) }],
  } : {
    at,
    items: [
      type === 'folder'
        ? { label: 'Buka', icon: FolderOpen, onSelect: () => nav({ folder: it.id }) }
        : { label: 'Buka', icon: FolderOpen, onSelect: () => openFile(it, data?.files || [it]) },
      ...(type === 'file' ? [{ label: 'Download', icon: Download, onSelect: () => location.assign(fileURL(it.id)) }] : []),
      ...(route.q ? [{ label: 'Tampilkan lokasi', icon: FolderInput, onSelect: () => nav({ folder: type === 'file' ? it.folder_id || 0 : it.parent_id || 0 }) }] : []),
      '-',
      { label: 'Ganti nama', icon: Pencil, onSelect: () => rename(type, it) },
      { label: 'Pindahkan', icon: FolderInput, onSelect: () => setMoving({ type, item: it }) },
      '-',
      { label: 'Hapus', icon: Trash2, danger: true, onSelect: () => remove(type, it) },
    ],
  })
  const newMenu = e => {
    const r = e.currentTarget.getBoundingClientRect()
    setMenu({
      at: { x: r.left, y: r.bottom + 6 },
      items: [
        { label: 'Folder baru', icon: FolderPlus, onSelect: newFolder },
        '-',
        { label: 'Upload file', icon: Upload, onSelect: () => inputRef.current.click() },
      ],
    })
  }

  // One file at a time: several 1 GB uploads in parallel only slow each other down.
  const startUploads = list => {
    const target = route.view === 'drive' && !route.q ? route.folder : 0
    for (const file of list) {
      const key = crypto.randomUUID()
      const update = patch => setUploads(us => us.map(x => (x.key === key ? { ...x, ...patch } : x)))
      setUploads(us => [...us, { key, name: file.name, size: file.size, phase: 'queued', done: 0, total: file.size, status: 'active' }])
      queue.current = queue.current.then(() =>
        uploadFile(file, target, update)
          .then(async s => {
            // The local copy decodes instantly, so make the video preview before the list shows it.
            if (kind(file.type) === 'video' && s.file_id) {
              const url = URL.createObjectURL(file)
              await makeVideoThumb(s.file_id, url)
              URL.revokeObjectURL(url)
            }
            update({ status: 'done' })
            refresh()
          })
          .catch(e => update({ status: 'error', error: e.message })))
    }
  }

  // Warn before closing the tab mid-upload.
  const uploading = uploads.some(u => u.status === 'active')
  useEffect(() => {
    if (!uploading) return
    const f = e => e.preventDefault()
    addEventListener('beforeunload', f)
    return () => removeEventListener('beforeunload', f)
  }, [uploading])

  if (offline) {
    return (
      <main className="login">
        <div className="login-card" role="alert">
          <HardDrive size={36} className="login-mark" aria-hidden />
          <h1>Tidak bisa terhubung ke server</h1>
          <p className="muted">Periksa koneksi internet. Kalau masih gagal, server drive mungkin sedang mati.</p>
          <button className="btn primary wide" onClick={() => { setOffline(false); refresh() }}>Coba lagi</button>
        </div>
      </main>
    )
  }
  if (authed === null) return <p className="boot muted">Memuat…</p>
  if (!authed) return <Login onDone={() => { setAuthed(true); refresh() }} />

  const here = route.view === 'photos' ? 'Foto' : data?.path?.at(-1)?.name || 'Drive saya'
  const goDrive = () => nav({})
  const goPhotos = () => nav({ view: 'photos' })
  const showUsers = route.view === 'users' && me?.is_admin

  return (
    <div
      className="shell"
      onDragOver={e => { if (e.dataTransfer.types.includes('Files')) { e.preventDefault(); setDragging(true) } }}
      onDragLeave={e => { if (!e.relatedTarget) setDragging(false) }}
      onDrop={e => { e.preventDefault(); setDragging(false); if (e.dataTransfer.files.length) startUploads(e.dataTransfer.files) }}
    >
      <aside className="sidebar">
        <div className="brand"><HardDrive size={24} aria-hidden /> Telegram Drive</div>
        <button className="new-btn" onClick={newMenu} aria-haspopup="menu"><Plus size={22} aria-hidden /> Baru</button>
        <nav className="side-nav" aria-label="Bagian">
          <button aria-current={route.view === 'drive' ? 'page' : undefined} onClick={goDrive}><HardDrive size={20} aria-hidden /> Drive saya</button>
          <button aria-current={route.view === 'photos' ? 'page' : undefined} onClick={goPhotos}><Images size={20} aria-hidden /> Foto</button>
          {me?.is_admin && (
            <button aria-current={route.view === 'users' ? 'page' : undefined} onClick={() => nav({ view: 'users' })}><UsersIcon size={20} aria-hidden /> Pengguna</button>
          )}
        </nav>
        {usage && (
          <div className="usage">
            <span><strong>{fmtSize(usage.bytes)}</strong> terpakai</span>
            <span className="muted small">{usage.files.toLocaleString('id-ID')} file tersimpan di channel Telegram</span>
          </div>
        )}
      </aside>

      <header className="topbar">
        <form className="search" role="search" onSubmit={e => { e.preventDefault(); nav({ folder: route.folder, q: query.trim() }) }}>
          <Search size={20} aria-hidden />
          <input type="search" placeholder="Cari di Drive" aria-label="Cari di Drive" value={query} onChange={e => setQuery(e.target.value)} />
          {query && <button type="button" className="icon-btn" onClick={() => nav({ folder: route.folder })} aria-label="Hapus pencarian"><X size={18} /></button>}
        </form>
        <button className="account-btn" onClick={accountMenu} aria-haspopup="menu"
          aria-label={me ? `Akun: ${me.username}` : 'Akun'} title={me?.username}>
          {me ? <Avatar name={me.username} size={36} /> : <LogOut size={20} />}
        </button>
      </header>

      <main className="main">
        {error && (
          <div className="banner" role="alert">
            <span>{error}</span>
            <button className="icon-btn" onClick={() => setError('')} aria-label="Tutup pesan"><X size={16} /></button>
          </div>
        )}
        {showUsers ? (
          <Users me={me} version={version} onMenu={(at, items) => setMenu({ at, items })} onAsk={askUser} onError={setError} />
        ) : route.view === 'photos' ? (
          <Photos version={version} onOpen={(items, start) => setLightbox({ items, start })} onUpload={() => inputRef.current.click()} onError={setError} />
        ) : (
          <Drive
            data={data}
            query={route.q}
            view={view}
            sort={sort}
            onView={v => { setView(v); savePref('view', v) }}
            onSort={s => { setSort(s); savePref('sort', s) }}
            onGo={id => nav({ folder: id })}
            onOpenFile={openFile}
            onMenu={itemMenu}
            onDelete={removeMany}
            onUpload={() => inputRef.current.click()}
          />
        )}
      </main>

      <nav className="tabbar" aria-label="Bagian">
        <button aria-current={route.view === 'drive' ? 'page' : undefined} onClick={goDrive}><HardDrive size={22} aria-hidden /> Drive</button>
        <button aria-current={route.view === 'photos' ? 'page' : undefined} onClick={goPhotos}><Images size={22} aria-hidden /> Foto</button>
      </nav>
      <button className="fab" onClick={newMenu} aria-label="Baru" aria-haspopup="menu"><Plus size={26} /></button>

      <input ref={inputRef} type="file" multiple hidden onChange={e => { startUploads(e.target.files); e.target.value = '' }} />
      {dragging && <div className="dropzone" aria-hidden><div><Upload size={28} /> Lepas untuk upload ke <strong>{here}</strong></div></div>}
      <UploadPanel uploads={uploads} onClear={() => setUploads(us => us.filter(u => u.status === 'active'))} />
      {menu && <Menu at={menu.at} items={menu.items} onClose={closeMenu} />}
      {ask && <AskDialog ask={ask} onDone={v => { setAsk(null); ask.resolve(v) }} />}
      {moving && <MoveDialog target={moving} onMove={to => move(moving, to)} onClose={() => setMoving(null)} />}
      {pwOpen && <PasswordDialog onClose={() => setPwOpen(false)} />}
      {lightbox && <Lightbox items={lightbox.items} start={lightbox.start} onClose={() => setLightbox(null)} onDelete={deleteFromLightbox} />}
    </div>
  )
}

function Login({ onDone }) {
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async e => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    setBusy(true)
    setErr('')
    try {
      await api('/api/auth/login', { method: 'POST', body: { username: f.get('username'), password: f.get('password') } })
      onDone()
    } catch (e) {
      setErr(e.status === 401 ? 'Username atau password salah.' : e.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <main className="login">
      <form className="login-card" onSubmit={submit}>
        <HardDrive size={36} className="login-mark" aria-hidden />
        <h1>Masuk ke Telegram Drive</h1>
        <p className="muted">File kamu tersimpan di channel Telegram pribadi.</p>
        <label>Username<input name="username" autoComplete="username" required autoFocus /></label>
        <label>Password<input name="password" type="password" autoComplete="current-password" required /></label>
        {err && <p className="err" role="alert">{err}</p>}
        <button className="btn primary wide" disabled={busy}>{busy ? 'Masuk…' : 'Masuk'}</button>
      </form>
    </main>
  )
}
