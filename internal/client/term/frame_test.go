package term

import (
	"bytes"
	"strings"
	"testing"

	"ion/internal/proto/wire"
)

func TestBuildBufferFramePaintsCollapsedCursorCell(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.visible = true
	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   1,
	})

	frame := buildBufferFrame(state, overlay, newMenuState(), theme, true)
	cell := frame.rows[0].cells[1]
	if got, want := cell.r, 'l'; got != want {
		t.Fatalf("cell rune = %q, want %q", got, want)
	}
	if got, want := cell.style, theme.cursorPrefix(); got != want {
		t.Fatalf("cell style = %q, want %q", got, want)
	}
}

func TestBuildBufferFramePaintsHUDPaddingRow(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.visible = true
	overlay.input = []rune(",p")
	overlay.cursor = len(overlay.input)
	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 0,
		DotEnd:   0,
	})

	frame := buildBufferFrame(state, overlay, newMenuState(), theme, true)
	row := frame.rows[overlayTopRow(overlay)]
	for i, cell := range row.cells {
		if got, want := cell.style, theme.hudPrefix(); got != want {
			t.Fatalf("row cell %d style = %q, want %q", i, got, want)
		}
		if got, want := cell.r, ' '; got != want {
			t.Fatalf("row cell %d rune = %q, want %q", i, got, want)
		}
	}
}

