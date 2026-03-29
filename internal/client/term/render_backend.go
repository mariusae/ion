package term

import (
	"fmt"
	"io"
	"strings"
)

type renderBackend interface {
	SetTitle(name string, dirty, changed bool)
	SetTerminalState(prev, next frameTerminalState)
	HideCursor()
	ClearAll()
	ScrollRegion(top, bottom, delta int)
	WriteCells(row, col int, cells []gridCell)
	SetCursor(cursor frameCursor)
	Flush() error
}

type gridStylePalette struct {
	ids  map[string]gridStyleID
	seqs []string
}

type ttyRenderBackend struct {
	out          io.Writer
	palette      *gridStylePalette
	buf          strings.Builder
	cursorHidden bool
}

func newGridStylePalette() *gridStylePalette {
	return &gridStylePalette{
		ids:  map[string]gridStyleID{"": 0},
		seqs: []string{""},
	}
}

func (p *gridStylePalette) ID(seq string) gridStyleID {
	if p == nil || seq == "" {
		return 0
	}
	if id, ok := p.ids[seq]; ok {
		return id
	}
	id := gridStyleID(len(p.seqs))
	p.ids[seq] = id
	p.seqs = append(p.seqs, seq)
	return id
}

func (p *gridStylePalette) Seq(id gridStyleID) string {
	if p == nil {
		return ""
	}
	index := int(id)
	if index < 0 || index >= len(p.seqs) {
		return ""
	}
	return p.seqs[index]
}

func newTTYRenderBackend(out io.Writer, palette *gridStylePalette) *ttyRenderBackend {
	return &ttyRenderBackend{
		out:     out,
		palette: palette,
	}
}

func (b *ttyRenderBackend) SetTitle(name string, dirty, changed bool) {
	if b == nil {
		return
	}
	b.buf.WriteString(bufferWindowTitleSequence(name, dirty, changed))
}

func (b *ttyRenderBackend) SetTerminalState(prev, next frameTerminalState) {
	if b == nil {
		return
	}
	_ = writeFrameTerminalState(&b.buf, prev, next)
}

func (b *ttyRenderBackend) HideCursor() {
	if b == nil || b.cursorHidden {
		return
	}
	b.buf.WriteString("\x1b[?25l")
	b.cursorHidden = true
}

func (b *ttyRenderBackend) ClearAll() {
	if b == nil {
		return
	}
	b.buf.WriteString("\x1b[2J")
}

func (b *ttyRenderBackend) ScrollRegion(top, bottom, delta int) {
	if b == nil || delta == 0 || top >= bottom {
		return
	}
	b.HideCursor()
	fullHeight := top == 0 && bottom == termRows
	if !fullHeight {
		fmt.Fprintf(&b.buf, "\x1b[%d;%dr", top+1, bottom)
	}
	fmt.Fprintf(&b.buf, "\x1b[%d;1H", top+1)
	op := 'L'
	count := -delta
	if delta > 0 {
		op = 'M'
		count = delta
	}
	fmt.Fprintf(&b.buf, "\x1b[%d%c", count, op)
	if !fullHeight {
		b.buf.WriteString("\x1b[r")
	}
}

func (b *ttyRenderBackend) WriteCells(row, col int, cells []gridCell) {
	if b == nil || len(cells) == 0 {
		return
	}
	b.HideCursor()
	fmt.Fprintf(&b.buf, "\x1b[%d;%dH", row+1, col+1)
	b.writeCellRange(cells)
}

func (b *ttyRenderBackend) writeCellRange(cells []gridCell) {
	if b == nil || len(cells) == 0 {
		return
	}
	b.buf.WriteString(styleReset())
	currentStyle := gridStyleID(0)
	for _, cell := range cells {
		if cell.style != currentStyle {
			seq := b.palette.Seq(cell.style)
			if seq == "" {
				b.buf.WriteString(styleReset())
			} else {
				b.buf.WriteString(seq)
			}
			currentStyle = cell.style
		}
		r := cell.r
		if r == 0 {
			r = ' '
		}
		b.buf.WriteRune(r)
	}
	if currentStyle != 0 {
		b.buf.WriteString(styleReset())
	}
}

func (b *ttyRenderBackend) SetCursor(cursor frameCursor) {
	if b == nil {
		return
	}
	if !cursor.visible {
		b.buf.WriteString("\x1b[?25l")
		return
	}
	fmt.Fprintf(&b.buf, "\x1b[?25h\x1b[%d;%dH", cursor.row+1, cursor.col+1)
}

func (b *ttyRenderBackend) Flush() error {
	if b == nil || b.out == nil || b.buf.Len() == 0 {
		return nil
	}
	_, err := io.WriteString(b.out, b.buf.String())
	return err
}
