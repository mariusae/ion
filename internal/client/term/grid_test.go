package term

import "testing"

func TestGridLineBuilderFlushTracksDirtySpan(t *testing.T) {
	t.Parallel()

	grid := newScreenGrid(1, 6)
	grid.clearDirty()
	builder := newGridLineBuilder(grid.cols)
	builder.Start(grid, 0)

	col := 0
	col = builder.PutRune(col, 'a', 1, bufferTabWidth)
	col = builder.PutRune(col, 'b', 2, bufferTabWidth)
	builder.PutRune(col, 'c', 2, bufferTabWidth)

	span, changed := builder.Flush()
	if !changed {
		t.Fatalf("Flush() changed = false, want true")
	}
	if got, want := span, (gridDirtySpan{start: 0, end: 3}); got != want {
		t.Fatalf("Flush() span = %+v, want %+v", got, want)
	}
	if got, want := gridRowText(grid, 0), "abc   "; got != want {
		t.Fatalf("row text = %q, want %q", got, want)
	}
	if got, want := grid.dirty[0], span; got != want {
		t.Fatalf("dirty span = %+v, want %+v", got, want)
	}
}

func TestGridLineBuilderClearEndErasesTrailingCells(t *testing.T) {
	t.Parallel()

	grid := newScreenGrid(1, 6)
	builder := newGridLineBuilder(grid.cols)
	builder.Start(grid, 0)
	for col, r := range []rune("abcdef") {
		builder.PutRune(col, r, 0, bufferTabWidth)
	}
	if _, changed := builder.Flush(); !changed {
		t.Fatalf("initial Flush() changed = false, want true")
	}

	grid.clearDirty()
	builder.Start(grid, 0)
	builder.PutRune(0, 'a', 0, bufferTabWidth)
	builder.PutRune(1, 'b', 0, bufferTabWidth)
	builder.ClearEnd(2)

	span, changed := builder.Flush()
	if !changed {
		t.Fatalf("Flush() changed = false, want true")
	}
	if got, want := span, (gridDirtySpan{start: 2, end: 6}); got != want {
		t.Fatalf("Flush() span = %+v, want %+v", got, want)
	}
	if got, want := gridRowText(grid, 0), "ab    "; got != want {
		t.Fatalf("row text = %q, want %q", got, want)
	}
}

func TestScreenGridScrollUpRotatesRowOffsets(t *testing.T) {
	t.Parallel()

	grid := newScreenGrid(4, 1)
	for row, r := range []rune{'a', 'b', 'c', 'd'} {
		grid.rowCells(row)[0] = gridCell{r: r}
	}
	grid.clearDirty()

	exposed := grid.Scroll(0, 4, 1)

	if got, want := exposed, []int{3}; !sameIntSlice(got, want) {
		t.Fatalf("Scroll() exposed = %v, want %v", got, want)
	}
	if got, want := gridRowText(grid, 0), "b"; got != want {
		t.Fatalf("row 0 text = %q, want %q", got, want)
	}
	if got, want := gridRowText(grid, 1), "c"; got != want {
		t.Fatalf("row 1 text = %q, want %q", got, want)
	}
	if got, want := gridRowText(grid, 2), "d"; got != want {
		t.Fatalf("row 2 text = %q, want %q", got, want)
	}
	if got, want := gridRowText(grid, 3), " "; got != want {
		t.Fatalf("row 3 text = %q, want %q", got, want)
	}
	if got, want := grid.dirty[3], (gridDirtySpan{start: 0, end: 1}); got != want {
		t.Fatalf("dirty span = %+v, want %+v", got, want)
	}
}

func TestScreenGridScrollDownRotatesRowOffsets(t *testing.T) {
	t.Parallel()

	grid := newScreenGrid(4, 1)
	for row, r := range []rune{'a', 'b', 'c', 'd'} {
		grid.rowCells(row)[0] = gridCell{r: r}
	}
	grid.clearDirty()

	exposed := grid.Scroll(0, 4, -1)

	if got, want := exposed, []int{0}; !sameIntSlice(got, want) {
		t.Fatalf("Scroll() exposed = %v, want %v", got, want)
	}
	if got, want := gridRowText(grid, 0), " "; got != want {
		t.Fatalf("row 0 text = %q, want %q", got, want)
	}
	if got, want := gridRowText(grid, 1), "a"; got != want {
		t.Fatalf("row 1 text = %q, want %q", got, want)
	}
	if got, want := gridRowText(grid, 2), "b"; got != want {
		t.Fatalf("row 2 text = %q, want %q", got, want)
	}
	if got, want := gridRowText(grid, 3), "c"; got != want {
		t.Fatalf("row 3 text = %q, want %q", got, want)
	}
	if got, want := grid.dirty[0], (gridDirtySpan{start: 0, end: 1}); got != want {
		t.Fatalf("dirty span = %+v, want %+v", got, want)
	}
}

func gridRowText(grid *ScreenGrid, row int) string {
	cells := grid.rowCells(row)
	runes := make([]rune, len(cells))
	for i, cell := range cells {
		runes[i] = cell.r
	}
	return string(runes)
}

func sameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
