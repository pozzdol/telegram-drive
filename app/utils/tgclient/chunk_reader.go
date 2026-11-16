package tgclient

import (
	"errors"
	"io"
)

// chunkReader is an io.ReadSeeker that downloads 1 MiB-aligned chunks on demand,
// so http.ServeContent can answer Range requests (video seek, resumed downloads).
// It keeps `parallel` chunks in flight ahead of the read position: one chunk per
// round trip is latency-bound (~3 MB/s), several at once fill the pipe.
type chunkReader struct {
	fetch     func(off int64) ([]byte, error)
	size, pos int64
	parallel  int // chunks in flight, including the one being read; 0 means 1
	pending   map[int64]chan chunkResult
	bufOff    int64
	buf       []byte
}

type chunkResult struct {
	b   []byte
	err error
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	off := r.pos - r.pos%chunkSize
	if r.buf == nil || r.bufOff != off {
		res := <-r.prefetch(off)
		if res.err != nil {
			return 0, res.err
		}
		r.buf, r.bufOff = res.b, off
	}
	i := r.pos - r.bufOff
	if i >= int64(len(r.buf)) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.buf[i:])
	r.pos += int64(n)
	return n, nil
}

// prefetch starts fetches for chunks off .. off+parallel-1 that are not in flight yet,
// forgets chunks outside that window (after a seek), and hands back off's result channel.
// Forgotten fetches finish into their buffered channel and are dropped; the request
// context cancels them when the client goes away.
func (r *chunkReader) prefetch(off int64) chan chunkResult {
	if r.pending == nil {
		r.pending = map[int64]chan chunkResult{}
	}
	last := off + int64(max(r.parallel, 1)-1)*chunkSize
	for k := range r.pending {
		if k < off || k > last {
			delete(r.pending, k)
		}
	}
	for o := off; o <= last && o < r.size; o += chunkSize {
		if _, ok := r.pending[o]; !ok {
			ch := make(chan chunkResult, 1)
			r.pending[o] = ch
			go func() {
				b, err := r.fetch(o)
				ch <- chunkResult{b, err}
			}()
		}
	}
	ch := r.pending[off]
	delete(r.pending, off)
	return ch
}

func (r *chunkReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.pos
	case io.SeekEnd:
		offset += r.size
	default:
		return 0, errors.New("seek: invalid whence")
	}
	if offset < 0 {
		return 0, errors.New("seek: negative position")
	}
	r.pos = offset
	return offset, nil
}
