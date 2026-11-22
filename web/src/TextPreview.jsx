// Text and Markdown preview inside the viewer. Reads only the start of big files (Range),
// renders Markdown through marked and then DOMPurify, so a .md file can never run scripts.
// An editor can switch to editing the text; it saves itself a moment after typing stops.
import { useEffect, useRef, useState } from 'react'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Check, Pencil } from 'lucide-react'
import { fileURL, saveContent } from './api.js'
import { fmtSize } from './ui.jsx'

const LIMIT = 512 * 1024
const MAX_EDIT = 1024 * 1024 // the server's limit for a document
const SAVE_AFTER = 1500 // ms without typing

// Links in a document open in a new tab and do not get access to this page.
DOMPurify.addHook('afterSanitizeAttributes', node => {
  if (node.tagName === 'A' && node.getAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

const renderMarkdown = text => DOMPurify.sanitize(marked.parse(text, { gfm: true }), { USE_PROFILES: { html: true } })

// src: another URL to read (an old version), shown read-only. canEdit: the user may edit.
// startEditing: a new document opens straight in the editor. onSaved: after the first save.
export default function TextPreview({ file, markdown, onMissing, src, canEdit, startEditing, onSaved }) {
  const [state, setState] = useState(null) // { text, partial, rev } | { error }
  const [raw, setRaw] = useState(false) // Markdown: show the source instead
  const editable = canEdit && !src && file.size <= MAX_EDIT
  const [editing, setEditing] = useState(false)

  useEffect(() => {
    let live = true
    setState(null)
    // Editing needs the whole text; a preview only the start.
    const headers = editable ? {} : { Range: `bytes=0-${LIMIT - 1}` }
    fetch(src || fileURL(file.id, true), { credentials: 'include', headers, cache: 'no-store' })
      .then(async r => {
        if (r.status === 410) return onMissing()
        if (!r.ok) throw new Error(`Gagal memuat file (HTTP ${r.status})`)
        const buf = await r.arrayBuffer()
        // A cut in the middle of a multi-byte character becomes one replacement character.
        const text = new TextDecoder('utf-8').decode(buf)
        if (!live) return
        setState({ text, partial: buf.byteLength < file.size && !editable, binary: text.includes('\u0000'), rev: r.headers.get('X-Rev') })
        if (startEditing && editable) setEditing(true)
      })
      .catch(e => live && setState({ error: e.message }))
    return () => { live = false }
  }, [file.id, src]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!state) {
    return (
      <div className="doc-sheet" aria-busy="true" aria-label="Memuat isi file">
        {['80%', '95%', '60%', '90%', '70%', '85%'].map((w, i) => <span key={i} className="sk sk-bar" style={{ width: w, height: 14, marginBottom: 14 }} />)}
      </div>
    )
  }
  if (state.error) return <div className="doc-sheet doc-msg" role="alert">{state.error}</div>
  if (state.binary) return <div className="doc-sheet doc-msg">File ini bukan teks, jadi tidak bisa ditampilkan. Download untuk membukanya.</div>

  if (editing) {
    return <Editor file={file} initial={state.text} rev={state.rev} onSaved={onSaved}
      onDone={(text, rev) => { setState(s => ({ ...s, text, rev })); setEditing(false) }} />
  }

  return (
    <div className="doc-sheet">
      {(markdown || state.partial || (editable && state.rev != null)) && (
        <div className="doc-bar">
          {state.partial && <span className="muted small">Menampilkan {fmtSize(LIMIT)} pertama dari {fmtSize(file.size)}. Download untuk isi lengkap.</span>}
          {markdown && (
            <div className="seg doc-mode" role="group" aria-label="Tampilan dokumen">
              <button aria-pressed={!raw} onClick={() => setRaw(false)}>Tampilan</button>
              <button aria-pressed={raw} onClick={() => setRaw(true)}>Kode</button>
            </div>
          )}
          {editable && state.rev != null && <button className="btn doc-edit" onClick={() => setEditing(true)}><Pencil size={16} aria-hidden /> Edit</button>}
        </div>
      )}
      {!state.text && <p className="muted">Dokumen ini masih kosong.</p>}
      {markdown && !raw
        ? <article className="md-body" dangerouslySetInnerHTML={{ __html: renderMarkdown(state.text) }} />
        : <pre className="txt-body">{state.text}</pre>}
    </div>
  )
}

// The text editor: saves SAVE_AFTER ms after typing stops, one save at a time, and stops on a
// conflict (someone else saved meanwhile) until the user picks what to keep.
function Editor({ file, initial, rev: startRev, onDone, onSaved }) {
  const [text, setText] = useState(initial)
  const [status, setStatus] = useState('saved') // saved | dirty | saving | error | conflict
  const s = useRef({ rev: startRev, saved: initial, busy: false, timer: 0, theirs: null, savedOnce: false })

  const save = async force => {
    const st = s.current
    if (st.busy) return
    const body = st.latest ?? initial
    if (body === st.saved && !force) return setStatus('saved')
    st.busy = true
    setStatus('saving')
    let ok = false
    try {
      const r = await saveContent(file.id, force ? st.theirs : st.rev, body)
      st.rev = String(r.rev)
      st.saved = body
      ok = true
      if (!st.savedOnce) { st.savedOnce = true; onSaved?.() }
    } catch (e) {
      if (e.status === 409) { st.theirs = e.rev; setStatus('conflict') } else setStatus('error')
    } finally { st.busy = false }
    if (!ok) return
    if (st.latest !== undefined && st.latest !== st.saved) { setStatus('dirty'); schedule() } else setStatus('saved')
  }
  const statusRef = useRef(status)
  statusRef.current = status
  const schedule = () => {
    clearTimeout(s.current.timer)
    s.current.timer = setTimeout(() => save(false), SAVE_AFTER)
  }
  const change = v => {
    setText(v)
    s.current.latest = v
    if (statusRef.current === 'conflict') return
    setStatus('dirty')
    schedule()
  }

  // Unsaved text: warn before the tab closes, and save straight away when the editor goes.
  useEffect(() => {
    const warn = e => { if (s.current.latest !== undefined && s.current.latest !== s.current.saved) e.preventDefault() }
    addEventListener('beforeunload', warn)
    return () => {
      removeEventListener('beforeunload', warn)
      clearTimeout(s.current.timer)
      const st = s.current
      if (st.latest !== undefined && st.latest !== st.saved && statusRef.current !== 'conflict') saveContent(file.id, st.rev, st.latest).then(onSaved, () => {})
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const done = () => {
    const st = s.current
    if (st.latest !== undefined && st.latest !== st.saved) return save(false).then(() => { if (s.current.latest === s.current.saved) onDone(s.current.saved, s.current.rev) })
    onDone(text, st.rev)
  }
  const label = { saved: 'Tersimpan', dirty: 'Belum disimpan', saving: 'Menyimpan…', error: 'Gagal menyimpan', conflict: 'Ada versi lebih baru' }[status]

  return (
    <div className="doc-sheet doc-editing">
      <div className="doc-bar">
        <span className={`small doc-status ${status}`} role="status">{status === 'saved' && <Check size={14} aria-hidden />} {label}</span>
        {status === 'error' && <button className="btn" onClick={() => save(false)}>Coba lagi</button>}
        <button className="btn primary doc-edit" onClick={done} disabled={status === 'saving' || status === 'conflict'}>Selesai</button>
      </div>
      {status === 'conflict' && (
        <div className="banner doc-conflict" role="alert">
          <span>Seseorang menyimpan file ini saat kamu mengedit.</span>
          <button className="btn" onClick={() => save(true)}>Simpan punyaku (versi mereka tetap di riwayat)</button>
        </div>
      )}
      <textarea className="txt-body doc-input" value={text} onChange={e => change(e.target.value)} spellCheck={false}
        aria-label={`Isi ${file.name}`} autoFocus maxLength={MAX_EDIT} />
    </div>
  )
}
