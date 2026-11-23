// The rich Markdown editor (TipTap): edits a document as it looks, saves it back as Markdown.
// Normally live: everyone editing the document sees each other's typing and cursors (Yjs through
// app/controllers/live), and one of them, the saver, writes it to Telegram. If the live
// connection cannot be made, it edits alone with autosave instead.
// "Sumber" shows the raw Markdown; it can be edited when nobody else is in the document.
import { useEffect, useRef, useState } from 'react'
import { EditorContent, useEditor, useEditorState } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import { Markdown } from '@tiptap/markdown'
import { TaskItem, TaskList } from '@tiptap/extension-list'
import { TableKit } from '@tiptap/extension-table'
import { Placeholder } from '@tiptap/extensions'
import Image from '@tiptap/extension-image'
import { mergeAttributes } from '@tiptap/core'
import Collaboration from '@tiptap/extension-collaboration'
import CollaborationCaret from '@tiptap/extension-collaboration-caret'
import {
  Bold, Check, Code, ImagePlus, FileCode2, Italic, Link2, List, ListOrdered, ListTodo, Minus, Quote, Redo2, SquareCode,
  Strikethrough, Table, Undo2, WifiOff,
} from 'lucide-react'
import { api, pictureURL, saveContent, uploadFile } from './api.js'
import { ConflictBanner, SaveStatus, useAutosave } from './autosave.jsx'
import { Live } from './live.js'
import { richSupported, splitFrontMatter, tidy } from './markdown.js'
import { Dropdown } from './ui.jsx'

// Pictures keep their relative path (notes.md.assets/photo.png) in the document and in the
// Markdown; only what the page shows goes through the document's picture URL.
const Picture = Image.extend({
  addOptions() { return { ...this.parent?.(), docId: '' } },
  renderHTML({ HTMLAttributes }) {
    return ['img', mergeAttributes(this.options.HTMLAttributes, HTMLAttributes, { src: pictureURL(this.options.docId, HTMLAttributes.src) })]
  },
})

const base = ({ undo, docId }) => [
  StarterKit.configure({
    underline: false, // Markdown has no underline
    undoRedo: undo ? {} : false, // live: Yjs keeps the undo history (only your own changes)
    link: { openOnClick: false, autolink: true, defaultProtocol: 'https' },
  }),
  TaskList,
  TaskItem.configure({ nested: true }),
  TableKit.configure({ table: { resizable: false } }),
  Placeholder.configure({ placeholder: 'Mulai menulis…' }),
  Picture.configure({ docId }),
  Markdown,
]

const MAX_PICTURE = 20 * 1024 * 1024

// Adding pictures: upload into the document's pictures folder (made on first use), then insert
// them where they were dropped or at the cursor. Names that are taken get " (2)", " (3)"…
function usePictures(file, ed) {
  const [busy, setBusy] = useState(0)
  const [err, setErr] = useState('')
  const add = async (list, pos) => {
    const pics = [...list].filter(f => f.type.startsWith('image/'))
    if (!pics.length) return false
    setErr('')
    if (pics.some(f => f.size > MAX_PICTURE)) { setErr('Gambar maksimal 20 MB.'); return true }
    setBusy(n => n + pics.length)
    try {
      const a = await api(`/api/files/${file.id}/assets`, { method: 'POST' })
      const taken = new Set(a.files)
      for (const f of pics) {
        let name = /^image\.\w+$/.test(f.name) ? f.name.replace(/^image/, 'gambar') : f.name // pasted from the clipboard
        const [stem, ext] = /^(.*?)(\.[^.]*)?$/.exec(name).slice(1)
        for (let i = 2; taken.has(name); i++) name = `${stem} (${i})${ext || ''}`
        taken.add(name)
        await uploadFile(new File([f], name, { type: f.type }), a.folder_id, () => {})
        const src = `${encodeURI(a.name)}/${encodeURI(name)}`
        const node = { type: 'image', attrs: { src, alt: stem } }
        const c = ed.current.chain().focus()
        ;(pos != null ? c.insertContentAt(pos, node) : c.insertContent(node)).run()
        setBusy(n => n - 1)
      }
    } catch (e) {
      setErr(`Gagal menambah gambar: ${e.message}`)
      setBusy(0)
    }
    return true
  }
  const pick = () => {
    const input = document.createElement('input')
    input.type = 'file'
    input.accept = 'image/*'
    input.multiple = true
    input.onchange = () => add(input.files)
    input.click()
  }
  return { busy, err, add, pick }
}

