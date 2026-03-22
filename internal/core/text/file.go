package text

import (
	"fmt"
	"io"
)

const (
	rbufSize = MaxBlock / 4
	maxMerge = 50
	undoSize = 5
)

const (
	undoDelete   rune = 'd'
	undoInsert   rune = 'i'
	undoFilename rune = 'f'
	undoDot      rune = 'D'
	undoMark     rune = 'm'
)

type undo struct {
	Type rune
	Mod  bool
	Seq  uint32
	P0   Posn
	N    Posn
}

type mergeState struct {
	seq  uint32
	p0   Posn
	n    Posn
	nbuf int
	buf  []rune
}

// File is the core mutable file state used by the editing engine.
type File struct {
	B       *Buffer
	Delta   *Buffer
	Epsilon *Buffer

	Name      String
	Unread    bool
	Mod       bool
	Rescuing  bool
	Seq       uint32
	CleanSeq  uint32
	HiPosn    Posn
	Dot       Range
	NDot      Range
	Mark      Range
	PrevDot   Range
	PrevMark  Range
	PrevSeq   uint32
	PrevMod   bool
	InitLine  Posn
	InitCol   Posn
	mergeOpen bool
	merge     mergeState
}

// NewFile constructs an empty file backed by the provided disk.
func NewFile(d *Disk) *File {
	return &File{
		B:       NewBuffer(d),
		Delta:   NewBuffer(d),
		Epsilon: NewBuffer(d),
		Name:    NewString0(),
		Unread:  true,
	}
}

// Close releases file-owned buffers.
func (f *File) Close() error {
	var firstErr error
	if err := f.B.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := f.Delta.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := f.Epsilon.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	f.Name.Close()
	return firstErr
}

// IsDirty reports whether the file differs from its last clean sequence.
func (f *File) IsDirty() bool {
	return f.Seq != f.CleanSeq
}

// MarkClean records the current sequence as clean and clears the mod bit.
func (f *File) MarkClean() {
	f.CleanSeq = f.Seq
	f.Mod = false
}

// ReadRune reads a single rune at q, returning -1 if out of range.
func (f *File) ReadRune(q Posn) (rune, error) {
	if q < 0 || int(q) >= f.B.Len() {
		return -1, nil
	}
	return f.B.RuneAt(q)
}

// Load reads UTF-8 text into the underlying buffer. It is intended for the raw
// file-load path and matches sam's restriction that fileload is only valid when
// no undo transcript is active.
func (f *File) Load(p0 Posn, r io.Reader) (loaded Posn, sawNulls bool, err error) {
	if f.Seq > 0 {
		return 0, false, fmt.Errorf("undo in file load unimplemented")
	}
	n, sawNulls, err := f.B.Load(p0, r)
	return Posn(n), sawNulls, err
}

// LoadInitial replaces unread state with file contents loaded from r.
func (f *File) LoadInitial(r io.Reader) (loaded Posn, sawNulls bool, err error) {
	n, sawNulls, err := f.Load(0, r)
	if err != nil {
		return 0, false, err
	}
	f.Unread = false
	if !sawNulls {
		f.MarkClean()
	}
	return n, sawNulls, nil
}

// WriteRangeTo writes the half-open range [p0, p1) as UTF-8.
func (f *File) WriteRangeTo(w io.Writer, p0, p1 Posn) (written Posn, err error) {
	if !(0 <= p0 && p0 <= p1 && int(p1) <= f.B.Len()) {
		return 0, fmt.Errorf("write range out of bounds")
	}
	for p0 < p1 {
		n := p1 - p0
		if n > MaxBlock {
			n = MaxBlock
		}
		buf := make([]rune, n)
		if err := f.B.Read(p0, buf); err != nil {
			return written, err
		}
		m, err := io.WriteString(w, string(buf))
		if err != nil {
			return written, err
		}
		written += Posn(m)
		p0 += n
	}
	return written, nil
}

// WriteTo writes the entire buffer as UTF-8.
func (f *File) WriteTo(w io.Writer) (written Posn, err error) {
	return f.WriteRangeTo(w, 0, Posn(f.B.Len()))
}

// SetName applies the file name directly.
func (f *File) SetName(s *String) error {
	if !f.Unread {
		if err := f.unsetName(f.Delta); err != nil {
			return err
		}
	}
	if err := f.Name.DupString(s); err != nil {
		return err
	}
	f.Unread = true
	return nil
}

