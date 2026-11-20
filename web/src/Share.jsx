// Share a file or folder with other family accounts, and change or take away their access.
import { useEffect, useRef, useState } from 'react'
import { Globe, Link2, Lock, RefreshCw, UserMinus } from 'lucide-react'
import { api } from './api.js'
import { Dropdown, Switch, fmtDate, fmtDateTime, fmtSize, useModal } from './ui.jsx'
import { Avatar } from './Users.jsx'

export const ROLES = [
  { value: 'viewer', label: 'Pelihat' },
  { value: 'commenter', label: 'Pemberi komentar' },
  { value: 'editor', label: 'Editor' },
]
const roleLabel = r => ROLES.find(x => x.value === r)?.label ?? r

// target: { type, item }. me: the logged-in account, or { guest: true } for a guest of a public
// link (who sees only the link settings). onLeft: called when the user removed themself.
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
    if (me.guest) return
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

      {!me.guest && <>
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
      </>}
      {(me.guest || canManage) && <LinkSection item={item} onError={setErr} onChanged={() => { changed.current = true }} />}
      {target.type === 'folder' && <p className="muted small">Akses ke folder berlaku juga untuk semua file dan subfolder di dalamnya.</p>}
      {err && <p className="err" role="alert">{err}</p>}
      <footer>
        <button className="btn primary" onClick={() => ref.current.close()}>Selesai</button>
      </footer>
    </dialog>
  )
}

const DAY = 86400
const LINK_ROLE_TEXT = {
  viewer: 'Siapa saja yang punya link bisa melihat, tanpa login.',
  commenter: 'Siapa saja yang punya link bisa melihat dan berkomentar, tanpa login.',
  editor: 'Siapa saja yang punya link bisa menambah, mengubah, dan menghapus isi, tanpa login.',
}
const sizes = list => list.map(n => ({ value: String(n), label: fmtSize(n) }))
const GB = 1 << 30

