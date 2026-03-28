package term

type gridStyleID uint32

type gridCell struct {
	r     rune
	style gridStyleID
}

type gridDirtySpan struct {
	start int
	end   int
}

type ScreenGrid struct {
	rows       int
	cols       int
	cells      []gridCell
	lineOffset []int
	valid      bool
	visible    bool
	zindex     int
	originRow  int
	originCol  int
	dirty      []gridDirtySpan
}

type GridLineBuilder struct {
	grid  *ScreenGrid
	row   int
	cells []gridCell
}

func newScreenGrid(rows, cols int) *ScreenGrid {
	grid := &ScreenGrid{visible: true}
	grid.Resize(rows, cols)
	return grid
}

func (g *ScreenGrid) Resize(rows, cols int) {
	if rows < 0 {
		rows = 0
	}
	if cols < 0 {
		cols = 0
	}
	g.rows = rows
	g.cols = cols
	g.cells = make([]gridCell, rows*cols)
	g.lineOffset = make([]int, rows)
	g.dirty = make([]gridDirtySpan, rows)
	for row := 0; row < rows; row++ {
		g.lineOffset[row] = row * cols
		g.resetRow(row)
	}
	g.invalidate()
}

func (g *ScreenGrid) invalidate() {
	g.valid = false
	for row := 0; row < g.rows; row++ {
		g.markDirty(row, 0, g.cols)
	}
}

func (g *ScreenGrid) clearDirty() {
	for row := range g.dirty {
		g.dirty[row] = gridDirtySpan{start: g.cols}
	}
}

func (g *ScreenGrid) clearRowDirty(row int) {
	if g == nil || row < 0 || row >= len(g.dirty) {
		return
	}
	g.dirty[row] = gridDirtySpan{start: g.cols}
}

func (g *ScreenGrid) markDirty(row, start, end int) {
	if g == nil || row < 0 || row >= g.rows || g.cols == 0 {
		return
	}
	if start < 0 {
		start = 0
	}
	if end > g.cols {
		end = g.cols
	}
	if start >= end {
		return
	}
	span := &g.dirty[row]
	if span.start >= span.end {
		*span = gridDirtySpan{start: start, end: end}
		return
	}
	if start < span.start {
		span.start = start
	}
	if end > span.end {
		span.end = end
	}
}

func (g *ScreenGrid) rowCells(row int) []gridCell {
	if g == nil || row < 0 || row >= g.rows || g.cols == 0 {
		return nil
	}
	offset := g.lineOffset[row]
	return g.cells[offset : offset+g.cols]
}

func (g *ScreenGrid) resetRow(row int) {
	cells := g.rowCells(row)
	for col := range cells {
		cells[col] = gridCell{r: ' '}
	}
}

func (g *ScreenGrid) Scroll(top, bottom, delta int) []int {
	if g == nil {
		return nil
	}
	if top < 0 {
		top = 0
	}
	if bottom > g.rows {
		bottom = g.rows
	}
	if top >= bottom || delta == 0 {
		return nil
	}
	height := bottom - top
	if delta >= height || delta <= -height {
		exposed := make([]int, 0, height)
		for row := top; row < bottom; row++ {
			g.resetRow(row)
			g.markDirty(row, 0, g.cols)
			exposed = append(exposed, row)
		}
		return exposed
	}
	if delta > 0 {
		rotated := append([]int(nil), g.lineOffset[top+delta:bottom]...)
		rotated = append(rotated, g.lineOffset[top:top+delta]...)
		copy(g.lineOffset[top:bottom], rotated)
		exposed := make([]int, 0, delta)
		for row := bottom - delta; row < bottom; row++ {
			g.resetRow(row)
			g.markDirty(row, 0, g.cols)
			exposed = append(exposed, row)
		}
		return exposed
	}
	count := -delta
	rotated := append([]int(nil), g.lineOffset[bottom-count:bottom]...)
	rotated = append(rotated, g.lineOffset[top:bottom-count]...)
	copy(g.lineOffset[top:bottom], rotated)
	exposed := make([]int, 0, count)
	for row := top; row < top+count; row++ {
		g.resetRow(row)
		g.markDirty(row, 0, g.cols)
		exposed = append(exposed, row)
	}
	return exposed
}

func newGridLineBuilder(cols int) *GridLineBuilder {
	builder := &GridLineBuilder{}
	if cols > 0 {
		builder.cells = make([]gridCell, cols)
	}
	return builder
}

func (b *GridLineBuilder) Start(grid *ScreenGrid, row int) {
	b.grid = grid
	b.row = row
	if grid == nil || grid.cols < 0 {
		b.cells = b.cells[:0]
		return
	}
	if cap(b.cells) < grid.cols {
		b.cells = make([]gridCell, grid.cols)
	} else {
		b.cells = b.cells[:grid.cols]
	}
	for col := range b.cells {
		b.cells[col] = gridCell{r: ' '}
	}
}

func (b *GridLineBuilder) PutCell(col int, cell gridCell) {
	if col < 0 || col >= len(b.cells) {
		return
	}
	if cell.r == 0 {
		cell.r = ' '
	}
	b.cells[col] = cell
}

func (b *GridLineBuilder) PutRune(col int, r rune, style gridStyleID, tabWidth int) int {
	if col < 0 || col >= len(b.cells) {
		return col
	}
	advance := runeDisplayAdvance(r, col, len(b.cells), tabWidth)
	if advance <= 0 {
		return col
	}
	if r == '\t' {
		for i := 0; i < advance; i++ {
			b.cells[col+i] = gridCell{r: ' ', style: style}
		}
		return col + advance
	}
	b.cells[col] = gridCell{r: r, style: style}
	return col + 1
}

func (b *GridLineBuilder) Fill(start, end int, cell gridCell) {
	if start < 0 {
		start = 0
	}
	if end > len(b.cells) {
		end = len(b.cells)
	}
	if start >= end {
		return
	}
	if cell.r == 0 {
		cell.r = ' '
	}
	for col := start; col < end; col++ {
		b.cells[col] = cell
	}
}

func (b *GridLineBuilder) ClearEnd(col int) {
	b.Fill(col, len(b.cells), gridCell{r: ' '})
}

func (b *GridLineBuilder) Flush() (gridDirtySpan, bool) {
	if b.grid == nil || b.row < 0 || b.row >= b.grid.rows {
		return gridDirtySpan{}, false
	}
	row := b.grid.rowCells(b.row)
	span := gridDirtySpan{start: len(row)}
	changed := false
	for col := range row {
		if row[col] == b.cells[col] {
			continue
		}
		row[col] = b.cells[col]
		if !changed {
			span = gridDirtySpan{start: col, end: col + 1}
			changed = true
			continue
		}
		span.end = col + 1
	}
	if !changed {
		return gridDirtySpan{}, false
	}
	b.grid.valid = true
	b.grid.markDirty(b.row, span.start, span.end)
	return span, true
}