// LogSetName appends a filename change to epsilon.
func (f *File) LogSetName(s *String, currentSeq uint32) error {
	if f.Rescuing {
		return nil
	}
	if f.Unread {
		return f.SetName(s)
	}
	if f.Seq < currentSeq {
		if err := f.MarkState(currentSeq); err != nil {
			return err
		}
	}
	u := undo{
		Type: undoFilename,
		Mod:  true,
		Seq:  f.Seq,
		P0:   0,
		N:    Posn(s.Len()),
	}
	if s.Len() > 0 {
		if err := f.Epsilon.Insert(Posn(f.Epsilon.Len()), s.Runes()); err != nil {
			return err
		}
	}
	if err := f.Epsilon.Insert(Posn(f.Epsilon.Len()), encodeUndo(u)); err != nil {
		return err
	}
	if !f.Unread && !f.Mod {
		f.Mod = true
	}
	return nil
}

// LogInsert appends an insertion to epsilon.
func (f *File) LogInsert(p0 Posn, s []rune, currentSeq uint32) error {
	if f.Rescuing || len(s) == 0 {
		return nil
	}
	if len(s) > MaxStringRunes {
		return fmt.Errorf("loginsert too large")
	}
	if f.Seq < currentSeq {
		if err := f.MarkState(currentSeq); err != nil {
			return err
		}
	}
	if p0 < f.HiPosn {
		return fmt.Errorf("sequence error")
	}

	if f.mergeOpen &&
		(p0-(f.merge.p0+f.merge.n) > maxMerge ||
			f.merge.nbuf+int((p0+Posn(len(s)))-(f.merge.p0+f.merge.n)) >= rbufSize) {
		if err := f.FlushMerge(); err != nil {
			return err
		}
	}

	if len(s) >= rbufSize {
		if f.mergeOpen && (f.merge.n != 0 || f.merge.nbuf != 0) {
			return fmt.Errorf("bad merge state")
		}
		if err := wrInsert(f.Epsilon, f.Seq, true, p0, s); err != nil {
			return err
		}
	} else {
		if !f.mergeOpen {
			f.mergeOpen = true
			f.merge.seq = f.Seq
			f.merge.p0 = p0
			f.merge.n = 0
			f.merge.nbuf = 0
			if cap(f.merge.buf) == 0 {
				f.merge.buf = make([]rune, rbufSize)
			}
		}
		if err := f.mergeExtend(p0); err != nil {
			return err
		}
		copy(f.merge.buf[f.merge.nbuf:], s)
		f.merge.nbuf += len(s)
	}

	f.HiPosn = p0
	if !f.Unread && !f.Mod {
		f.Mod = true
	}
	return nil
}

// LogDelete appends a deletion to epsilon.
func (f *File) LogDelete(p0, p1 Posn, currentSeq uint32) error {
	if f.Rescuing || p0 == p1 {
		return nil
	}
	if f.Seq < currentSeq {
		if err := f.MarkState(currentSeq); err != nil {
			return err
		}
	}
	if p0 < f.HiPosn {
		return fmt.Errorf("sequence error")
	}

	if !f.mergeOpen ||
		p0-(f.merge.p0+f.merge.n) > maxMerge ||
		f.merge.nbuf+int(p0-(f.merge.p0+f.merge.n)) >= rbufSize {
		if err := f.FlushMerge(); err != nil {
			return err
		}
		f.mergeOpen = true
		f.merge.seq = f.Seq
		f.merge.p0 = p0
		f.merge.n = 0
		f.merge.nbuf = 0
		if cap(f.merge.buf) == 0 {
			f.merge.buf = make([]rune, rbufSize)
		}
	}

	if err := f.mergeExtend(p0); err != nil {
		return err
	}
	f.merge.n = p1 - f.merge.p0

	f.HiPosn = p1
	if !f.Unread && !f.Mod {
		f.Mod = true
	}
	return nil
}

// FlushMerge writes any pending merge state into epsilon.
func (f *File) FlushMerge() error {
	if !f.mergeOpen {
		return nil
	}
	if f.merge.seq != f.Seq {
		return fmt.Errorf("flushmerge seq mismatch")
	}
	if f.merge.n != 0 {
		if err := wrDelete(f.Epsilon, f.Seq, true, f.merge.p0, f.merge.p0+f.merge.n); err != nil {
			return err
		}
	}
	if f.merge.nbuf != 0 {
		if err := wrInsert(f.Epsilon, f.Seq, true, f.merge.p0+f.merge.n, f.merge.buf[:f.merge.nbuf]); err != nil {
			return err
		}
	}
	f.mergeOpen = false
	f.merge.n = 0
	f.merge.nbuf = 0
	return nil
}

// MarkState starts a new modification sequence for the file.
func (f *File) MarkState(currentSeq uint32) error {
	if f.Unread {
		return nil
	}
	if f.Epsilon.Len() > 0 {
		if err := f.Epsilon.Delete(0, Posn(f.Epsilon.Len())); err != nil {
			return err
		}
	}
	f.PrevDot = f.Dot
	f.PrevMark = f.Mark
	f.PrevSeq = f.Seq
	f.PrevMod = f.Mod
	f.NDot = f.Dot
	f.Seq = currentSeq
	f.HiPosn = 0
	return nil
}

