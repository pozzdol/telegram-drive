// Video and audio player with the app's own controls (the browser's built-in controls look
// different on every browser and cannot be styled).
import { useEffect, useRef, useState } from 'react'
import { Maximize, Minimize, Pause, Play, Volume2, VolumeX } from 'lucide-react'

const clock = s => {
  if (!Number.isFinite(s)) return '0:00'
  s = Math.floor(s)
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = String(s % 60).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`
}

export default function Player({ src, poster, audio, label, onError }) {
  const box = useRef(null)
  const media = useRef(null)
  const bar = useRef(null)
  const [playing, setPlaying] = useState(false)
  const [time, setTime] = useState(0)
  const [dur, setDur] = useState(0)
  const [buffered, setBuffered] = useState(0)
  const [muted, setMuted] = useState(false)
  const [full, setFull] = useState(false)
  const [idle, setIdle] = useState(false) // video: hide controls while playing untouched
  const idleTimer = useRef(null)

  const toggle = () => { const m = media.current; m.paused ? m.play().catch(() => {}) : m.pause() }
  const wake = () => {
    setIdle(false)
    clearTimeout(idleTimer.current)
    idleTimer.current = setTimeout(() => setIdle(true), 2500)
  }
  useEffect(() => () => clearTimeout(idleTimer.current), [])
  useEffect(() => {
    const f = () => setFull(document.fullscreenElement === box.current)
    document.addEventListener('fullscreenchange', f)
    return () => document.removeEventListener('fullscreenchange', f)
  }, [])

  // Seek by pointer: click or drag along the bar.
  const seekTo = e => {
    const r = bar.current.getBoundingClientRect()
    const x = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width))
    if (dur) media.current.currentTime = x * dur
  }
  const seekKeys = e => {
    const step = { ArrowLeft: -5, ArrowRight: 5, Home: -Infinity, End: Infinity }[e.key]
    if (step === undefined) return
    e.preventDefault()
    e.stopPropagation() // keep the viewer from switching files
    media.current.currentTime = Math.min(dur, Math.max(0, media.current.currentTime + step))
  }

  const Tag = audio ? 'audio' : 'video'
  const pct = dur ? (time / dur) * 100 : 0
  return (
    <div ref={box} className={`player${audio ? ' audio' : ''}${playing && idle && !audio ? ' idle' : ''}`}
      onPointerMove={wake} onPointerDown={wake}>
      <Tag ref={media} className="player-media" src={src} poster={poster} autoPlay playsInline preload="metadata"
        onClick={toggle} onPlay={() => { setPlaying(true); wake() }} onPause={() => { setPlaying(false); setIdle(false) }}
        onTimeUpdate={e => setTime(e.currentTarget.currentTime)}
        onDurationChange={e => setDur(e.currentTarget.duration)}
        onProgress={e => { const b = e.currentTarget.buffered; if (b.length) setBuffered(b.end(b.length - 1)) }}
        onVolumeChange={e => setMuted(e.currentTarget.muted)}
        onError={onError} />
      {audio && <p className="player-title">{label}</p>}
      <div className="player-bar">
        <button className="icon-btn" onClick={toggle} aria-label={playing ? 'Jeda' : 'Putar'}>
          {playing ? <Pause size={20} fill="currentColor" /> : <Play size={20} fill="currentColor" />}
        </button>
        <span className="player-time">{clock(time)}</span>
        <div ref={bar} className="player-seek" role="slider" tabIndex={0} aria-label="Posisi"
          aria-valuemin={0} aria-valuemax={Math.round(dur)} aria-valuenow={Math.round(time)} aria-valuetext={`${clock(time)} dari ${clock(dur)}`}
          onKeyDown={seekKeys}
          onPointerDown={e => { e.currentTarget.setPointerCapture(e.pointerId); seekTo(e) }}
          onPointerMove={e => { if (e.currentTarget.hasPointerCapture(e.pointerId)) seekTo(e) }}>
          <span className="buffered" style={{ width: `${dur ? (buffered / dur) * 100 : 0}%` }} />
          <span className="played" style={{ width: `${pct}%` }} />
          <span className="knob" style={{ left: `${pct}%` }} />
        </div>
        <span className="player-time">{clock(dur)}</span>
        <button className="icon-btn" onClick={() => { media.current.muted = !media.current.muted }} aria-label={muted ? 'Bunyikan' : 'Bisukan'}>
          {muted ? <VolumeX size={20} /> : <Volume2 size={20} />}
        </button>
        {!audio && (
          <button className="icon-btn" aria-label={full ? 'Keluar layar penuh' : 'Layar penuh'}
            onClick={() => (full ? document.exitFullscreen() : box.current.requestFullscreen?.())}>
            {full ? <Minimize size={20} /> : <Maximize size={20} />}
          </button>
        )}
      </div>
    </div>
  )
}