// Paste and drop of picture files go to usePictures; everything else is the editor's.
const pictureProps = pics => ({
  handlePaste: (view, e) => {
    const files = [...(e.clipboardData?.files || [])]
    if (!files.some(f => f.type.startsWith('image/'))) return false
    pics.current.add(files)
    return true
  },
  handleDrop: (view, e, slice, moved) => {
    const files = [...(e.dataTransfer?.files || [])]
    if (moved || !files.some(f => f.type.startsWith('image/'))) return false
    pics.current.add(files, view.posAtCoords({ left: e.clientX, top: e.clientY })?.pos)
    return true
  },
})

function PictureStatus({ pics }) {
  if (pics.err) return <p className="err small" role="alert">{pics.err}</p>
  if (pics.busy) return <p className="muted small" role="status">Mengupload {pics.busy} gambar…</p>
  return null
}

const STYLES = [
  { value: 'p', label: 'Teks biasa' },
  { value: 1, label: 'Judul 1' },
  { value: 2, label: 'Judul 2' },
  { value: 3, label: 'Judul 3' },
]

const SAVE_IDLE = 5000 // live saver: after this long without changes
const SAVE_MAX = 60000 // and at least this often while typing goes on

export default function DocEditor(props) {
  const [solo, setSolo] = useState(false)
  return solo ? <SoloEditor {...props} /> : <LiveEditor {...props} onFail={() => setSolo(true)} />
}

// A toolbar button.
const B = ({ on, label, icon: I, onClick, disabled }) => (
  <button type="button" className="icon-btn" aria-label={label} data-tip={label} aria-pressed={on} onClick={onClick} disabled={disabled}>
    <I size={18} />
  </button>
)

