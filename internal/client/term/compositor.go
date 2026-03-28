package term

func composeRootGrid(root *ScreenGrid, builder *GridLineBuilder, rows []bool, layers ...*ScreenGrid) {
	if root == nil || builder == nil {
		return
	}
	for row := 0; row < root.rows; row++ {
		if len(rows) != 0 && !rows[row] {
			continue
		}
		root.clearRowDirty(row)
		builder.Start(root, row)
		for _, layer := range layers {
			composeGridRow(builder, row, layer)
		}
		builder.Flush()
	}
	root.valid = true
}

func composeGridRow(builder *GridLineBuilder, rootRow int, layer *ScreenGrid) {
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
	startCol := max(layer.originCol, 0)
	endCol := min(layer.originCol+layer.cols, len(builder.cells))
	if startCol >= endCol {
		return
	}
	for rootCol := startCol; rootCol < endCol; rootCol++ {
		cell := layerRow[rootCol-layer.originCol]
		if cell.r == ' ' && cell.style == 0 {
			continue
		}
		builder.PutCell(rootCol, cell)
	}
}

func projectLayerDirtyRows(root *ScreenGrid, layer *ScreenGrid, rows []bool) {
	if root == nil || layer == nil || !layer.visible || len(rows) != root.rows {
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
		rows[rootRow] = true
	}
}

func projectGridDirtyRows(grid *ScreenGrid, rows []bool) {
	if grid == nil || len(rows) != grid.rows {
		return
	}
	for row := 0; row < grid.rows; row++ {
		span := grid.dirty[row]
		if span.start < span.end {
			rows[row] = true
		}
	}
}
