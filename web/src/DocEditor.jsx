// The rich Markdown editor (TipTap): edits a document as it looks, saves it back as Markdown.
// "Sumber" switches to the raw Markdown; switching back reads it into the editor again.
import { useRef, useState } from 'react'
import { EditorContent, useEditor, useEditorState } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import { Markdown } from '@tiptap/markdown'
import { TaskItem, TaskList } from '@tiptap/extension-list'
import { TableKit } from '@tiptap/extension-table'
import { Placeholder } from '@tiptap/extensions'
import {
  Bold, Code, FileCode2, Italic, Link2, List, ListOrdered, ListTodo, Minus, Quote, Redo2, SquareCode,
  Strikethrough, Table, Undo2,
} from 'lucide-react'
import { ConflictBanner, SaveStatus, useAutosave } from './autosave.jsx'
import { richSupported, splitFrontMatter, tidy } from './markdown.js'
import { Dropdown } from './ui.jsx'

const extensions = [
  StarterKit.configure({
    underline: false, // Markdown has no underline
    link: { openOnClick: false, autolink: true, defaultProtocol: 'https' },
  }),
  TaskList,
  TaskItem.configure({ nested: true }),
  TableKit.configure({ table: { resizable: false } }),
  Placeholder.configure({ placeholder: 'Mulai menulis…' }),
  Markdown,
]

const STYLES = [
  { value: 'p', label: 'Teks biasa' },
  { value: 1, label: 'Judul 1' },
  { value: 2, label: 'Judul 2' },
  { value: 3, label: 'Judul 3' },
]

// A toolbar button.
const B = ({ on, label, icon: I, onClick, disabled }) => (
  <button type="button" className="icon-btn" aria-label={label} data-tip={label} aria-pressed={on} onClick={onClick} disabled={disabled}>
    <I size={18} />
  </button>
)

export default function DocEditor({ file, initial, rev, onSaved, onAsk, autoFocus }) {
  const [front, body] = splitFrontMatter(initial)
  const frontRef = useRef(front)
  const [source, setSource] = useState(null) // raw Markdown while "Sumber" is on
  const sourceRef = useRef(null)
  const [srcErr, setSrcErr] = useState('')

  const editor = useEditor({
    extensions,
    content: body,
    contentType: 'markdown',
    autofocus: autoFocus ? 'end' : false,
    editorProps: { attributes: { class: 'md-body doc-rich', 'aria-label': `Isi ${file.name}`, spellcheck: 'true' } },
    onUpdate: () => a.changed(),
  })
  const read = () => (sourceRef.current != null ? sourceRef.current : frontRef.current + tidy(editor.getMarkdown()))
  const a = useAutosave({ file, rev, initial, read, onSaved })

  const st = useEditorState({
    editor,
    selector: ({ editor: e }) => e && {
      heading: [1, 2, 3].find(l => e.isActive('heading', { level: l })) ?? 'p',
      bold: e.isActive('bold'), italic: e.isActive('italic'), strike: e.isActive('strike'), code: e.isActive('code'),
      link: e.isActive('link'), bullet: e.isActive('bulletList'), ordered: e.isActive('orderedList'), task: e.isActive('taskList'),
      quote: e.isActive('blockquote'), codeBlock: e.isActive('codeBlock'), table: e.isActive('table'),
      undo: e.can().undo(), redo: e.can().redo(),
    },
  })
  if (!editor || !st) return <div className="doc-sheet" aria-busy="true" />

  const run = f => () => f(editor.chain().focus()).run()
  const setLink = async () => {
    const prev = editor.getAttributes('link').href
    const url = await onAsk({ title: prev ? 'Ubah link' : 'Tambah link', input: true, value: prev || 'https://', confirm: 'Simpan',
      message: 'Kosongkan untuk menghapus link.' })
    if (url == null) return
    const c = editor.chain().focus().extendMarkRange('link')
    ;(url.trim() && url.trim() !== 'https://' ? c.setLink({ href: url.trim() }) : c.unsetLink()).run()
  }
  const toggleSource = () => {
    setSrcErr('')
    if (source == null) {
      const md = frontRef.current + tidy(editor.getMarkdown())
      sourceRef.current = md
      setSource(md)
      return
    }
    if (!richSupported(source)) return setSrcErr('Teks ini memakai Markdown yang tidak bisa ditampilkan editor. Tetap di mode Sumber, atau hapus bagian itu dulu.')
    const [f, b] = splitFrontMatter(source)
    frontRef.current = f
    editor.commands.setContent(b, { contentType: 'markdown', emitUpdate: false })
    sourceRef.current = null
    setSource(null)
  }
  return (
    <div className="doc-sheet doc-editing">
      <div className="doc-bar">
        <SaveStatus status={a.status} onRetry={() => a.save(false)} />
        <button type="button" className="btn doc-edit" aria-pressed={source != null} onClick={toggleSource}>
          <FileCode2 size={16} aria-hidden /> {source != null ? 'Kembali ke editor' : 'Sumber'}
        </button>
      </div>
      <ConflictBanner status={a.status} onForce={() => a.save(true)} />
      {source == null && (
        <div className="doc-tools" role="toolbar" aria-label="Format teks">
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
        </div>
      )}
      {source == null && st.table && (
        <div className="doc-tools doc-table-tools" role="toolbar" aria-label="Tabel">
          <button type="button" className="btn" onClick={run(c => c.addRowAfter())}>+ Baris</button>
          <button type="button" className="btn" onClick={run(c => c.addColumnAfter())}>+ Kolom</button>
          <button type="button" className="btn" onClick={run(c => c.deleteRow())}>Hapus baris</button>
          <button type="button" className="btn" onClick={run(c => c.deleteColumn())}>Hapus kolom</button>
          <button type="button" className="btn" onClick={run(c => c.deleteTable())}>Hapus tabel</button>
        </div>
      )}
      {srcErr && <p className="err small" role="alert">{srcErr}</p>}
      {source != null
        ? <textarea className="txt-body doc-input" value={source} spellCheck={false} aria-label={`Sumber Markdown ${file.name}`}
            onChange={e => { setSource(e.target.value); sourceRef.current = e.target.value; a.changed() }} />
        : <EditorContent editor={editor} className="doc-rich-wrap"
            onKeyDown={e => { if ((e.ctrlKey || e.metaKey) && e.key === 'k') { e.preventDefault(); setLink() } }} />}
    </div>
  )
}
