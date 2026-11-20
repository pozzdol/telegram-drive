// Share a file or folder with other family accounts, and change or take away their access.
import { useEffect, useRef, useState } from 'react'
import { UserMinus } from 'lucide-react'
import { api } from './api.js'
import { Dropdown, useModal } from './ui.jsx'
import { Avatar } from './Users.jsx'

export const ROLES = [
  { value: 'viewer', label: 'Pelihat' },
  { value: 'commenter', label: 'Pemberi komentar' },
  { value: 'editor', label: 'Editor' },
]
const roleLabel = r => ROLES.find(x => x.value === r)?.label ?? r

// item: { type, item }. me: the logged-in account. onLeft: called when the user removed themself.
export default function ShareDialog({ target, me, onClose, onLeft }) {
  const ref = useModal()
  const { item } = target
  const [d, setD] = useState(null) // { owner, role, people }
  const [everyone, setEveryone] = useState([])
  const [pick, setPick] = useState('')
  const [role, setRole] = useState('viewer')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const changed = useRef(false)

  const load = () => api(`/api/shares/${item.id}`).then(setD, e => setErr(e.message))
  useEffect(() => {
    load()
    api('/api/people').then(p => setEveryone(p.people), () => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const canManage = d && (d.role === 'owner' || d.role === 'editor')
  // People who do not have access yet (the owner always has it).
  const candidates = d ? everyone.filter(p => p.id !== d.owner.id && !d.people.some(g => g.user_id === p.id)) : []
  useEffect(() => {
    if (candidates.length && !candidates.some(p => p.id === pick)) setPick(candidates[0].id)
  }, [candidates.length]) // eslint-disable-line react-hooks/exhaustive-deps

  const put = async (userID, r) => {
    setBusy(true)
    setErr('')
    try {
      await api(`/api/shares/${item.id}`, { method: 'PUT', body: { user_id: userID, role: r } })
      changed.current = true
      await load()
    } catch (e) { setErr(e.message) } finally { setBusy(false) }
  }
  const remove = async userID => {
    setBusy(true)
    setErr('')
    try {
      await api(`/api/shares/${item.id}/${userID}`, { method: 'DELETE' })
      changed.current = true
      if (userID === me.id) { onLeft?.(); ref.current.close(); return }
      await load()
    } catch (e) { setErr(e.message) } finally { setBusy(false) }
  }

  return (
    <dialog ref={ref} className="sheet share" onClose={() => onClose(changed.current)} aria-labelledby="share-title">
      <h2 id="share-title" className="share-title">Bagikan “{item.name}”</h2>

      {canManage && (candidates.length > 0 ? (
        <div className="share-add">
          <Dropdown label="Bagikan dengan" value={pick} onChange={setPick}
            options={candidates.map(p => ({ value: p.id, label: p.username }))} />
          <Dropdown label="Peran" value={role} onChange={setRole} options={ROLES} />
          <button className="btn primary" disabled={busy || !pick} onClick={() => put(pick, role)}>Bagikan</button>
        </div>
      ) : d && (
        <p className="muted small">Semua akun keluarga sudah punya akses. Tambahkan akun baru lewat halaman Pengguna.</p>
      ))}

      <h3 className="share-sub">Orang yang punya akses</h3>
      {!d ? <p className="muted small" aria-busy="true">Memuat…</p> : (
        <ul className="share-people">
          <li>
            <Avatar name={d.owner.username} />
            <span className="share-who">{d.owner.username}{d.owner.id === me.id && <span className="muted"> (kamu)</span>}</span>
            <span className="muted small">Pemilik</span>
          </li>
          {d.people.map(g => (
            <li key={g.user_id}>
              <Avatar name={g.username} />
              <span className="share-who">{g.username}{g.user_id === me.id && <span className="muted"> (kamu)</span>}</span>
              {canManage ? (
                <Dropdown label={`Peran ${g.username}`} value={g.role}
                  onChange={v => (v === 'remove' ? remove(g.user_id) : put(g.user_id, v))}
                  options={[...ROLES, '-', { value: 'remove', label: 'Hapus akses', danger: true, icon: UserMinus }]} />
              ) : g.user_id === me.id ? (
                <Dropdown label="Peran kamu" value={g.role} onChange={v => v === 'remove' && remove(me.id)}
                  options={[{ value: g.role, label: roleLabel(g.role) }, '-', { value: 'remove', label: 'Hapus akses saya', danger: true, icon: UserMinus }]} />
              ) : (
                <span className="muted small">{roleLabel(g.role)}</span>
              )}
            </li>
          ))}
        </ul>
      )}
      {target.type === 'folder' && <p className="muted small">Akses ke folder berlaku juga untuk semua file dan subfolder di dalamnya.</p>}
      {err && <p className="err" role="alert">{err}</p>}
      <footer>
        <button className="btn primary" onClick={() => ref.current.close()}>Selesai</button>
      </footer>
    </dialog>
  )
}