// Update applies the pending epsilon log into the file buffer and reverses it into delta.
func (f *File) Update(notrans bool) (changed bool, q0, q1 Posn, err error) {
	if f.Rescuing {
		return false, 0, 0, nil
	}
	if err := f.FlushMerge(); err != nil {
		return false, 0, 0, err
	}

	mod := f.Mod
	f.Mod = f.PrevMod
	if err := f.unsetDot(f.Delta, f.PrevDot); err != nil {
		return false, 0, 0, err
	}
	if err := f.unsetMark(f.Delta, f.PrevMark); err != nil {
		return false, 0, 0, err
	}
	f.Dot = f.NDot
	q0, q1, err = f.Undo(false, !notrans)
	if err != nil {
		return false, 0, 0, err
	}
	f.Mod = mod

	if f.Delta.Len() == 0 {
		f.Seq = 0
	}
	return true, q0, q1, nil
}

// AbortPendingSequence discards an unflushed epsilon sequence and restores the
// file metadata captured by MarkState.
func (f *File) AbortPendingSequence(seq uint32) error {
	f.mergeOpen = false
	f.merge.n = 0
	f.merge.nbuf = 0
	f.HiPosn = 0
	if f.Epsilon.Len() > 0 {
		if err := f.Epsilon.Delete(0, Posn(f.Epsilon.Len())); err != nil {
			return err
		}
	}
	if f.Seq == seq {
		f.Seq = f.PrevSeq
		f.Dot = f.PrevDot
		f.NDot = f.PrevDot
		f.Mark = f.PrevMark
		f.Mod = f.PrevMod
	}
	return nil
}

// Undo undoes or redoes one sequence of changes.
func (f *File) Undo(isUndo, canRedo bool) (q0, q1 Posn, err error) {
	var delta, epsilon *Buffer
	var stop uint32
	if isUndo {
		delta = f.Delta
		epsilon = f.Epsilon
		stop = f.Seq
	} else {
		delta = f.Epsilon
		epsilon = f.Delta
	}

	for delta.Len() > 0 {
		up := Posn(delta.Len() - undoSize)
		u, err := readUndo(delta, up)
		if err != nil {
			return 0, 0, err
		}
		if isUndo {
			if u.Seq < stop {
				f.Seq = u.Seq
				return q0, q1, nil
			}
		} else {
			if stop == 0 {
				stop = u.Seq
			}
			if u.Seq > stop {
				return q0, q1, nil
			}
		}

		switch u.Type {
		case undoDelete:
			f.Seq = u.Seq
			if canRedo {
				if err := f.undelete(epsilon, u.P0, u.P0+u.N); err != nil {
					return 0, 0, err
				}
			}
			f.Mod = u.Mod
			if err := f.B.Delete(u.P0, u.P0+u.N); err != nil {
				return 0, 0, err
			}
			q0, q1 = u.P0, u.P0

		case undoInsert:
			f.Seq = u.Seq
			if canRedo {
				if err := f.uninsert(epsilon, u.P0, u.N); err != nil {
					return 0, 0, err
				}
			}
			f.Mod = u.Mod
			up -= u.N
			buf := make([]rune, u.N)
			if err := delta.Read(up, buf); err != nil {
				return 0, 0, err
			}
			if err := f.B.Insert(u.P0, buf); err != nil {
				return 0, 0, err
			}
			q0, q1 = u.P0, u.P0+u.N

		case undoFilename:
			f.Seq = u.Seq
			if canRedo {
				if err := f.unsetName(epsilon); err != nil {
					return 0, 0, err
				}
			}
			f.Mod = u.Mod
			up -= u.N
			name := make([]rune, u.N)
			if u.N > 0 {
				if err := delta.Read(up, name); err != nil {
					return 0, 0, err
				}
			}
			if err := f.Name.DupRunes(name); err != nil {
				return 0, 0, err
			}

		case undoDot:
			f.Seq = u.Seq
			if canRedo {
				if err := f.unsetDot(epsilon, f.Dot); err != nil {
					return 0, 0, err
				}
			}
			f.Mod = u.Mod
			f.Dot = Range{P1: u.P0, P2: u.P0 + u.N}

		case undoMark:
			f.Seq = u.Seq
			if canRedo {
				if err := f.unsetMark(epsilon, f.Mark); err != nil {
					return 0, 0, err
				}
			}
			f.Mod = u.Mod
			f.Mark = Range{P1: u.P0, P2: u.P0 + u.N}

		default:
			return 0, 0, fmt.Errorf("unknown undo type %q", u.Type)
		}

		if err := delta.Delete(up, Posn(delta.Len())); err != nil {
			return 0, 0, err
		}
	}
	if isUndo {
		f.Seq = 0
	}
	return q0, q1, nil
}