function Toolbar({ editor, onAsk, onPicture }) {
  const st = useEditorState({
    editor,
    selector: ({ editor: e }) => e && {
      heading: [1, 2, 3].find(l => e.isActive('heading', { level: l })) ?? 'p',
      bold: e.isActive('bold'), italic: e.isActive('italic'), strike: e.isActive('strike'), code: e.isActive('code'),
      link: e.isActive('link'), bullet: e.isActive('bulletList'), ordered: e.isActive('orderedList'), task: e.isActive('taskList'),
      quote: e.isActive('blockquote'), codeBlock: e.isActive('codeBlock'), table: e.isActive('table'),
      undo: e.can().undo(), redo: e.can().redo(), editable: e.isEditable,
    },
  })
  useEffect(() => {
    const key = e => { if ((e.ctrlKey || e.metaKey) && e.key === 'k' && editor.isFocused) { e.preventDefault(); setLink() } }
    addEventListener('keydown', key)
    return () => removeEventListener('keydown', key)
  }) // eslint-disable-line react-hooks/exhaustive-deps
  if (!st) return null
  const run = f => () => f(editor.chain().focus()).run()
  async function setLink() {
    const prev = editor.getAttributes('link').href
    const url = await onAsk({ title: prev ? 'Ubah link' : 'Tambah link', input: true, value: prev || 'https://', confirm: 'Simpan',
      message: 'Kosongkan untuk menghapus link.' })
    if (url == null) return
    const c = editor.chain().focus().extendMarkRange('link')
    ;(url.trim() && url.trim() !== 'https://' ? c.setLink({ href: url.trim() }) : c.unsetLink()).run()
  }
  return (
    <>
      <fieldset className="doc-tools" role="toolbar" aria-label="Format teks" disabled={!st.editable}>
        <B label="Urungkan (Ctrl+Z)" icon={Undo2} onClick={run(c => c.undo())} disabled={!st.undo} />
        <B label="Ulangi (Ctrl+Shift+Z)" icon={Redo2} onClick={run(c => c.redo())} disabled={!st.redo} />
        <span className="lb-sep" aria-hidden />
        <Dropdown label="Gaya paragraf" value={st.heading} options={STYLES}
          onChange={v => (v === 'p' ? editor.chain().focus().setParagraph().run() : editor.chain().focus().setHeading({ level: v }).run())} />
        <span className="lb-sep" aria-hidden />
        <B label="Tebal (Ctrl+B)" icon={Bold} on={st.bold} onClick={run(c => c.toggleBold())} />
        <B label="Miring (Ctrl+I)" icon={Italic} on={st.italic} onClick={run(c => c.toggleItalic())} />
        <B label="Coret" icon={Strikethrough} on={st.strike} onClick={run(c => c.toggleStrike())} />
        <B label="Kode" icon={Code} on={st.code} onClick={run(c => c.toggleCode())} />
        <B label="Link (Ctrl+K)" icon={Link2} on={st.link} onClick={setLink} />
        <span className="lb-sep" aria-hidden />
        <B label="Daftar berbutir" icon={List} on={st.bullet} onClick={run(c => c.toggleBulletList())} />
        <B label="Daftar bernomor" icon={ListOrdered} on={st.ordered} onClick={run(c => c.toggleOrderedList())} />
        <B label="Daftar centang" icon={ListTodo} on={st.task} onClick={run(c => c.toggleTaskList())} />
        <B label="Kutipan" icon={Quote} on={st.quote} onClick={run(c => c.toggleBlockquote())} />
        <B label="Blok kode" icon={SquareCode} on={st.codeBlock} onClick={run(c => c.toggleCodeBlock())} />
        <B label="Tabel" icon={Table} on={st.table} onClick={run(c => c.insertTable({ rows: 3, cols: 3, withHeaderRow: true }))} />
        <B label="Garis pemisah" icon={Minus} onClick={run(c => c.setHorizontalRule())} />
        <B label="Gambar (atau tempel/seret ke sini)" icon={ImagePlus} onClick={onPicture} />
      </fieldset>
      {st.table && st.editable && (
        <div className="doc-tools doc-table-tools" role="toolbar" aria-label="Tabel">
          <button type="button" className="btn" onClick={run(c => c.addRowAfter())}>+ Baris</button>
          <button type="button" className="btn" onClick={run(c => c.addColumnAfter())}>+ Kolom</button>
          <button type="button" className="btn" onClick={run(c => c.deleteRow())}>Hapus baris</button>
          <button type="button" className="btn" onClick={run(c => c.deleteColumn())}>Hapus kolom</button>
          <button type="button" className="btn" onClick={run(c => c.deleteTable())}>Hapus tabel</button>
        </div>
      )}
    </>
  )
}

// "Sumber": the raw Markdown. editable false shows it read-only. apply(text) puts it back.
function useSource({ editor, frontRef }) {
  const [source, setSource] = useState(null)
  const [err, setErr] = useState('')
  const open = () => { setErr(''); setSource(frontRef.current + tidy(editor.getMarkdown())) }
  const close = apply => {
    setErr('')
    if (apply) {
      if (!richSupported(source)) return setErr('Teks ini memakai Markdown yang tidak bisa ditampilkan editor. Hapus bagian itu dulu, atau batalkan.')
      const [f, b] = splitFrontMatter(source)
      frontRef.current = f
      editor.commands.setContent(b, { contentType: 'markdown' })
    }
    setSource(null)
  }
  return { source, setSource, err, open, close }
}

const editorProps = (file, pics) => ({ attributes: { class: 'md-body doc-rich', 'aria-label': `Isi ${file.name}`, spellcheck: 'true' }, ...pictureProps(pics) })

// ---- live: together with everyone who has it open ----

