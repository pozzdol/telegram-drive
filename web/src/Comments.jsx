// Comments on a file, beside the viewer. Anyone who can open the file reads them; commenters,
// editors and the owner write. Authors and editors delete.
import { useEffect, useRef, useState } from 'react'
import { Trash2 } from 'lucide-react'
import { api } from './api.js'
import { fmtDateTime } from './ui.jsx'

export default function Comments({ file, onChanged, onCount }) {
  const [d, setD] = useState(null) // { comments, can_comment }
  const [text, setText] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const list = useRef(null)

  const load = () => api(`/api/files/${file.id}/comments`).then(x => { setD(x); onCount?.(x.comments.length) }, e => setErr(e.message))
  useEffect(() => { setD(null); setErr(''); load() }, [file.id]) // eslint-disable-line react-hooks/exhaustive-deps
  // Newest at the bottom, like a chat: keep it in view.
  useEffect(() => { if (list.current) list.current.scrollTop = list.current.scrollHeight }, [d?.comments.length])

  const send = async e => {
    e?.preventDefault()
    if (!text.trim() || busy) return
    setBusy(true)
    setErr('')
    try {
      await api(`/api/files/${file.id}/comments`, { method: 'POST', body: { body: text } })
      setText('')
      await load()
      onChanged?.()
    } catch (e) { setErr(e.message) } finally { setBusy(false) }
  }
  const remove = async c => {
    try { await api(`/api/comments/${c.id}`, { method: 'DELETE' }); await load(); onChanged?.() } catch (e) { setErr(e.message) }
  }

  return (
    <aside className="lb-comments" aria-label="Komentar">
      <h2 className="lb-side-title">Komentar</h2>
      <div className="cmt-list" ref={list}>
        {!d ? <p className="muted small">Memuat…</p> : !d.comments.length ? (
          <p className="muted small">{d.can_comment ? 'Belum ada komentar. Tulis yang pertama.' : 'Belum ada komentar.'}</p>
        ) : d.comments.map(c => (
          <article key={c.id} className="cmt">
            <header>
              <strong>{c.author}</strong>{c.guest && <span className="muted"> · tamu</span>}
              <time className="muted" dateTime={new Date(c.created_at * 1000).toISOString()}>{fmtDateTime(c.created_at)}</time>
              {c.can_delete && (
                <button className="icon-btn" onClick={() => remove(c)} aria-label="Hapus komentar" data-tip="Hapus komentar"><Trash2 size={16} /></button>
              )}
            </header>
            <p>{c.body}</p>
          </article>
        ))}
      </div>
      {err && <p className="err small" role="alert">{err}</p>}
      {d?.can_comment && (
        <form className="cmt-form" onSubmit={send}>
          <textarea value={text} onChange={e => setText(e.target.value)} maxLength={2000} rows={2} aria-label="Tulis komentar"
            placeholder="Tulis komentar…" onKeyDown={e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) send(e) }} />
          <button className="btn primary" disabled={busy || !text.trim()}>{busy ? 'Mengirim…' : 'Kirim'}</button>
        </form>
      )}
    </aside>
  )
}
