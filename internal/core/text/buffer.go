package text

import (
	"bytes"
	"fmt"
	"io"
	"unicode/utf8"
)

const cacheSlop = 100

// Buffer is the disk-backed rune store used by sam's core editing engine.
type Buffer struct {
	disk *Disk

	nc     int
	cache  []rune
	cq     int
	cdirty bool
	cbi    int
	blocks []*block
}

// NewBuffer constructs an empty buffer backed by d.
func NewBuffer(d *Disk) *Buffer {
	return &Buffer{disk: d}
}

// Len reports the number of runes stored in the buffer.
func (b *Buffer) Len() int {
	return b.nc
}

// Insert inserts s at q0.
func (b *Buffer) Insert(q0 Posn, s []rune) error {
	pos := int(q0)
	if pos < 0 || pos > b.nc {
		return fmt.Errorf("bufinsert out of range")
	}

	n := len(s)
	for n > 0 {
		var m int
		if err := b.setCache(pos); err != nil {
			return err
		}
		off := pos - b.cq
		cacheLen := len(b.cache)

		if cacheLen+n <= MaxBlock {
			t := cacheLen + n
			m = n
			if b.blocks == nil {
				if cacheLen != 0 {
					return fmt.Errorf("bufinsert inconsistent cache")
				}
				if err := b.addBlock(0, t); err != nil {
					return err
				}
				b.cbi = 0
			}
			b.sizeCache(t)
			oldLen := cacheLen
			b.cache = b.cache[:t]
			copy(b.cache[off+m:], b.cache[off:oldLen])
			copy(b.cache[off:], s[:m])
			goto tail
		}

		if pos == b.cq || pos == b.cq+cacheLen {
			if b.cdirty {
				if err := b.flush(); err != nil {
					return err
				}
			}
			m = minInt(n, MaxBlock)
			i := 0
			if b.blocks != nil {
				i = b.cbi
				if pos > b.cq {
					i++
				}
			}
			if err := b.addBlock(i, m); err != nil {
				return err
			}
			b.sizeCache(m)
			b.cache = b.cache[:m]
			copy(b.cache, s[:m])
			b.cq = pos
			b.cbi = i
			goto tail
		}

		m = cacheLen - off
		if m > 0 {
			i := b.cbi + 1
			if err := b.addBlock(i, m); err != nil {
				return err
			}
			if err := b.disk.write(&b.blocks[i], b.cache[off:cacheLen], m); err != nil {
				return err
			}
			b.cache = b.cache[:cacheLen-m]
			cacheLen = len(b.cache)
		}

		m = minInt(n, MaxBlock-cacheLen)
		b.sizeCache(cacheLen + m)
		b.cache = b.cache[:cacheLen+m]
		copy(b.cache[cacheLen:], s[:m])

	tail:
		b.nc += m
		pos += m
		s = s[m:]
		n -= m
		b.cdirty = true
	}
	return nil
}

// Delete removes the half-open range [q0, q1).
func (b *Buffer) Delete(q0, q1 Posn) error {
	p0 := int(q0)
	p1 := int(q1)
	if !(0 <= p0 && p0 <= p1 && p1 <= b.nc) {
		return fmt.Errorf("bufdelete out of range")
	}

	for p1 > p0 {
		if err := b.setCache(p0); err != nil {
			return err
		}
		off := p0 - b.cq
		n := p1 - p0
		if limit := b.cq + len(b.cache); p1 > limit {
			n = len(b.cache) - off
		}
		m := len(b.cache) - (off + n)
		if m > 0 {
			copy(b.cache[off:], b.cache[off+n:off+n+m])
		}
		b.cache = b.cache[:len(b.cache)-n]
		b.cdirty = true
		p1 -= n
		b.nc -= n
	}
	return nil
}

// Read copies len(dst) runes from q0 into dst.
func (b *Buffer) Read(q0 Posn, dst []rune) error {
	pos := int(q0)
	n := len(dst)
	if !(0 <= pos && pos <= b.nc && pos+n <= b.nc) {
		return fmt.Errorf("bufread out of range")
	}

	for n > 0 {
		if err := b.setCache(pos); err != nil {
			return err
		}
		m := minInt(n, len(b.cache)-(pos-b.cq))
		copy(dst[:m], b.cache[pos-b.cq:pos-b.cq+m])
		pos += m
		dst = dst[m:]
		n -= m
	}
	return nil
}

// RuneAt reads one rune from q.
func (b *Buffer) RuneAt(q Posn) (rune, error) {
	if q < 0 || int(q) >= b.nc {
		return 0, fmt.Errorf("rune index out of range")
	}
	var dst [1]rune
	if err := b.Read(q, dst[:]); err != nil {
		return 0, err
	}
	return dst[0], nil
}

// Reset discards all buffer contents.
func (b *Buffer) Reset() error {
	b.nc = 0
	b.cache = b.cache[:0]
	b.cq = 0
	b.cdirty = false
	b.cbi = 0
	for i := len(b.blocks) - 1; i >= 0; i-- {
		if err := b.delBlock(i); err != nil {
			return err
		}
	}
	return nil
}