function LiveEditor({ file, initial, onSaved, onAsk, autoFocus, onFail, onReset }) {
  const [front, body] = splitFrontMatter(initial)
  const frontRef = useRef(front)
  const [, rerender] = useState(0)
  const ed = useRef(null)
  const save = useRef({ timer: 0, since: 0, busy: false, savedOnce: false, error: false })
  const picsRef = useRef(null)
  const [live] = useState(() => new Live(file.id, {
    seed: () => ed.current.commands.setContent(body, { contentType: 'markdown' }),
    hello: (first, m) => {
      ed.current.commands.updateUser({ name: m.name, color: m.color })
      ed.current.setEditable(true)
      if (first && autoFocus) ed.current.commands.focus('end')
    },
    state: () => { plan(); rerender(n => n + 1) },
    reset: () => onReset?.(),
    fail: () => onFail(),
  }))
  const editor = useEditor({
    extensions: [
      ...base({ undo: false, docId: file.id }),
      Collaboration.configure({ document: live.doc }),
      CollaborationCaret.configure({ provider: live, user: { name: '', color: '#868e96' } }),
    ],
    editable: false, // until the document arrives
    editorProps: editorProps(file, picsRef),
  })
  ed.current = editor
  const pics = usePictures(file, ed)
  picsRef.current = pics
  const src = useSource({ editor, frontRef })

  // The saver writes the Markdown to Telegram SAVE_IDLE after the last change, at least every SAVE_MAX.
  function plan() {
    const s = save.current
    clearTimeout(s.timer)
    if (!live.saver || !live.online || s.busy || (live.seq <= live.savedSeq && !live.unacked)) { if (live.seq <= live.savedSeq) s.since = 0; return }
    if (!s.since) s.since = Date.now()
    s.timer = setTimeout(doSave, Math.max(0, Math.min(SAVE_IDLE, s.since + SAVE_MAX - Date.now())))
  }
  async function doSave() {
    const s = save.current
    if (live.unacked) return plan() // wait until the server has numbered our own edits
    const seq = live.seq
    s.busy = true
    try {
      await saveContent(file.id, live.rev, frontRef.current + tidy(ed.current.getMarkdown()), { liveSeq: seq })
      live.compact(seq)
      s.since = 0
      s.error = false
      if (!s.savedOnce) { s.savedOnce = true; onSaved?.() }
    } catch (e) {
      s.error = e.status !== 409 // 409: changed outside; the server resets everyone
      if (s.error) setTimeout(plan, 10000)
    } finally { s.busy = false }
    rerender(n => n + 1)
  }

  useEffect(() => {
    // Closing the tab while this browser still has to save: send it on the way out.
    const out = () => {
      if (live.saver && live.seq > live.savedSeq) {
        const md = frontRef.current + tidy(ed.current.getMarkdown())
        if (md.length < 60000) saveContent(file.id, live.rev, md, { liveSeq: live.seq, keepalive: true }).catch(() => {})
      }
    }
    addEventListener('pagehide', out)
    live.start()
    return () => { removeEventListener('pagehide', out); clearTimeout(save.current.timer); out(); live.close() }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  if (!editor) return <div className="doc-sheet" aria-busy="true" />
  const others = [...live.awareness.getStates()].filter(([id, s]) => id !== live.doc.clientID && s.user?.name).map(([id, s]) => ({ id, ...s.user }))
  const status = !live.synced ? 'waiting' : !live.online ? 'offline' : save.current.error ? 'error'
    : live.unacked || live.seq > live.savedSeq ? 'saving' : 'saved'
  const label = { waiting: 'Menghubungkan…', offline: 'Offline, disimpan saat tersambung lagi', error: 'Gagal menyimpan, mencoba lagi…', saving: 'Menyimpan…', saved: 'Tersimpan' }[status]

  return (
    <div className="doc-sheet doc-editing">
      <div className="doc-bar">
        <span className={`small doc-status ${status === 'offline' || status === 'error' ? 'error' : status}`} role="status">
          {status === 'saved' && <Check size={14} aria-hidden />}{status === 'offline' && <WifiOff size={14} aria-hidden />} {label}
        </span>
        <ul className="doc-people" aria-label="Sedang mengedit">
          {others.map(p => (
            <li key={p.id} style={{ '--who': p.color }} data-tip={p.name} aria-label={p.name}>{p.name.slice(0, 1).toUpperCase()}</li>
          ))}
        </ul>
        <button type="button" className="btn doc-edit" aria-pressed={src.source != null} onClick={() => (src.source == null ? src.open() : src.close(false))} disabled={!live.synced}>
          <FileCode2 size={16} aria-hidden /> {src.source != null ? 'Tutup sumber' : 'Sumber'}
        </button>
      </div>
      {src.source == null && <Toolbar editor={editor} onAsk={onAsk} onPicture={pics.pick} />}
      {src.source != null && (
        <div className="doc-source-bar">
          {others.length
            ? <span className="muted small">Sumber hanya bisa dibaca saat ada orang lain di dokumen ini.</span>
            : <button type="button" className="btn primary" onClick={() => src.close(true)}>Terapkan</button>}
        </div>
      )}
      {src.err && <p className="err small" role="alert">{src.err}</p>}
      <PictureStatus pics={pics} />
      {src.source != null && <textarea className="txt-body doc-input" value={src.source} spellCheck={false} readOnly={others.length > 0}
        aria-label={`Sumber Markdown ${file.name}`} onChange={e => src.setSource(e.target.value)} />}
      <EditorContent editor={editor} className="doc-rich-wrap" hidden={src.source != null} />
    </div>
  )
}

// ---- solo: no live connection, autosave like the plain-text editor ----

function SoloEditor({ file, initial, rev, onSaved, onAsk, autoFocus }) {
  const [front, body] = splitFrontMatter(initial)
  const frontRef = useRef(front)
  const picsRef = useRef(null)
  const editor = useEditor({
    extensions: base({ undo: true, docId: file.id }),
    content: body,
    contentType: 'markdown',
    autofocus: autoFocus ? 'end' : false,
    editorProps: editorProps(file, picsRef),
    onUpdate: () => a.changed(),
  })
  const ed = useRef(null)
  ed.current = editor
  const pics = usePictures(file, ed)
  picsRef.current = pics
  const src = useSource({ editor, frontRef })
  const sourceRef = useRef(null)
  sourceRef.current = src.source
  const read = () => (sourceRef.current != null ? sourceRef.current : frontRef.current + tidy(editor.getMarkdown()))
  const a = useAutosave({ file, rev, initial, read, onSaved })
  if (!editor) return <div className="doc-sheet" aria-busy="true" />
  return (
    <div className="doc-sheet doc-editing">
      <div className="doc-bar">
        <SaveStatus status={a.status} onRetry={() => a.save(false)} />
        <span className="muted small">Tanpa koneksi langsung: perubahan orang lain baru terlihat setelah dibuka ulang.</span>
        <button type="button" className="btn doc-edit" aria-pressed={src.source != null} onClick={() => (src.source == null ? src.open() : src.close(true))}>
          <FileCode2 size={16} aria-hidden /> {src.source != null ? 'Kembali ke editor' : 'Sumber'}
        </button>
      </div>
      <ConflictBanner status={a.status} onForce={() => a.save(true)} />
      {src.source == null && <Toolbar editor={editor} onAsk={onAsk} onPicture={pics.pick} />}
      {src.err && <p className="err small" role="alert">{src.err}</p>}
      <PictureStatus pics={pics} />
      {src.source != null && <textarea className="txt-body doc-input" value={src.source} spellCheck={false} aria-label={`Sumber Markdown ${file.name}`}
        onChange={e => { src.setSource(e.target.value); sourceRef.current = e.target.value; a.changed() }} />}
      <EditorContent editor={editor} className="doc-rich-wrap" hidden={src.source != null} />
    </div>
  )
}
