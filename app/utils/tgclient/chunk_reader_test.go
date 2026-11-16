package tgclient

import (
	"bytes"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Range requests over chunkReader must return the exact bytes and only ask Telegram for aligned chunks,
// both one chunk at a time and with read-ahead.
func TestChunkReaderRange(t *testing.T) {
	data := make([]byte, 5*chunkSize+12345)
	for i := range data {
		data[i] = byte(rand.IntN(256))
	}
	for _, parallel := range []int{0, 4} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "x.bin", time.Time{}, &chunkReader{size: int64(len(data)), parallel: parallel,
				fetch: func(off int64) ([]byte, error) {
					if off%chunkSize != 0 {
						t.Errorf("unaligned fetch offset %d", off)
					}
					return data[off:min(off+chunkSize, int64(len(data)))], nil
				}})
		}))
		get := func(rng string) (int, []byte) {
			req, _ := http.NewRequest("GET", srv.URL, nil)
			if rng != "" {
				req.Header.Set("Range", rng)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			return res.StatusCode, b
		}
		// A range straddling a chunk boundary, the whole file, and the tail.
		if code, b := get("bytes=1048570-1048590"); code != 206 || !bytes.Equal(b, data[1048570:1048591]) {
			t.Fatalf("parallel=%d range: code %d, got %d bytes", parallel, code, len(b))
		}
		if code, b := get(""); code != 200 || !bytes.Equal(b, data) {
			t.Fatalf("parallel=%d full: code %d, got %d of %d bytes", parallel, code, len(b), len(data))
		}
		if code, b := get("bytes=-100"); code != 206 || !bytes.Equal(b, data[len(data)-100:]) {
			t.Fatalf("parallel=%d suffix range: code %d", parallel, code)
		}
		srv.Close()
	}
}

// Read-ahead must overlap fetches: 6 chunks at 50 ms each take ~300 ms one by one, ~100 ms with 4 in flight.
func TestChunkReaderParallel(t *testing.T) {
	size := int64(6 * chunkSize)
	r := &chunkReader{size: size, parallel: 4, fetch: func(off int64) ([]byte, error) {
		time.Sleep(50 * time.Millisecond)
		return make([]byte, min(chunkSize, size-off)), nil
	}}
	start := time.Now()
	n, err := io.Copy(io.Discard, r)
	if err != nil || n != size {
		t.Fatalf("read %d bytes, err %v", n, err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("read-ahead not parallel: took %v", d)
	}
}
