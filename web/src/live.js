// Connection to the live editing hub of one document (see app/controllers/live). Holds the Yjs
// document and the awareness (cursors); reconnects by itself, and after a reconnect sends only
// what the server is missing. The editor listens through the callbacks.
import * as Y from 'yjs'
import { Awareness, applyAwarenessUpdate, encodeAwarenessUpdate, removeAwarenessStates } from 'y-protocols/awareness'
import { api, wsURL } from './api.js'

const T = { hello: 0, seed: 1, update: 2, awareness: 3, saver: 4, compact: 5, reset: 6, ack: 7, saved: 8 }
const MAX_UPDATE = 256 * 1024
const b64 = s => Uint8Array.from(atob(s), c => c.charCodeAt(0))
const frame = (t, ...parts) => {
  const out = new Uint8Array(1 + parts.reduce((n, p) => n + p.length, 0))
  out[0] = t
  let o = 1
  for (const p of parts) { out.set(p, o); o += p.length }
  return out
}
const seqBytes = n => { const b = new Uint8Array(8); new DataView(b.buffer).setBigUint64(0, BigInt(n)); return b }
const readSeq = (b, o = 1) => Number(new DataView(b.buffer, b.byteOffset + o, 8).getBigUint64(0))

// on: { hello(first, msg), seed(), state(), reset(), fail(err) } — state() is called whenever online,
// saver, seq or savedSeq change.
export class Live {
  constructor(fileId, on) {
    this.fileId = fileId
    this.on = on
    this.doc = new Y.Doc()
    this.awareness = new Awareness(this.doc)
    this.online = false
    this.synced = false // got a hello with a seeded state at least once
    this.saver = false
    this.seq = 0 // the last update this browser has (its own, acked, or someone else's)
    this.savedSeq = 0
    this.rev = null
    this.tries = 0
    this.closed = false
    this.unacked = 0 // updates sent, not yet numbered by the server

    this.doc.on('update', (u, origin) => {
      if (origin === 'remote' || !this.synced) return
      this.unacked++
      this.send(T.update, u)
      this.on.state()
    })
    this.awareness.on('update', ({ added, updated, removed }, origin) => {
      if (origin !== 'local') return this.on.state()
      this.send(T.awareness, encodeAwarenessUpdate(this.awareness, [...added, ...updated, ...removed]))
    })
  }

  // start connects (after the editor is on the page: a hello may change it at once).
  start() {
    this.closed = false
    if (this.awareness.getLocalState() === null) this.awareness.setLocalState({}) // cleared by an earlier close
    this.connect()
  }

  send(t, ...parts) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(frame(t, ...parts))
  }

  async connect() {
    if (this.closed) return
    const gen = this.gen = (this.gen || 0) + 1 // a newer connect (or close) makes this one stale
    let t
    try {
      t = (await api(`/api/files/${this.fileId}/live/ticket`, { method: 'POST' })).ticket
    } catch (e) {
      if (e.status === 404 || e.status === 401) return this.on.fail(e)
      return this.retry()
    }
    if (this.closed || gen !== this.gen) return
    const ws = new WebSocket(wsURL(`/api/files/${this.fileId}/live?t=${encodeURIComponent(t)}`))
    ws.binaryType = 'arraybuffer'
    this.ws = ws
    ws.onmessage = e => { if (this.ws === ws) this.receive(new Uint8Array(e.data)) }
    ws.onclose = () => {
      if (this.ws !== ws) return
      this.online = false
      this.saver = false
      // Others' cursors are stale now; they come back with the next hello.
      removeAwarenessStates(this.awareness, [...this.awareness.getStates().keys()].filter(id => id !== this.doc.clientID), 'remote')
      this.on.state()
      this.retry()
    }
  }

  retry() {
    if (this.closed) return
    this.tries++
    if (this.tries === 4 && !this.synced) return this.on.fail(new Error('no connection'))
    setTimeout(() => this.connect(), Math.min(10000, 1000 * this.tries))
  }

  receive(b) {
    const body = b.subarray(1)
    switch (b[0]) {
      case T.hello: return this.hello(JSON.parse(new TextDecoder().decode(body)))
      case T.update:
        this.seq = Math.max(this.seq, readSeq(b))
        Y.applyUpdate(this.doc, b.subarray(9), 'remote')
        return this.on.state()
      case T.ack:
        this.seq = Math.max(this.seq, readSeq(b))
        this.unacked = Math.max(0, this.unacked - 1)
        return this.on.state()
      case T.awareness: return applyAwarenessUpdate(this.awareness, body, 'remote')
      case T.saver:
        this.saver = JSON.parse(new TextDecoder().decode(body)).saver
        return this.on.state()
      case T.saved: {
        const s = JSON.parse(new TextDecoder().decode(body))
        this.rev = s.rev
        this.savedSeq = Math.max(this.savedSeq, s.seq)
        return this.on.state()
      }
      case T.reset:
        this.close()
        return this.on.reset()
    }
  }

  hello(m) {
    this.online = true
    this.tries = 0
    this.saver = m.saver
    this.rev = m.rev
    this.savedSeq = m.saved_seq
    if (m.seed) { // nobody has it open: build it from the saved file and hand it to the server
      this.on.seed()
      this.synced = true
      this.send(T.seed, Y.encodeStateAsUpdate(this.doc))
      this.on.hello?.(true, m)
      return this.on.state()
    }
    if (!m.state) return this.on.state() // someone else is seeding; their hello comes next
    // What the server has, applied here; then whatever only this browser has goes to it.
    const server = new Y.Doc()
    for (const u of [m.state, ...m.updates]) {
      const x = b64(u)
      Y.applyUpdate(server, x)
      Y.applyUpdate(this.doc, x, 'remote')
    }
    this.seq = Math.max(this.seq, m.seq)
    const mine = Y.encodeStateAsUpdate(this.doc, Y.encodeStateVector(server))
    server.destroy()
    const first = !this.synced
    this.synced = true
    this.unacked = 0
    if (mine.length > 2 && mine.length <= MAX_UPDATE) { this.unacked = 1; this.send(T.update, mine) }
    this.send(T.awareness, encodeAwarenessUpdate(this.awareness, [this.doc.clientID]))
    this.on.hello?.(first, m)
    this.on.state()
  }

  // After the saver saved up to seq: fold everything into one state on the server.
  compact(seq) {
    this.send(T.compact, seqBytes(seq), Y.encodeStateAsUpdate(this.doc))
  }

  close() {
    this.closed = true
    this.gen = (this.gen || 0) + 1
    removeAwarenessStates(this.awareness, [this.doc.clientID], 'local')
    const ws = this.ws
    this.ws = null
    this.online = false
    this.synced = false
    setTimeout(() => ws?.close(), 50) // let the awareness removal go out first
  }
}
