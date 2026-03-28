package term

func composeRootGrid(root *ScreenGrid, builder *GridLineBuilder, layers ...*ScreenGrid) {
	if root == nil || builder == nil {
		return
	}
	for row := 0; row < root.rows; row++ {
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