func TestBuildBufferFrameOverlaysMenuCells(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 12, 40
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	state := newBufferState(wire.BufferView{
		Name:     "alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	menu := buildContextMenu(state, nil, 6, 3, menuStickyState{})

	frame := buildBufferFrame(state, nil, menu, theme, true)
	border := frame.rows[menu.y].cells[menu.x]
	if got, want := border.r, '╭'; got != want {
		t.Fatalf("menu border rune = %q, want %q", got, want)
	}
	if got, want := border.style, theme.subtlePrefix(); got != want {
		t.Fatalf("menu border style = %q, want %q", got, want)
	}
}

func TestWriteFullFrameUsesFrameCursorAndTitle(t *testing.T) {
	t.Parallel()

	frame := newTerminalFrame(2, 4)
	frame.title = "/tmp/alpha.txt"
	frame.rows[0].cells[0] = frameCell{r: 'a', style: "\x1b[1m"}
	frame.cursor = frameCursor{visible: true, row: 1, col: 2}

	var out bytes.Buffer
	if err := writeFullFrame(&out, frame); err != nil {
		t.Fatalf("writeFullFrame() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b]2;alpha.txt\x07") {
		t.Fatalf("writeFullFrame() = %q, want basename-only title sequence", got)
	}
	if !strings.Contains(got, "\x1b[2;3H") {
		t.Fatalf("writeFullFrame() = %q, want cursor move to row 2 col 3", got)
	}
}

func TestWriteFrameDiffUnchangedEmitsNothing(t *testing.T) {
	t.Parallel()

	prev := newTerminalFrame(2, 4)
	prev.title = "/tmp/alpha.txt"
	prev.rows[0].cells[0] = frameCell{r: 'a', style: "\x1b[1m"}
	prev.cursor = frameCursor{visible: true, row: 0, col: 1}
	next := cloneTerminalFrame(prev)

	var out bytes.Buffer
	if err := writeFrameDiff(&out, prev, next); err != nil {
		t.Fatalf("writeFrameDiff() error = %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("writeFrameDiff() = %q, want empty output for unchanged frame", got)
	}
}

func TestWriteFrameDiffSingleRowChangeRewritesOnlyChangedRow(t *testing.T) {
	t.Parallel()

	prev := newTerminalFrame(2, 4)
	prev.rows[0].cells[0] = frameCell{r: 'a'}
	prev.rows[1].cells[0] = frameCell{r: 'b'}
	prev.cursor = frameCursor{visible: true, row: 1, col: 0}
	next := cloneTerminalFrame(prev)
	next.rows[1].cells[0] = frameCell{r: 'z'}

	var out bytes.Buffer
	if err := writeFrameDiff(&out, prev, next); err != nil {
		t.Fatalf("writeFrameDiff() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[2;1H\x1b[2Kz") {
		t.Fatalf("writeFrameDiff() = %q, want row-2 repaint for changed row", got)
	}
	if strings.Contains(got, "\x1b[1;1H\x1b[2K") {
		t.Fatalf("writeFrameDiff() = %q, want row 1 left untouched", got)
	}
}

func TestWriteFrameDiffCursorOnlyMoveDoesNotClearRows(t *testing.T) {
	t.Parallel()

	prev := newTerminalFrame(1, 4)
	prev.rows[0].cells[0] = frameCell{r: 'a'}
	prev.cursor = frameCursor{visible: true, row: 0, col: 0}
	next := cloneTerminalFrame(prev)
	next.cursor = frameCursor{visible: true, row: 0, col: 2}

	var out bytes.Buffer
	if err := writeFrameDiff(&out, prev, next); err != nil {
		t.Fatalf("writeFrameDiff() error = %v", err)
	}
	if got, want := out.String(), "\x1b[?25h\x1b[1;3H"; got != want {
		t.Fatalf("writeFrameDiff() = %q, want %q", got, want)
	}
}

func TestWriteFrameDiffTitleOnlyEmitsTitleSequence(t *testing.T) {
	t.Parallel()

	prev := newTerminalFrame(1, 4)
	prev.title = "/tmp/alpha.txt"
	next := cloneTerminalFrame(prev)
	next.title = "/tmp/beta.txt"

	var out bytes.Buffer
	if err := writeFrameDiff(&out, prev, next); err != nil {
		t.Fatalf("writeFrameDiff() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b]2;beta.txt\x07") {
		t.Fatalf("writeFrameDiff() = %q, want updated title sequence", got)
	}
	if strings.Contains(got, "\x1b[2K") {
		t.Fatalf("writeFrameDiff() = %q, want no row clears for title-only change", got)
	}
}

func TestFrameRendererRecordsStatsByClass(t *testing.T) {
	t.Parallel()

	renderer := newFrameRenderer()
	stats := &frameRenderStats{
		enabled: true,
		counts:  make(map[redrawClass]*frameRenderAggregate),
	}

	initial := newTerminalFrame(1, 4)
	var out bytes.Buffer
	if err := renderer.Render(&out, initial, redrawInitial, true, stats); err != nil {
		t.Fatalf("Render(initial) error = %v", err)
	}
	if got := stats.counts[redrawInitial]; got == nil || got.full != 1 || got.rows != 1 || got.bytes == 0 {
		t.Fatalf("initial stats = %#v, want one full render with one touched row and nonzero bytes", got)
	}

	out.Reset()
	next := cloneTerminalFrame(initial)
	next.cursor = frameCursor{visible: true, row: 0, col: 1}
	if err := renderer.Render(&out, next, redrawBufferCursor, false, stats); err != nil {
		t.Fatalf("Render(buffer) error = %v", err)
	}
	if got := stats.counts[redrawBufferCursor]; got == nil || got.diff != 1 || got.renders != 1 || got.bytes == 0 {
		t.Fatalf("buffer stats = %#v, want one diff render with nonzero bytes", got)
	}
}

func TestWriteFrameDiffTerminalStateOnlyEmitsModeTransitions(t *testing.T) {
	t.Parallel()

	prev := newTerminalFrame(1, 4)
	next := cloneTerminalFrame(prev)
	next.terminal.bracketedPaste = false
	next.terminal.cursorShape = frameCursorShapeBlock

	var out bytes.Buffer
	if err := writeFrameDiff(&out, prev, next); err != nil {
		t.Fatalf("writeFrameDiff() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[2 q") {
		t.Fatalf("writeFrameDiff() = %q, want block cursor transition", got)
	}
	if !strings.Contains(got, "\x1b[?2004l") {
		t.Fatalf("writeFrameDiff() = %q, want bracketed-paste disable", got)
	}
	if strings.Contains(got, "\x1b[2K") {
		t.Fatalf("writeFrameDiff() = %q, want no row clears for terminal-state-only change", got)
	}
}

func TestFrameRendererRecoverForcesFullRender(t *testing.T) {
	t.Parallel()

	renderer := newFrameRenderer()
	stats := &frameRenderStats{
		enabled: true,
		counts:  make(map[redrawClass]*frameRenderAggregate),
	}
	frame := newTerminalFrame(1, 4)

	var out bytes.Buffer
	if err := renderer.Render(&out, frame, redrawInitial, true, stats); err != nil {
		t.Fatalf("Render(initial) error = %v", err)
	}

	out.Reset()
	if err := renderer.Recover(&out, frame, redrawRecover, stats); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[2J") {
		t.Fatalf("Recover() = %q, want full-screen clear", got)
	}
	if stats.counts[redrawRecover] == nil || stats.counts[redrawRecover].full != 1 {
		t.Fatalf("recover stats = %#v, want one full recover render", stats.counts[redrawRecover])
	}
}
