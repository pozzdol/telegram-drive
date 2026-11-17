// Text and Markdown preview inside the viewer. Reads only the start of big files (Range),
// renders Markdown through marked and then DOMPurify, so a .md file can never run scripts.
import { useEffect, useState } from 'react'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { fileURL } from './api.js'
import { fmtSize } from './ui.jsx'

const LIMIT = 512 * 1024

// Links in a document open in a new tab and do not get access to this page.
DOMPurify.addHook('afterSanitizeAttributes', node => {
  if (node.tagName === 'A' && node.getAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

const renderMarkdown = text => DOMPurify.sanitize(marked.parse(text, { gfm: true }), { USE_PROFILES: { html: true } })

export default function TextPreview({ file, markdown, onMissing }) {
  const [state, setState] = useState(null) // { text, partial } | { error }
  const [raw, setRaw] = useState(false) // Markdown: show the source instead

  useEffect(() => {
    let live = true
    setState(null)
    fetch(fileURL(file.id), { credentials: 'include', headers: { Range: `bytes=0-${LIMIT - 1}` } })
      .then(async r => {
        if (r.status === 410) return onMissing()
        if (!r.ok) throw new Error(`Gagal memuat file (HTTP ${r.status})`)
        const buf = await r.arrayBuffer()
        // A cut in the middle of a multi-byte character becomes one replacement character.
        const text = new TextDecoder('utf-8').decode(buf)
        if (live) setState({ text, partial: buf.byteLength < file.size, binary: text.includes('\u0000') })
      })
      .catch(e => live && setState({ error: e.message }))
    return () => { live = false }
  }, [file.id]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!state) {
    return (
      <div className="doc-sheet" aria-busy="true" aria-label="Memuat isi file">
        {['80%', '95%', '60%', '90%', '70%', '85%'].map((w, i) => <span key={i} className="sk sk-bar" style={{ width: w, height: 14, marginBottom: 14 }} />)}
      </div>
    )
  }
  if (state.error) return <div className="doc-sheet doc-msg" role="alert">{state.error}</div>
  if (state.binary) return <div className="doc-sheet doc-msg">File ini bukan teks, jadi tidak bisa ditampilkan. Download untuk membukanya.</div>

  return (
    <div className="doc-sheet">
      {(markdown || state.partial) && (
        <div className="doc-bar">
          {state.partial && <span className="muted small">Menampilkan {fmtSize(LIMIT)} pertama dari {fmtSize(file.size)}. Download untuk isi lengkap.</span>}
          {markdown && (
            <div className="seg doc-mode" role="group" aria-label="Tampilan dokumen">
              <button aria-pressed={!raw} onClick={() => setRaw(false)}>Tampilan</button>
              <button aria-pressed={raw} onClick={() => setRaw(true)}>Kode</button>
            </div>
          )}
        </div>
      )}
      {markdown && !raw
        ? <article className="md-body" dangerouslySetInnerHTML={{ __html: renderMarkdown(state.text) }} />
        : <pre className="txt-body">{state.text}</pre>}
    </div>
  )
}
