// Autosave for documents, shared by the text editor and the rich (WYSIWYG) editor.
// The editor calls changed() on every edit; SAVE_AFTER ms after the last one, read() gives the
// text to save. One save at a time; on a conflict (someone else saved meanwhile) saving stops
// until the user chooses to save theirs anyway.
import { useEffect, useRef, useState } from 'react'
import { Check } from 'lucide-react'
import { saveContent } from './api.js'

const SAVE_AFTER = 1500

export function useAutosave({ file, rev, initial, read, onSaved }) {
  const [status, setStatus] = useState('saved') // saved | dirty | saving | error | conflict
  const s = useRef({ rev, saved: initial, busy: false, timer: 0, theirs: null, savedOnce: false, dirty: false })
  const statusRef = useRef(status)
  statusRef.current = status
  const readRef = useRef(read)
  readRef.current = read

  const schedule = () => {
    clearTimeout(s.current.timer)
    s.current.timer = setTimeout(() => save(false), SAVE_AFTER)
  }
  const save = async force => {
    const st = s.current
    if (st.busy) return
    clearTimeout(st.timer)
    st.dirty = false
    const body = readRef.current()
    if (body === st.saved && !force) return setStatus(statusRef.current === 'conflict' ? 'conflict' : 'saved')
    st.busy = true
    setStatus('saving')
    try {
      const r = await saveContent(file.id, force ? st.theirs : st.rev, body)
      st.rev = String(r.rev)
      st.saved = body
      if (!st.savedOnce) { st.savedOnce = true; onSaved?.() }
      if (st.dirty) { setStatus('dirty'); schedule() } else setStatus('saved')
    } catch (e) {
      if (e.status === 409) { st.theirs = e.rev; setStatus('conflict') } else setStatus('error')
    } finally { st.busy = false }
  }
  const changed = () => {
    s.current.dirty = true
    if (statusRef.current === 'conflict') return
    if (!s.current.busy) setStatus('dirty')
    schedule()
  }

  // Unsaved changes: warn before the tab closes, and save straight away when the editor goes.
  useEffect(() => {
    const pending = () => s.current.dirty || statusRef.current === 'dirty' || statusRef.current === 'error'
    const warn = e => { if (pending() || s.current.busy) e.preventDefault() }
    addEventListener('beforeunload', warn)
    return () => {
      removeEventListener('beforeunload', warn)
      clearTimeout(s.current.timer)
      if (pending() && statusRef.current !== 'conflict') {
        const body = readRef.current()
        if (body !== s.current.saved) saveContent(file.id, s.current.rev, body).then(() => onSaved?.(), () => {})
      }
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // finish saves what is left and resolves with { text, rev } once everything is saved.
  const finish = async () => {
    if (s.current.dirty || statusRef.current === 'dirty' || statusRef.current === 'error') await save(false)
    return s.current.dirty || s.current.saved !== readRef.current() ? null : { text: s.current.saved, rev: s.current.rev }
  }
  return { status, changed, save, finish }
}

const LABELS = { saved: 'Tersimpan', dirty: 'Belum disimpan', saving: 'Menyimpan…', error: 'Gagal menyimpan', conflict: 'Ada versi lebih baru' }

export function SaveStatus({ status, onRetry }) {
  return (
    <>
      <span className={`small doc-status ${status}`} role="status">{status === 'saved' && <Check size={14} aria-hidden />} {LABELS[status]}</span>
      {status === 'error' && <button className="btn" onClick={onRetry}>Coba lagi</button>}
    </>
  )
}

export function ConflictBanner({ status, onForce }) {
  if (status !== 'conflict') return null
  return (
    <div className="banner doc-conflict" role="alert">
      <span>Seseorang menyimpan file ini saat kamu mengedit.</span>
      <button className="btn" onClick={onForce}>Simpan punyaku (versi mereka tetap di riwayat)</button>
    </div>
  )
}
