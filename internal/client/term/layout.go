package term

type bufferLayout struct {
	origin   int
	viewRows int
	wrapCols int
	rows     []bufferLayoutRow
}

type bufferLayoutRow struct {
	start    int
	end      int
	colToPos []int
	posToCol []int
}

func (state *bufferState) visibleLayout(overlay *overlayState) *bufferLayout {
	if state == nil {
		return nil
	}
	origin := visualRowStartForPos(state.text, state.origin)
	viewRows := bufferViewRows(overlay)
	wrapCols := bufferWrapCols()
	if state.layout != nil &&
		state.layout.origin == origin &&
		state.layout.viewRows == viewRows &&
		state.layout.wrapCols == wrapCols {
		return state.layout
	}
	layout := buildBufferLayout(state.text, origin, viewRows, wrapCols)
	state.layout = layout
	return layout
}

func buildBufferLayout(text []rune, origin, viewRows, wrapCols int) *bufferLayout {
	layout := &bufferLayout{
		origin:   visualRowStartForPos(text, origin),
		viewRows: viewRows,
		wrapCols: wrapCols,
	}
	if viewRows < 1 {
		viewRows = 1
	}
	start := layout.origin
	for row := 0; row < viewRows; row++ {
		layout.rows = append(layout.rows, buildBufferLayoutRow(text, start, wrapCols))
		next := nextVisualRowStart(text, start)
		if next == start {
			break
		}
		start = next
	}
	return layout
}

func buildBufferLayoutRow(text []rune, start, wrapCols int) bufferLayoutRow {
	start = clampIndex(start, len(text))
	end := visualRowEnd(text, start)
	if wrapCols < 1 {
		wrapCols = 1
	}
	row := bufferLayoutRow{
		start:    start,
		end:      end,
		colToPos: make([]int, wrapCols),
		posToCol: make([]int, end-start+1),
	}
	for i := range row.colToPos {
		row.colToPos[i] = end
	}
	col := 0
	for pos := start; pos < end; pos++ {
		row.posToCol[pos-start] = col
		if col < len(row.colToPos) {
			row.colToPos[col] = pos
		}
		advance := bufferRuneAdvance(text[pos], col, wrapCols)
		if advance <= 0 {
			break
		}
		for c := col + 1; c < col+advance && c < len(row.colToPos); c++ {
			row.colToPos[c] = pos + 1
		}
		col += advance
	}
	row.posToCol[end-start] = col
	return row
}

func (row bufferLayoutRow) posAtColumn(col int) int {
	if col < 0 {
		col = 0
	}
	if col >= len(row.colToPos) {
		return row.end
	}
	return row.colToPos[col]
}

func (row bufferLayoutRow) columnForPos(pos int) int {
	pos = clampIndex(pos, row.end)
	if pos < row.start {
		return 0
	}
	index := pos - row.start
	if index >= len(row.posToCol) {
		return row.posToCol[len(row.posToCol)-1]
	}
	return row.posToCol[index]
}

func sameRunesString(text []rune, s string) bool {
	index := 0
	for _, r := range s {
		if index >= len(text) || text[index] != r {
			return false
		}
		index++
	}
	return index == len(text)
}

func bufferTextForView(viewText string, previous *bufferState) ([]rune, bool) {
	if previous != nil && sameRunesString(previous.text, viewText) {
		return previous.text, true
	}
	return []rune(viewText), false
}
