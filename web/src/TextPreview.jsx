// Text and Markdown preview inside the viewer. Reads only the start of big files (Range),
// renders Markdown through marked and then DOMPurify, so a .md file can never run scripts.
// An editor can switch to editing the text; it saves itself a moment after typing stops.
import { Suspense, lazy, useEffect, useRef, useState } from 'react'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Pencil } from 'lucide-react'
import { fileURL, pictureURL } from './api.js'
import { fmtSize } from './ui.jsx'
import { ConflictBanner, SaveStatus, useAutosave } from './autosave.jsx'
import { richSupported } from './markdown.js'
// The rich editor (TipTap) loads only when an editor opens a Markdown document.
const DocEditor = lazy(() => import('./DocEditor.jsx'))

const LIMIT = 512 * 1024
const MAX_EDIT = 1024 * 1024 // the server's limit for a document

// Links in a document open in a new tab and do not get access to this page.
DOMPurify.addHook('afterSanitizeAttributes', node => {
  if (node.tagName === 'A' && node.getAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

// Pictures linked by relative path load through the document (see pictureURL).
const renderMarkdown = (text, docId) => {
  const frag = DOMPurify.sanitize(marked.parse(text, { gfm: true }), { USE_PROFILES: { html: true }, RETURN_DOM_FRAGMENT: true })
  frag.querySelectorAll('img').forEach(img => img.setAttribute('src', pictureURL(docId, img.getAttribute('src') || '')))
  const box = document.createElement('div')
  box.appendChild(frag)
  return box.innerHTML
}

// src: another URL to read (an old version), shown read-only. canEdit: the user may edit.
// startEditing: a new document opens straight in the editor. onSaved: after the first save.
export default function TextPreview({ file, markdown, onMissing, src, canEdit, startEditing, onSaved, onAsk }) {
  const [state, setState] = useState(null) // { text, partial, rev } | { error }
  const [raw, setRaw] = useState(false) // Markdown: show the source instead
  const editable = canEdit && !src && file.size <= MAX_EDIT
  const [editing, setEditing] = useState(false)
  const [gen, setGen] = useState(0) // bumped when the live editor says the file changed elsewhere

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
        if (startEditing && editable && !(markdown && richSupported(text))) setEditing(true) // rich: focuses itself
      })
      .catch(e => live && setState({ error: e.message }))
    return () => { live = false }
  }, [file.id, src, gen]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!state) {
    return (
      <div className="doc-sheet" aria-busy="true" aria-label="Memuat isi file">
        {['80%', '95%', '60%', '90%', '70%', '85%'].map((w, i) => <span key={i} className="sk sk-bar" style={{ width: w, height: 14, marginBottom: 14 }} />)}
      </div>
    )
  }
  if (state.error) return <div className="doc-sheet doc-msg" role="alert">{state.error}</div>
  if (state.binary) return <div className="doc-sheet doc-msg">File ini bukan teks, jadi tidak bisa ditampilkan. Download untuk membukanya.</div>

  // Markdown an editor may change opens straight in the rich editor, as in Google Docs.
  if (markdown && editable && state.rev != null && !editing && richSupported(state.text)) {
    return (
      <Suspense fallback={<div className="doc-sheet" aria-busy="true" />}>
        <DocEditor key={gen} file={file} initial={state.text} rev={state.rev} onSaved={onSaved} onAsk={onAsk} autoFocus={startEditing}
          onReset={() => setGen(g => g + 1)} />
      </Suspense>
    )
  }
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
          {markdown && editable && <span className="muted small">Bagian file ini tidak bisa ditampilkan editor, jadi diedit sebagai teks.</span>}
          {editable && state.rev != null && <button className="btn doc-edit" onClick={() => setEditing(true)}><Pencil size={16} aria-hidden /> Edit</button>}
        </div>
      )}
      {!state.text && <p className="muted">Dokumen ini masih kosong.</p>}
      {markdown && !raw
        ? <article className="md-body" dangerouslySetInnerHTML={{ __html: renderMarkdown(state.text, file.id) }} />
        : <pre className="txt-body">{state.text}</pre>}
    </div>
  )
}

// The plain-text editor (.txt, and Markdown the rich editor cannot keep).
function Editor({ file, initial, rev, onDone, onSaved }) {
  const [text, setText] = useState(initial)
  const latest = useRef(initial)
  const a = useAutosave({ file, rev, initial, read: () => latest.current, onSaved })
  const done = async () => { const r = await a.finish(); if (r) onDone(r.text, r.rev) }
  return (
    <div className="doc-sheet doc-editing">
      <div className="doc-bar">
        <SaveStatus status={a.status} onRetry={() => a.save(false)} />
        <button className="btn primary doc-edit" onClick={done} disabled={a.status === 'saving' || a.status === 'conflict'}>Selesai</button>
      </div>
      <ConflictBanner status={a.status} onForce={() => a.save(true)} />
      <textarea className="txt-body doc-input" value={text} spellCheck={false} aria-label={`Isi ${file.name}`} autoFocus
        onChange={e => { setText(e.target.value); latest.current = e.target.value; a.changed() }} />
    </div>
  )
}
