// Admin page: family accounts. Each account has its own private drive.
// Accounts are only made here; there is no public sign-up.
import { useEffect, useRef, useState } from 'react'
import { EllipsisVertical, KeyRound, Pencil, ShieldCheck, ShieldOff, Trash2, UserPlus } from 'lucide-react'
import { api } from './api.js'
import { fmtDate, fmtSize, useModal } from './ui.jsx'

const MIN_PW = 8

export function Avatar({ name, size = 32 }) {
  return <span className="avatar" style={{ '--size': `${size}px` }} aria-hidden>{name.slice(0, 1).toUpperCase()}</span>
}

export default function Users({ me, version, onMenu, onAsk, onError }) {
  const [users, setUsers] = useState(null)
  const [adding, setAdding] = useState(false)
  const [v, setV] = useState(0)
  const reload = () => setV(x => x + 1)

  useEffect(() => {
    api('/api/admin/users').then(d => setUsers(d.users), e => onError(e.message))
  }, [version, v]) // eslint-disable-line react-hooks/exhaustive-deps

  const patch = async (u, body) => {
    try {
      await api(`/api/admin/users/${u.id}`, { method: 'PATCH', body })
      reload()
    } catch (e) {
      onError(e.status === 409 ? 'Username itu sudah dipakai.' : e.message)
    }
  }

  const rename = async u => {
    const username = await onAsk({ title: 'Ganti username', input: true, value: u.username, confirm: 'Simpan' })
    if (username && username !== u.username) patch(u, { username })
  }
  const reset = async u => {
    const password = await onAsk({
      title: `Password baru untuk ${u.username}`, input: true, value: '', confirm: 'Simpan',
      message: `Minimal ${MIN_PW} karakter. ${u.username} akan keluar dari semua perangkat dan masuk lagi dengan password ini.`,
    })
    if (password == null) return
    if (password.length < MIN_PW) return onError(`Password minimal ${MIN_PW} karakter.`)
    patch(u, { password })
  }
  const remove = async u => {
    if (u.files) {
      return onError(`${u.username} masih punya ${u.files} file. Minta dia menghapus atau mendownload filenya dulu.`)
    }
    const ok = await onAsk({ title: `Hapus akun ${u.username}?`, message: 'Akun dan folder kosongnya dihapus. Tidak bisa dikembalikan.', confirm: 'Hapus', danger: true })
    if (!ok) return
    try { await api(`/api/admin/users/${u.id}`, { method: 'DELETE' }); reload() } catch (e) { onError(e.message) }
  }

  const menu = (u, e) => {
    const r = e.currentTarget.getBoundingClientRect()
    const self = u.id === me.id
    onMenu({ x: r.left, y: r.bottom + 4 }, [
      { label: 'Ganti username', icon: Pencil, onSelect: () => rename(u) },
      { label: 'Atur password baru', icon: KeyRound, onSelect: () => reset(u) },
      ...(self ? [] : [
        u.is_admin
          ? { label: 'Cabut akses admin', icon: ShieldOff, onSelect: () => patch(u, { is_admin: false }) }
          : { label: 'Jadikan admin', icon: ShieldCheck, onSelect: () => patch(u, { is_admin: true }) },
        '-',
        { label: 'Hapus akun', icon: Trash2, danger: true, onSelect: () => remove(u) },
      ]),
    ])
  }

  return (
    <section className="panel" aria-labelledby="panel-title">
      <div className="panel-head">
        <h1 id="panel-title" className="panel-title">Pengguna</h1>
        <button className="btn primary" onClick={() => setAdding(true)}><UserPlus size={18} aria-hidden /> Tambah pengguna</button>
      </div>
      <p className="muted pad-x">Setiap pengguna punya drive sendiri. Mereka tidak bisa melihat file satu sama lain.</p>

      {!users ? <p className="muted pad">Memuat…</p> : (
        <table className="list users">
          <thead>
            <tr><th>Nama</th><th className="num hide-xs">File</th><th className="num">Terpakai</th><th className="num hide-sm">Dibuat</th><th className="act"><span className="sr">Tindakan</span></th></tr>
          </thead>
          <tbody>
            {users.map(u => (
              <tr key={u.id}>
                <td>
                  <div className="who">
                    <Avatar name={u.username} />
                    <span className="who-name">{u.username}{u.id === me.id && <span className="muted"> (kamu)</span>}</span>
                    {u.is_admin && <span className="role">Admin</span>}
                  </div>
                </td>
                <td className="num hide-xs">{u.files.toLocaleString('id-ID')}</td>
                <td className="num">{fmtSize(u.bytes)}</td>
                <td className="num hide-sm">{fmtDate(u.created_at)}</td>
                <td className="act">
                  <button className="icon-btn more" onClick={e => menu(u, e)} aria-label={`Tindakan untuk ${u.username}`} aria-haspopup="menu">
                    <EllipsisVertical size={18} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {adding && <AddUserDialog onClose={added => { setAdding(false); if (added) reload() }} />}
    </section>
  )
}

function AddUserDialog({ onClose }) {
  const ref = useModal()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const added = useRef(false) // a ref: the close event fires before React re-renders

  const submit = async e => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    setBusy(true)
    setErr('')
    try {
      await api('/api/admin/users', {
        method: 'POST',
        body: { username: f.get('username').trim(), password: f.get('password'), is_admin: f.get('admin') === 'on' },
      })
      added.current = true
      ref.current.close()
    } catch (e) {
      setErr(e.status === 409 ? 'Username itu sudah dipakai.' : e.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <dialog ref={ref} className="sheet form" onClose={() => onClose(added.current)}>
      <form onSubmit={submit}>
        <h2>Tambah pengguna</h2>
        <label>Username<input name="username" required minLength={3} maxLength={64} autoComplete="off" autoFocus /></label>
        <label>Password<input name="password" required minLength={MIN_PW} autoComplete="new-password" /></label>
        <p className="muted small">Minimal {MIN_PW} karakter. Berikan ke anggota keluarga; mereka bisa menggantinya sendiri nanti.</p>
        <label className="check"><input type="checkbox" name="admin" /> Jadikan admin (bisa mengatur pengguna)</label>
        {err && <p className="err" role="alert">{err}</p>}
        <footer>
          <button type="button" className="btn ghost" onClick={() => ref.current.close()}>Batal</button>
          <button className="btn primary" disabled={busy}>{busy ? 'Menyimpan…' : 'Tambah'}</button>
        </footer>
      </form>
    </dialog>
  )
}

// Any user: change own password. Other devices are logged out.
export function PasswordDialog({ onClose }) {
  const ref = useModal()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const done = useRef(false) // a ref: the close event fires before React re-renders

  const submit = async e => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    if (f.get('new') !== f.get('confirm')) return setErr('Password baru dan konfirmasinya tidak sama.')
    setBusy(true)
    setErr('')
    try {
      await api('/api/me/password', { method: 'PUT', body: { current: f.get('current'), new: f.get('new') } })
      done.current = true
      ref.current.close()
    } catch (e) {
      setErr(e.status === 403 ? 'Password lama salah.' : e.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <dialog ref={ref} className="sheet form" onClose={() => onClose(done.current)}>
      <form onSubmit={submit}>
        <h2>Ganti password</h2>
        <label>Password lama<input name="current" type="password" required autoComplete="current-password" autoFocus /></label>
        <label>Password baru<input name="new" type="password" required minLength={MIN_PW} autoComplete="new-password" /></label>
        <label>Ulangi password baru<input name="confirm" type="password" required minLength={MIN_PW} autoComplete="new-password" /></label>
        <p className="muted small">Minimal {MIN_PW} karakter. Perangkat lain yang sedang masuk akan dikeluarkan.</p>
        {err && <p className="err" role="alert">{err}</p>}
        <footer>
          <button type="button" className="btn ghost" onClick={() => ref.current.close()}>Batal</button>
          <button className="btn primary" disabled={busy}>{busy ? 'Menyimpan…' : 'Simpan'}</button>
        </footer>
      </form>
    </dialog>
  )
}
