package vinyl

import (
	"io"
	"slices"
	"sync"
)

// orderedKeys returns the keys of m in the given order, skipping keys m lacks, followed by the remaining keys of
// m in sorted order.
func orderedKeys(m map[string]any, order []string) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	seen := make(map[string]bool, len(m))
	for _, k := range order {
		if _, ok := m[k]; ok && !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	return append(keys, rest...)
}

// deepCopy copies the JSON-like values a source map holds (maps, slices and scalars).
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopy(e)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	default:
		return v
	}
}

// teeSource buffers what it reads from src so that several readers can each see every byte, the way teex splits
// a stream for file.clone().
type teeSource struct {
	mu   sync.Mutex
	src  io.Reader
	buf  []byte
	err  error
	done bool
}

// readAt copies buffered bytes from offset off, pulling more from the source when needed.
func (t *teeSource) readAt(p []byte, off int) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for off >= len(t.buf) && !t.done {
		chunk := make([]byte, 32*1024)
		n, err := t.src.Read(chunk)
		t.buf = append(t.buf, chunk[:n]...)
		if err != nil {
			t.done = true
			if err != io.EOF {
				t.err = err
			}
		}
	}
	if off < len(t.buf) {
		return copy(p, t.buf[off:]), nil
	}
	if t.err != nil {
		return 0, t.err
	}
	return 0, io.EOF
}

// teeReader is one branch of a teeSource.
type teeReader struct {
	src *teeSource
	off int
}

// Read implements io.Reader.
func (r *teeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := r.src.readAt(p, r.off)
	r.off += n
	return n, err
}

// tee splits r into two readers that each yield all of r's bytes.
func tee(r io.Reader) (io.Reader, io.Reader) {
	src := &teeSource{src: r}
	return &teeReader{src: src}, &teeReader{src: src}
}
