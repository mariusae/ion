package term

type gridRect struct {
	top    int
	bottom int
	left   int
	right  int
	ok     bool
}

func composeRootGrid(root *ScreenGrid, builder *GridLineBuilder, spans []gridDirtySpan, layers ...*ScreenGrid) {
	if root == nil || builder == nil {
		return
	}
	for row := 0; row < root.rows; row++ {
		span := spanAt(spans, row, root.cols)
		if span.start >= span.end {
			continue
		}
		span = expandRootSpanForWideCells(root, row, span, layers)
		root.clearRowDirty(row)
		builder.StartFromGrid(root, row)
		builder.Fill(span.start, span.end, gridCell{r: ' '})
		for _, layer := range layers {
			composeGridSpan(builder, row, span.start, span.end, layer)
		}
		repairWideCells(builder.cells)
		builder.Flush()
	}
	root.valid = true
}

func expandRootSpanForWideCells(root *ScreenGrid, rootRow int, span gridDirtySpan, layers []*ScreenGrid) gridDirtySpan {
	for {
		previous := span
		span = expandGridDirtySpan(root.rowCells(rootRow), span)
		for _, layer := range layers {
			if layer == nil || !layer.visible {
				continue
			}
			localRow := rootRow - layer.originRow
			if localRow < 0 || localRow >= layer.rows {
				continue
			}
			cells := layer.rowCells(localRow)
			left := layer.originCol
			right := left + len(cells)
			if span.start >= left && span.start < right {
				for span.start > left && cells[span.start-left].continuation {
					span.start--
				}
			}
			if span.end > left && span.end < right {
				for span.end < right && cells[span.end-left].continuation {
					span.end++
				}
			}
		}
		span.start = max(span.start, 0)
		span.end = min(span.end, root.cols)
		if span.start >= span.end {
			return gridDirtySpan{start: root.cols}
		}
		if span == previous {
			return span
		}
	}
}

func repairWideCells(cells []gridCell) {
	expectedContinuation := 0
	wideLead := -1
	for col := range cells {
		cell := cells[col]
		if cell.continuation {
			if expectedContinuation == 0 || cell.style != cells[wideLead].style {
				if expectedContinuation > 0 {
					cells[wideLead] = gridCell{r: ' ', style: cells[wideLead].style}
				}
				cells[col] = gridCell{r: ' ', style: cell.style}
				expectedContinuation = 0
				wideLead = -1
				continue
			}
			expectedContinuation--
			if expectedContinuation == 0 {
				wideLead = -1
			}
			continue
		}
		if expectedContinuation > 0 {
			cells[wideLead] = gridCell{r: ' ', style: cells[wideLead].style}
			expectedContinuation = 0
			wideLead = -1
		}
		width := runeDisplayWidth(cell.r)
		if width <= 1 {
			continue
		}
		if col+width > len(cells) {
			cells[col] = gridCell{r: ' ', style: cell.style}
			continue
		}
		wideLead = col
		expectedContinuation = width - 1
	}
	if expectedContinuation > 0 {
		cells[wideLead] = gridCell{r: ' ', style: cells[wideLead].style}
	}
}

func composeGridSpan(builder *GridLineBuilder, rootRow, startCol, endCol int, layer *ScreenGrid) {
	if builder == nil || layer == nil || !layer.visible {
		return
	}
	localRow := rootRow - layer.originRow
	if localRow < 0 || localRow >= layer.rows {
		return
	}
	layerRow := layer.rowCells(localRow)
	if len(layerRow) == 0 {
		return
	}
	start := max(startCol, layer.originCol)
	end := min(endCol, layer.originCol+layer.cols)
	if start >= end {
		return
	}
	for rootCol := start; rootCol < end; rootCol++ {
		cell := layerRow[rootCol-layer.originCol]
		if cell.r == ' ' && cell.style == 0 {
			continue
		}
		builder.PutCell(rootCol, cell)
	}
}

func projectGridDirtySpans(grid *ScreenGrid, spans []gridDirtySpan) {
	if grid == nil || len(spans) != grid.rows {
		return
	}
	for row := 0; row < grid.rows; row++ {
		mergeDirtySpan(spans, row, grid.dirty[row])
	}
}

func projectLayerDirtySpans(root *ScreenGrid, layer *ScreenGrid, spans []gridDirtySpan) {
	if root == nil || layer == nil || !layer.visible || len(spans) != root.rows {
		return
	}
	for row := 0; row < layer.rows; row++ {
		span := layer.dirty[row]
		if span.start >= span.end {
			continue
		}
		rootRow := layer.originRow + row
		if rootRow < 0 || rootRow >= root.rows {
			continue
		}
		mergeDirtySpan(spans, rootRow, gridDirtySpan{
			start: layer.originCol + span.start,
			end:   layer.originCol + span.end,
		})
	}
}

func mergeDirtySpan(spans []gridDirtySpan, row int, span gridDirtySpan) {
	if row < 0 || row >= len(spans) || span.start >= span.end {
		return
	}
	dst := &spans[row]
	if dst.start >= dst.end {
		*dst = span
		return
	}
	if span.start < dst.start {
		dst.start = span.start
	}
	if span.end > dst.end {
		dst.end = span.end
	}
}

func markRect(spans []gridDirtySpan, rect gridRect) {
	if !rect.ok || len(spans) == 0 {
		return
	}
	top := max(rect.top, 0)
	bottom := min(rect.bottom, len(spans))
	if top >= bottom {
		return
	}
	for row := top; row < bottom; row++ {
		mergeDirtySpan(spans, row, gridDirtySpan{start: rect.left, end: rect.right})
	}
}

func visibleGridRect(grid *ScreenGrid) gridRect {
	if grid == nil || !grid.visible || grid.rows <= 0 || grid.cols <= 0 {
		return gridRect{}
	}
	return gridRect{
		top:    grid.originRow,
		bottom: grid.originRow + grid.rows,
		left:   grid.originCol,
		right:  grid.originCol + grid.cols,
		ok:     true,
	}
}

func spanAt(spans []gridDirtySpan, row, cols int) gridDirtySpan {
	if row < 0 || row >= len(spans) {
		return gridDirtySpan{start: cols}
	}
	span := spans[row]
	if span.start < 0 {
		span.start = 0
	}
	if span.end > cols {
		span.end = cols
	}
	if span.start >= span.end {
		return gridDirtySpan{start: cols}
	}
	return span
}