// Reset clears the undo buffers and sequence.
func (f *File) Reset() error {
	if err := f.Delta.Reset(); err != nil {
		return err
	}
	if err := f.Epsilon.Reset(); err != nil {
		return err
	}
	f.Seq = 0
	return nil
}

func (f *File) mergeExtend(p0 Posn) error {
	mp0n := f.merge.p0 + f.merge.n
	if mp0n == p0 {
		return nil
	}
	gap := int(p0 - mp0n)
	if gap < 0 {
		return fmt.Errorf("mergeextend out of order")
	}
	tmp := make([]rune, gap)
	if err := f.B.Read(mp0n, tmp); err != nil {
		return err
	}
	copy(f.merge.buf[f.merge.nbuf:], tmp)
	f.merge.nbuf += gap
	f.merge.n = p0 - f.merge.p0
	return nil
}

func (f *File) uninsert(delta *Buffer, p0, ns Posn) error {
	u := undo{Type: undoDelete, Mod: f.Mod, Seq: f.Seq, P0: p0, N: ns}
	return delta.Insert(Posn(delta.Len()), encodeUndo(u))
}

func (f *File) undelete(delta *Buffer, p0, p1 Posn) error {
	u := undo{Type: undoInsert, Mod: f.Mod, Seq: f.Seq, P0: p0, N: p1 - p0}
	if p1 > p0 {
		buf := make([]rune, p1-p0)
		if err := f.B.Read(p0, buf); err != nil {
			return err
		}
		if err := delta.Insert(Posn(delta.Len()), buf); err != nil {
			return err
		}
	}
	return delta.Insert(Posn(delta.Len()), encodeUndo(u))
}

func (f *File) unsetName(delta *Buffer) error {
	u := undo{Type: undoFilename, Mod: f.Mod, Seq: f.Seq, P0: 0, N: Posn(f.Name.Len())}
	if f.Name.Len() > 0 {
		if err := delta.Insert(Posn(delta.Len()), f.Name.Runes()); err != nil {
			return err
		}
	}
	return delta.Insert(Posn(delta.Len()), encodeUndo(u))
}

func (f *File) unsetDot(delta *Buffer, dot Range) error {
	u := undo{Type: undoDot, Mod: f.Mod, Seq: f.Seq, P0: dot.P1, N: dot.P2 - dot.P1}
	return delta.Insert(Posn(delta.Len()), encodeUndo(u))
}

func (f *File) unsetMark(delta *Buffer, mark Range) error {
	u := undo{Type: undoMark, Mod: f.Mod, Seq: f.Seq, P0: mark.P1, N: mark.P2 - mark.P1}
	return delta.Insert(Posn(delta.Len()), encodeUndo(u))
}

func wrInsert(delta *Buffer, seq uint32, mod bool, p0 Posn, s []rune) error {
	u := undo{Type: undoInsert, Mod: mod, Seq: seq, P0: p0, N: Posn(len(s))}
	if err := delta.Insert(Posn(delta.Len()), s); err != nil {
		return err
	}
	return delta.Insert(Posn(delta.Len()), encodeUndo(u))
}

func wrDelete(delta *Buffer, seq uint32, mod bool, p0, p1 Posn) error {
	u := undo{Type: undoDelete, Mod: mod, Seq: seq, P0: p0, N: p1 - p0}
	return delta.Insert(Posn(delta.Len()), encodeUndo(u))
}

func prevSeq(b *Buffer) (uint32, error) {
	up := b.Len()
	if up == 0 {
		return 0, nil
	}
	return readUndoSeq(b, Posn(up-undoSize))
}

func readUndoSeq(b *Buffer, pos Posn) (uint32, error) {
	u, err := readUndo(b, pos)
	if err != nil {
		return 0, err
	}
	return u.Seq, nil
}

func readUndo(b *Buffer, pos Posn) (undo, error) {
	data := make([]rune, undoSize)
	if err := b.Read(pos, data); err != nil {
		return undo{}, err
	}
	return decodeUndo(data)
}

func encodeUndo(u undo) []rune {
	mod := rune(0)
	if u.Mod {
		mod = 1
	}
	return []rune{
		u.Type,
		mod,
		rune(u.Seq),
		rune(u.P0),
		rune(u.N),
	}
}

func decodeUndo(data []rune) (undo, error) {
	if len(data) != undoSize {
		return undo{}, fmt.Errorf("invalid undo size: %d", len(data))
	}
	return undo{
		Type: data[0],
		Mod:  data[1] != 0,
		Seq:  uint32(data[2]),
		P0:   Posn(data[3]),
		N:    Posn(data[4]),
	}, nil
}
