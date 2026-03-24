package term

import (
	"testing"

	"ion/internal/proto/wire"
)

func TestVisibleLayoutMapsTabColumns(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 16
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "\tab\n",
		DotStart: 0,
		DotEnd:   0,
	})
	layout := state.visibleLayout(nil)
	if got, want := layout.rows[0].posAtColumn(0), 0; got != want {
		t.Fatalf("posAtColumn(0) = %d, want %d", got, want)
	}
	if got, want := layout.rows[0].posAtColumn(3), 1; got != want {
		t.Fatalf("posAtColumn(3) = %d, want %d", got, want)
	}
}

func TestNewBufferStateWithPreviousReusesTextAndLayout(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 16
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	prev := newBufferState(wire.BufferView{
		ID:       7,
		Text:     "alpha\tbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	layout := prev.visibleLayout(nil)

	next := newBufferStateWithPrevious(wire.BufferView{
		ID:       7,
		Text:     "alpha\tbeta\n",
		DotStart: 2,
		DotEnd:   2,
	}, prev)

	if &next.text[0] != &prev.text[0] {
		t.Fatalf("text slice was not reused")
	}
	if next.layout != layout {
		t.Fatalf("layout cache was not reused")
	}
}

func TestScreenToPosTrailingBlankRowsReuseLastVisibleRow(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 16
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "abc\n",
		DotStart: 0,
		DotEnd:   0,
	})
	got, ok := screenToPos(state, nil, 4, 2)
	if !ok {
		t.Fatalf("screenToPos() ok = false, want true")
	}
	if want := 2; got != want {
		t.Fatalf("screenToPos(trailing blank row) = %d, want %d", got, want)
	}
}
