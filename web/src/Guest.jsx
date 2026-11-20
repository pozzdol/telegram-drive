// Opening a public link (/s/<token>) without an account: asks for the link's password and the
// guest's name when the link wants them, then hands a guest session to the app.
import { useEffect, useState } from 'react'
import { Eye, EyeOff, Link2Off } from 'lucide-react'
import { api } from './api.js'
import { FolderIcon, KindIcon } from './ui.jsx'

const key = token => `guest:${token}`

// Forgets a stored guest session (it stopped working: new password, link reset or turned off).
export const forgetGuest = token => { try { sessionStorage.removeItem(key(token)) } catch { /* nothing stored */ } }

export default function GuestGate({ token, onReady }) {
  const [info, setInfo] = useState(null)
  const [gone, setGone] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [shown, setShown] = useState(false)

  const open = async body => {
    setBusy(true)
    setErr('')
    try {
      const g = await api(`/api/s/${token}/session`, { method: 'POST', body })
      try { sessionStorage.setItem(key(token), JSON.stringify(g)) } catch { /* reload asks again */ }
      onReady(g)
    } catch (e) {
      if (e.status === 404) setGone(true)
      setErr(e.status === 403 ? 'Password salah.' : e.status === 429 ? 'Terlalu banyak percobaan. Coba lagi dalam 15 menit.' : e.message)
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    let stored = null
    try { stored = JSON.parse(sessionStorage.getItem(key(token))) } catch { /* none */ }
    if (stored?.guest) return onReady(stored)
    api(`/api/s/${token}`).then(i => (i.password || i.require_name ? setInfo(i) : open({})), e => (e.status === 404 ? setGone(true) : setErr(e.message)))
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  if (gone) {
    return (
      <main className="login">
        <div className="login-card" role="alert">
          <Link2Off size={36} className="login-mark" aria-hidden />
          <h1>Link ini tidak berlaku lagi</h1>
          <p className="muted">Mungkin pemiliknya sudah mematikan atau mengganti link, atau masa berlakunya sudah habis. Minta link baru ke orang yang membagikannya.</p>
        </div>
      </main>
    )
  }
  if (!info) {
    return (
      <main className="login" aria-busy={!err}>
        <div className="login-card">{err ? <p className="err" role="alert">{err}</p> : <p className="muted">Membuka link…</p>}</div>
      </main>
    )
  }
  const submit = e => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    open({ password: f.get('password') || '', name: (f.get('name') || '').trim() })
  }
  return (
    <main className="login">
      <form className="login-card" onSubmit={submit}>
        <span className="login-mark guest-mark" aria-hidden>{info.type === 'folder' ? <FolderIcon size={36} /> : <KindIcon mime="" size={36} />}</span>
        <h1 className="guest-name">{info.name}</h1>
        <p className="muted">Dibagikan oleh {info.owner}</p>
        {info.password && (
          <label>Password link
            <div className="pw-field">
              <input name="password" type={shown ? 'text' : 'password'} required autoFocus autoComplete="off" />
              <button type="button" className="icon-btn" onClick={() => setShown(s => !s)}
                aria-label={shown ? 'Sembunyikan password' : 'Tampilkan password'} aria-pressed={shown}>
                {shown ? <EyeOff size={18} /> : <Eye size={18} />}
              </button>
            </div>
          </label>
        )}
        {info.require_name && (
          <label>Nama kamu
            <input name="name" required maxLength={60} autoFocus={!info.password} autoComplete="name" />
            <span className="muted small">Dicatat setiap kali kamu menambah atau mengubah isi.</span>
          </label>
        )}
        {err && <p className="err" role="alert">{err}</p>}
        <button className="btn primary wide" disabled={busy}>{busy ? 'Membuka…' : 'Buka'}</button>
      </form>
    </main>
  )
}