// "Akses umum": the item's one public link and its settings, plus who changed access lately.
function LinkSection({ item, onError, onChanged }) {
  const [l, setL] = useState(null)
  const [more, setMore] = useState(false)
  const [pw, setPw] = useState('')
  const [copied, setCopied] = useState(false)
  const [resetting, setResetting] = useState(false) // asked once; the second click resets
  const [log, setLog] = useState(false)
  useEffect(() => { api(`/api/links/${item.id}`).then(setL, e => onError(e.message)) }, [item.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const put = async body => {
    try { setL(await api(`/api/links/${item.id}`, { method: 'PUT', body })); onChanged() } catch (e) { onError(e.message) }
  }
  const reset = async () => {
    if (!resetting) return setResetting(true)
    setResetting(false)
    try { setL(await api(`/api/links/${item.id}/reset`, { method: 'POST' })); onChanged() } catch (e) { onError(e.message) }
  }
  if (!l) return <p className="muted small" aria-busy="true">Memuat link…</p>
  const url = l.token ? `${location.origin}/s/${l.token}` : ''
  const copy = async () => {
    try { await navigator.clipboard.writeText(url); setCopied(true); setTimeout(() => setCopied(false), 2000) } catch { onError('Tidak bisa menyalin. Salin link dari kolom di atas.') }
  }
  const expiry = l.expires_at
    ? [{ value: 'set', label: `Sampai ${fmtDate(l.expires_at)}` }] : []

  return (
    <section className="link-sec" aria-labelledby="link-title">
      <h3 id="link-title" className="share-sub">Akses umum</h3>
      <div className="link-row">
        <span className={`link-ic${l.enabled ? ' on' : ''}`} aria-hidden>{l.enabled ? <Globe size={20} /> : <Lock size={20} />}</span>
        <div className="link-main">
          <Dropdown label="Akses umum" value={l.enabled ? 'on' : 'off'} onChange={v => put({ enabled: v === 'on' })}
            options={[{ value: 'off', label: 'Terbatas' }, { value: 'on', label: 'Siapa saja yang punya link' }]} />
          <span className="muted small">{l.enabled ? LINK_ROLE_TEXT[l.role] : 'Hanya orang yang ditambahkan yang bisa membuka.'}</span>
        </div>
        {l.enabled && <Dropdown label="Peran link" value={l.role} onChange={v => put({ role: v })} options={ROLES} />}
      </div>

      {l.enabled && (
        <>
          <div className="link-copy">
            <input readOnly value={url} aria-label="Link" onFocus={e => e.currentTarget.select()} />
            <button className="btn ghost" onClick={copy}><Link2 size={18} aria-hidden /> {copied ? 'Disalin' : 'Salin link'}</button>
          </div>
          <button className="btn ghost link-more" onClick={() => setMore(m => !m)} aria-expanded={more}>
            {more ? 'Sembunyikan pengaturan link' : 'Pengaturan link'}
          </button>
        </>
      )}

      {l.enabled && more && (
        <div className="link-settings">
          <div className="link-field">
            <span>Berlaku</span>
            <Dropdown label="Berlaku" value={l.expires_at ? 'set' : '0'}
              onChange={v => v !== 'set' && put({ expires_at: v === '0' ? 0 : Math.floor(Date.now() / 1000) + Number(v) * DAY })}
              options={[{ value: '0', label: 'Selamanya' }, ...expiry, { value: '1', label: '1 hari' }, { value: '7', label: '7 hari' }, { value: '30', label: '30 hari' }]} />
          </div>
          <div className="link-field">
            <span>Password</span>
            {l.password ? (
              <span className="link-pw-set">Aktif <button className="btn ghost" onClick={() => put({ password: '' })}>Hapus password</button></span>
            ) : (
              <form className="link-pw" onSubmit={e => { e.preventDefault(); if (pw.length >= 4) { put({ password: pw }); setPw('') } }}>
                <input type="password" value={pw} onChange={e => setPw(e.target.value)} placeholder="Minimal 4 karakter" aria-label="Password link" autoComplete="new-password" />
                <button className="btn ghost" disabled={pw.length < 4}>Pasang</button>
              </form>
            )}
          </div>
          {l.role !== 'editor' && (
            <Switch checked={l.no_download} onChange={v => put({ no_download: v })}>Matikan download (hanya pratinjau)</Switch>
          )}
          {l.role !== 'viewer' && (
            <Switch checked={l.require_name} onChange={v => put({ require_name: v })}>Tamu wajib mengisi nama</Switch>
          )}
          {l.role === 'editor' && (
            <>
              <div className="link-field">
                <span>Upload tamu per file</span>
                <Dropdown label="Batas per file" value={String(l.max_file)} onChange={v => put({ max_file: Number(v) })}
                  options={sizes([100 << 20, 500 << 20, 2 * GB])} />
              </div>
              <div className="link-field">
                <span>Total upload tamu <span className="muted">({fmtSize(l.uploaded)} terpakai)</span></span>
                <Dropdown label="Batas total" value={String(l.upload_limit)} onChange={v => put({ upload_limit: Number(v) })}
                  options={sizes([GB, 5 * GB, 20 * GB, 100 * GB])} />
              </div>
            </>
          )}
          <button className={`btn ghost${resetting ? ' danger' : ''}`} onClick={reset} onBlur={() => setResetting(false)}>
            <RefreshCw size={18} aria-hidden /> {resetting ? 'Klik lagi: link lama langsung tidak berlaku' : 'Buat link baru'}
          </button>
          {l.log.length > 0 && (
            <div className="link-log">
              <button className="btn ghost" onClick={() => setLog(v => !v)} aria-expanded={log}>
                {log ? 'Sembunyikan riwayat' : 'Riwayat perubahan akses'}
              </button>
              {log && (
                <ul>
                  {l.log.map((x, i) => <li key={i}><strong>{x.actor}</strong> {x.action} <span className="muted">· {fmtDateTime(x.at)}</span></li>)}
                </ul>
              )}
            </div>
          )}
        </div>
      )}
    </section>
  )
}
