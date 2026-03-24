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
