package term

import "testing"

func TestComposeRootGridRestoresWideRuneAfterOverlayMoves(t *testing.T) {
	root := newScreenGrid(1, 4)
	buffer := newScreenGrid(1, 4)
	bufferBuilder := newGridLineBuilder(buffer.cols)
	bufferBuilder.Start(buffer, 0)
	col := bufferBuilder.PutRune(0, '🦋', 0, bufferTabWidth)
	bufferBuilder.PutRune(col, 'b', 0, bufferTabWidth)
	bufferBuilder.Flush()

	overlay := newScreenGrid(1, 1)
	overlay.originCol = 1
	overlayBuilder := newGridLineBuilder(overlay.cols)
	overlayBuilder.Start(overlay, 0)
	overlayBuilder.PutRune(0, 'x', 0, hudTabWidth)
	overlayBuilder.Flush()

	rootBuilder := newGridLineBuilder(root.cols)
	composeRootGrid(root, rootBuilder, []gridDirtySpan{{start: 0, end: root.cols}}, buffer, overlay)
	if got := root.rowCells(0)[0].r; got != ' ' {
		t.Fatalf("partially covered wide rune lead = %q, want blank", got)
	}
	if got := root.rowCells(0)[1].r; got != 'x' {
		t.Fatalf("overlay cell = %q, want x", got)
	}

	overlay.visible = false
	composeRootGrid(root, rootBuilder, []gridDirtySpan{{start: 1, end: 2}}, buffer, overlay)
	cells := root.rowCells(0)
	if got := cells[0].r; got != '🦋' {
		t.Fatalf("restored wide rune lead = %q, want butterfly", got)
	}
	if !cells[1].continuation {
		t.Fatal("restored butterfly second cell is not a continuation")
	}
}

func TestRepairWideCellsRemovesOrphanedHalves(t *testing.T) {
	cells := []gridCell{
		{r: '🦋'},
		{r: 'x'},
		{continuation: true},
	}
	repairWideCells(cells)
	if got := cells[0].r; got != ' ' {
		t.Fatalf("wide lead without continuation = %q, want blank", got)
	}
	if got := cells[2].r; got != ' ' || cells[2].continuation {
		t.Fatalf("orphan continuation = %+v, want a normal blank cell", cells[2])
	}
}