// Close discards all buffer contents and releases in-memory state.
func (b *Buffer) Close() error {
	if err := b.Reset(); err != nil {
		return err
	}
	b.cache = nil
	b.blocks = nil
	return nil
}

// Load decodes UTF-8 from r and inserts it at q0, dropping NULs as sam does.
func (b *Buffer) Load(q0 Posn, r io.Reader) (loaded int, sawNulls bool, err error) {
	pos := int(q0)
	if pos < 0 || pos > b.nc {
		return 0, false, fmt.Errorf("bufload out of range")
	}

	buf := make([]byte, 0, MaxBlock+utf8.UTFMax+1)
	tmp := make([]byte, MaxBlock)
	for {
		n, readErr := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			convertLimit := len(buf)
			if readErr == nil && convertLimit > utf8.UTFMax {
				convertLimit -= utf8.UTFMax
			}
			consumed, runes, nulls := cvtToRunes(buf, convertLimit)
			if nulls {
				sawNulls = true
			}
			if len(runes) > 0 {
				if err := b.Insert(Posn(pos), runes); err != nil {
					return loaded, sawNulls, err
				}
				pos += len(runes)
				loaded += len(runes)
			}
			buf = append([]byte(nil), buf[consumed:]...)
		}

		if readErr == io.EOF {
			if len(buf) > 0 {
				_, runes, nulls := cvtToRunes(buf, len(buf))
				if nulls {
					sawNulls = true
				}
				if len(runes) > 0 {
					if err := b.Insert(Posn(pos), runes); err != nil {
						return loaded, sawNulls, err
					}
					loaded += len(runes)
				}
			}
			return loaded, sawNulls, nil
		}
		if readErr != nil {
			return loaded, sawNulls, readErr
		}
	}
}

func (b *Buffer) sizeCache(n int) {
	if cap(b.cache) >= n {
		return
	}
	next := make([]rune, len(b.cache), n+cacheSlop)
	copy(next, b.cache)
	b.cache = next
}

func (b *Buffer) addBlock(i, n int) error {
	if i < 0 || i > len(b.blocks) {
		return fmt.Errorf("addblock out of range")
	}
	block, err := b.disk.newBlock(n)
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, nil)
	copy(b.blocks[i+1:], b.blocks[i:])
	b.blocks[i] = block
	return nil
}

func (b *Buffer) delBlock(i int) error {
	if i < 0 || i >= len(b.blocks) {
		return fmt.Errorf("delblock out of range")
	}
	if err := b.disk.release(b.blocks[i]); err != nil {
		return err
	}
	copy(b.blocks[i:], b.blocks[i+1:])
	b.blocks[len(b.blocks)-1] = nil
	b.blocks = b.blocks[:len(b.blocks)-1]
	return nil
}

func (b *Buffer) flush() error {
	if b.cdirty || len(b.cache) == 0 {
		if len(b.cache) == 0 {
			if err := b.delBlock(b.cbi); err != nil {
				return err
			}
		} else {
			if err := b.disk.write(&b.blocks[b.cbi], b.cache, len(b.cache)); err != nil {
				return err
			}
		}
		b.cdirty = false
	}
	return nil
}

func (b *Buffer) setCache(q0 int) error {
	if q0 > b.nc {
		return fmt.Errorf("setcache out of range")
	}
	if b.nc == 0 || (b.cq <= q0 && q0 < b.cq+len(b.cache)) {
		return nil
	}
	if q0 == b.nc && q0 == b.cq+len(b.cache) && len(b.cache) <= MaxBlock {
		return nil
	}
	if len(b.blocks) == 0 {
		return nil
	}
	if err := b.flush(); err != nil {
		return err
	}

	var q int
	var i int
	if q0 < b.cq {
		q = 0
		i = 0
	} else {
		q = b.cq
		i = b.cbi
	}

	for {
		if i >= len(b.blocks) {
			return fmt.Errorf("block not found")
		}
		bl := b.blocks[i]
		if q+bl.n > q0 || q+bl.n >= b.nc {
			b.cbi = i
			b.cq = q
			b.sizeCache(bl.n)
			b.cache = b.cache[:bl.n]
			return b.disk.read(bl, b.cache, bl.n)
		}
		q += bl.n
		i++
	}
}

func cvtToRunes(p []byte, n int) (consumed int, out []rune, sawNull bool) {
	if n > len(p) {
		n = len(p)
	}
	q := p[:n]
	out = make([]rune, 0, len(q))
	for len(q) > 0 {
		r, size := utf8.DecodeRune(q)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(q) {
			break
		}
		q = q[size:]
		consumed += size
		if r == 0 {
			sawNull = true
			continue
		}
		out = append(out, r)
	}
	return consumed, out, sawNull
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// String returns the whole buffer as a Go string for tests and debugging.
func (b *Buffer) String() string {
	if b.nc == 0 {
		return ""
	}
	all := make([]rune, b.nc)
	if err := b.Read(0, all); err != nil {
		return ""
	}
	return string(all)
}

// Bytes returns the whole buffer encoded as UTF-8 for tests and debugging.
func (b *Buffer) Bytes() []byte {
	return bytes.Clone([]byte(b.String()))
}
