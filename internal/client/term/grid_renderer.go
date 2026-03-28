package term

import "io"

type gridRenderer struct {
	trace *traceLogger

	palette    *gridStylePalette
	root       *ScreenGrid
	buffer     *ScreenGrid
	hudHistory *ScreenGrid
	hudInput   *ScreenGrid
	menu       *ScreenGrid

	lastTitle      string
	lastTitleDirty bool
	lastTerminal   frameTerminalState
	lastCursor     frameCursor
	initialized    bool

	bufferAnchors  []int
	overlayAnchors []int
}

type gridScrollOp struct {
	top    int
	bottom int
	delta  int
}

func newGridRenderer() *gridRenderer {
	return &gridRenderer{
		trace:   newTraceLogger(),
		palette: newGridStylePalette(),
	}
}

func (r *gridRenderer) Reset() {
	if r == nil {
		return
	}
	r.root = nil
	r.buffer = nil
	r.hudHistory = nil
	r.hudInput = nil
	r.menu = nil
	r.lastTitle = ""
	r.lastTitleDirty = false
	r.lastTerminal = frameTerminalState{}
	r.lastCursor = frameCursor{}
	r.initialized = false
	r.bufferAnchors = nil
	r.overlayAnchors = nil
}

func (r *gridRenderer) Draw(stdout io.Writer, req renderRequest, state *bufferState, overlay *overlayState, menu *menuState, theme *uiTheme, focused bool, stats *frameRenderStats) error {
	if state == nil {
		return nil
	}
	if r == nil {
		renderer := newGridRenderer()
		return renderer.Draw(stdout, req, state, overlay, menu, theme, focused, stats)
	}
	prevRects := r.layerRects()
	rootChanged := r.ensureGrids(overlay, menu)
	forceFull := req.forceFull || !r.initialized || rootChanged

	nextTitle := state.name
	nextTitleDirty := state.dirty
	nextTerminal := defaultFrameTerminalState()
	nextCursor := buildFrameCursor(state, overlay, menu, focused)

	bufferAnchors := currentBufferAnchors(state, overlay, r.buffer.rows)
	overlayAnchors := currentOverlayAnchors(overlay)
	nextRects := r.layerRects()
	if forceFull {
		req.invalidation = renderInvalidateAllLayers
	}

	scrollOps := make([]gridScrollOp, 0, 2)
	if !forceFull && req.invalidates(renderInvalidateBuffer) {
		if op, ok := r.planBufferScroll(bufferAnchors, overlay, menu); ok {
			r.buffer.Scroll(0, r.buffer.rows, op.delta)
			r.root.Scroll(op.top, op.bottom, op.delta)
			scrollOps = append(scrollOps, op)
		}
	}
	if !forceFull && req.invalidates(renderInvalidateOverlayHistory) {
		if op, ok := r.planOverlayHistoryScroll(overlayAnchors, overlay, menu); ok {
			topPad := overlayTopPadRows(overlay)
			r.hudHistory.Scroll(topPad, topPad+overlayHistoryRows(overlay), op.delta)
			r.root.Scroll(op.top, op.bottom, op.delta)
			scrollOps = append(scrollOps, op)
		}
	}

	if forceFull {
		r.root.invalidate()
	}

	if forceFull || req.invalidates(renderInvalidateBuffer) {
		r.renderBufferGrid(state, overlay, menu, theme, focused)
	}
	if forceFull || req.invalidates(renderInvalidateOverlayHistory) {
		r.renderHUDHistoryGrid(overlay, theme)
	}
	if forceFull || req.invalidates(renderInvalidateOverlayInput) {
		r.renderHUDInputGrid(overlay, theme)
	}
	if forceFull || req.invalidates(renderInvalidateMenu) {
		r.renderMenuGrid(menu, theme)
	}

	rootBuilder := newGridLineBuilder(r.root.cols)
	composeSpans := r.rootComposeSpans(forceFull, prevRects, nextRects)
	composeRootGrid(r.root, rootBuilder, composeSpans, r.buffer, r.hudHistory, r.hudInput, r.menu)

	counted := &countingWriter{w: stdout}
	backend := newTTYRenderBackend(counted, r.palette)
	if forceFull {
		if !r.initialized || r.lastTitle != nextTitle || r.lastTitleDirty != nextTitleDirty {
			backend.SetTitle(nextTitle, nextTitleDirty)
		}
		backend.HideCursor()
		prevTerminal := frameTerminalState{}
		if r.initialized {
			prevTerminal = r.lastTerminal
		}
		backend.SetTerminalState(prevTerminal, nextTerminal)
		backend.ClearAll()
		r.emitDirtyRows(backend, r.root)
		backend.SetCursor(nextCursor)
	} else {
		if r.lastTitle != nextTitle || r.lastTitleDirty != nextTitleDirty {
			backend.SetTitle(nextTitle, nextTitleDirty)
		}
		backend.SetTerminalState(r.lastTerminal, nextTerminal)
		for _, op := range scrollOps {
			backend.ScrollRegion(op.top, op.bottom, op.delta)
		}
		r.emitDirtyRows(backend, r.root)
		if len(scrollOps) > 0 || r.lastCursor != nextCursor {
			backend.SetCursor(nextCursor)
		}
	}
	if err := backend.Flush(); err != nil {
		return err
	}

	r.lastTitle = nextTitle
	r.lastTitleDirty = nextTitleDirty
	r.lastTerminal = nextTerminal
	r.lastCursor = nextCursor
	r.initialized = true
	r.bufferAnchors = bufferAnchors
	r.overlayAnchors = overlayAnchors
	r.clearGridDirty()
	if stats != nil {
		stats.Record(req.class, frameRenderResult{
			full:  forceFull,
			rows:  dirtyRowCount(r.root, scrollOps),
			bytes: counted.count,
		})
	}
	if r.trace != nil {
		r.trace.Printf("grid-render class=%s full=%t invalidation=%d scrollOps=%d bytes=%d", req.class, forceFull, req.invalidation, len(scrollOps), counted.count)
	}
	return nil
}

func (r *gridRenderer) ensureGrids(overlay *overlayState, menu *menuState) bool {
	rootChanged := r.ensureGrid(&r.root, termRows, termCols)
	bufferRows := bufferViewRows(overlay)
	r.ensureGrid(&r.buffer, bufferRows, termCols)
	r.buffer.originRow = 0
	r.buffer.originCol = 0
	r.buffer.visible = true

	historyRows := 0
	if overlay != nil && overlay.visible {
		historyRows = overlayTopPadRows(overlay) + overlayHistoryRows(overlay)
	}
	r.ensureGrid(&r.hudHistory, historyRows, termCols)
	r.hudHistory.originRow = overlayTopRow(overlay)
	r.hudHistory.originCol = 0
	r.hudHistory.visible = overlay != nil && overlay.visible && historyRows > 0

	inputRows := 0
	if overlay != nil && overlay.visible {
		inputRows = overlayPromptRows(overlay) + overlayBottomPadRows(overlay)
	}
	r.ensureGrid(&r.hudInput, inputRows, termCols)
	r.hudInput.originRow = termRows - inputRows
	r.hudInput.originCol = 0
	r.hudInput.visible = overlay != nil && overlay.visible && inputRows > 0

	menuRows, menuCols := 0, 0
	if menu != nil && menu.visible {
		menuRows = menu.height
		menuCols = menu.width
	}
	r.ensureGrid(&r.menu, menuRows, menuCols)
	r.menu.originRow = 0
	r.menu.originCol = 0
	r.menu.visible = false
	if menu != nil && menu.visible && menuRows > 0 && menuCols > 0 {
		r.menu.originRow = menu.y
		r.menu.originCol = menu.x
		r.menu.visible = true
	}
	return rootChanged
}

func (r *gridRenderer) ensureGrid(target **ScreenGrid, rows, cols int) bool {
	if *target == nil {
		*target = newScreenGrid(rows, cols)
		return true
	}
	if (*target).rows == rows && (*target).cols == cols {
		return false
	}
	(*target).Resize(rows, cols)
	return true
}

func (r *gridRenderer) planBufferScroll(nextAnchors []int, overlay *overlayState, menu *menuState) (gridScrollOp, bool) {
	if r.buffer == nil || len(nextAnchors) == 0 {
		return gridScrollOp{}, false
	}
	shift, ok := detectAnchorShift(r.bufferAnchors, nextAnchors)
	if !ok || shift == 0 || menuOverlapsRegion(menu, 0, bufferViewRows(overlay)) {
		return gridScrollOp{}, false
	}
	return gridScrollOp{top: 0, bottom: bufferViewRows(overlay), delta: shift}, true
}

func (r *gridRenderer) planOverlayHistoryScroll(nextAnchors []int, overlay *overlayState, menu *menuState) (gridScrollOp, bool) {
	if overlay == nil || !overlay.visible || overlayHistoryRows(overlay) == 0 {
		return gridScrollOp{}, false
	}
	shift, ok := detectAnchorShift(r.overlayAnchors, nextAnchors)
	if !ok || shift == 0 {
		return gridScrollOp{}, false
	}
	top := overlayTopRow(overlay) + overlayTopPadRows(overlay)
	bottom := top + overlayHistoryRows(overlay)
	if menuOverlapsRegion(menu, top, bottom) {
		return gridScrollOp{}, false
	}
	return gridScrollOp{top: top, bottom: bottom, delta: shift}, true
}

func (r *gridRenderer) renderBufferGrid(state *bufferState, overlay *overlayState, menu *menuState, theme *uiTheme, focused bool) {
	if r.buffer == nil {
		return
	}
	builder := newGridLineBuilder(r.buffer.cols)
	layout := state.visibleLayout(overlay)
	inactive := bufferInactive(overlay, menu, focused)
	for row := 0; row < r.buffer.rows; row++ {
		builder.Start(r.buffer, row)
		if layout != nil && row < len(layout.rows) {
			layoutRow := layout.rows[row]
			renderBufferGridRow(builder, state, layoutRow.start, layoutRow.end, inactive, theme, r.palette)
		}
		builder.Flush()
	}
	r.buffer.valid = true
}

func (r *gridRenderer) renderHUDHistoryGrid(overlay *overlayState, theme *uiTheme) {
	if r.hudHistory == nil {
		return
	}
	builder := newGridLineBuilder(r.hudHistory.cols)
	for row := 0; row < r.hudHistory.rows; row++ {
		builder.Start(r.hudHistory, row)
		builder.Flush()
	}
	if overlay == nil || !overlay.visible || r.hudHistory.rows == 0 {
		r.hudHistory.valid = true
		return
	}
	topPad := overlayTopPadRows(overlay)
	historyRows := overlayHistoryRows(overlay)
	lines := overlay.renderLines(historyRows)
	hudStyle := r.palette.ID(hudPrefix(theme))
	for row := 0; row < topPad; row++ {
		builder.Start(r.hudHistory, row)
		builder.Fill(0, r.hudHistory.cols, gridCell{r: ' ', style: hudStyle})
		builder.Flush()
	}
	for row := 0; row < historyRows; row++ {
		builder.Start(r.hudHistory, topPad+row)
		line := overlayRenderLine{}
		if row < len(lines) {
			line = lines[row]
		}
		renderOverlayGridLine(builder, line, overlay, theme, r.palette)
		builder.Flush()
	}
	r.hudHistory.valid = true
}

func (r *gridRenderer) renderHUDInputGrid(overlay *overlayState, theme *uiTheme) {
	if r.hudInput == nil || r.hudInput.rows == 0 {
		return
	}
	builder := newGridLineBuilder(r.hudInput.cols)
	for row := 0; row < r.hudInput.rows; row++ {
		builder.Start(r.hudInput, row)
		if row == 0 && overlay != nil && overlay.visible && overlayPromptRows(overlay) > 0 {
			renderOverlayPromptGridRow(builder, overlay, theme, r.palette)
		} else {
			builder.Fill(0, r.hudInput.cols, gridCell{r: ' ', style: r.palette.ID(hudPrefix(theme))})
		}
		builder.Flush()
	}
	r.hudInput.valid = true
}

func (r *gridRenderer) renderMenuGrid(menu *menuState, theme *uiTheme) {
	if r.menu == nil {
		return
	}
	builder := newGridLineBuilder(r.menu.cols)
	for row := 0; row < r.menu.rows; row++ {
		builder.Start(r.menu, row)
		builder.Flush()
	}
	if menu == nil || !menu.visible || r.menu.rows == 0 || r.menu.cols == 0 {
		r.menu.valid = true
		return
	}
	inner := menu.width - 2
	row := 0
	builder.Start(r.menu, row)
	renderGridText(builder, 0, formatMenuBorder(menu.title, inner, '╭', '╮', '─'), menuBorderStyle(theme), 0, r.palette)
	builder.Flush()
	row++
	for i, item := range menu.items {
		if row >= r.menu.rows {
			break
		}
		builder.Start(r.menu, row)
		renderGridText(builder, 0, formatMenuItemLine(item, inner), menuItemStyle(theme, item.current, menu.hover == i), 0, r.palette)
		builder.Flush()
		row++
		if item.sepAfter && i < len(menu.items)-1 && row < r.menu.rows {
			builder.Start(r.menu, row)
			renderGridText(builder, 0, formatMenuBorder("", inner, '├', '┤', '─'), menuBorderStyle(theme), 0, r.palette)
			builder.Flush()
			row++
		}
	}
	if row < r.menu.rows {
		builder.Start(r.menu, row)
		renderGridText(builder, 0, formatMenuBorder("", inner, '╰', '╯', '─'), menuBorderStyle(theme), 0, r.palette)
		builder.Flush()
	}
	r.menu.valid = true
}

func (r *gridRenderer) emitDirtyRows(backend renderBackend, grid *ScreenGrid) {
	if backend == nil || grid == nil {
		return
	}
	for row := 0; row < grid.rows; row++ {
		span := grid.dirty[row]
		if span.start >= span.end {
			continue
		}
		cells := grid.rowCells(row)
		backend.WriteCells(row, span.start, cells[span.start:span.end])
	}
}

func (r *gridRenderer) rootComposeSpans(forceFull bool, prevRects, nextRects []gridRect) []gridDirtySpan {
	if r == nil || r.root == nil {
		return nil
	}
	spans := make([]gridDirtySpan, r.root.rows)
	for row := range spans {
		spans[row] = gridDirtySpan{start: r.root.cols}
	}
	if forceFull {
		for row := range spans {
			spans[row] = gridDirtySpan{start: 0, end: r.root.cols}
		}
		return spans
	}
	projectGridDirtySpans(r.root, spans)
	for _, layer := range []*ScreenGrid{r.buffer, r.hudHistory, r.hudInput, r.menu} {
		projectLayerDirtySpans(r.root, layer, spans)
	}
	for _, rect := range prevRects {
		markRect(spans, rect)
	}
	for _, rect := range nextRects {
		markRect(spans, rect)
	}
	return spans
}

func (r *gridRenderer) clearGridDirty() {
	for _, grid := range []*ScreenGrid{r.root, r.buffer, r.hudHistory, r.hudInput, r.menu} {
		if grid == nil {
			continue
		}
		grid.clearDirty()
		grid.valid = true
	}
}

func renderBufferGridRow(builder *GridLineBuilder, state *bufferState, start, end int, inactive bool, theme *uiTheme, palette *gridStylePalette) {
	if builder == nil || state == nil {
		return
	}
	col := 0
	collapsedPos, collapsedCol, collapsedVisible := collapsedInactiveSelection(state, inactive, start)
	collapsedPainted := false
	for pos := start; pos < end && col < len(builder.cells); pos++ {
		style := gridStyleID(0)
		if collapsedVisible {
			switch {
			case collapsedCol >= len(builder.cells):
				if pos == end-1 {
					style = palette.ID(highlightPrefix(theme, true))
					collapsedPainted = true
				}
			case pos == collapsedPos:
				style = palette.ID(highlightPrefix(theme, true))
				collapsedPainted = true
			}
		} else if !state.flashSelection && pos >= state.dotStart && pos < state.dotEnd {
			style = palette.ID(highlightPrefix(theme, false))
		}
		col = builder.PutRune(col, state.text[pos], style, bufferTabWidth)
	}
	if collapsedVisible && !collapsedPainted && collapsedCol == col && col < len(builder.cells) {
		builder.PutCell(col, gridCell{r: ' ', style: palette.ID(highlightPrefix(theme, true))})
	}
}

func renderOverlayGridLine(builder *GridLineBuilder, line overlayRenderLine, overlay *overlayState, theme *uiTheme, palette *gridStylePalette) {
	if builder == nil {
		return
	}
	if line.history < 0 {
		style := palette.ID(hudPrefix(theme))
		builder.Fill(0, len(builder.cells), gridCell{r: ' ', style: style})
		renderGridText(builder, 0, line.text, hudPrefix(theme), hudTabWidth, palette)
		return
	}
	start, end, ok := overlay.selectionBounds()
	selStart := 0
	selEnd := 0
	contentOffset := line.offset
	if ok && !overlay.flashSelection && line.history >= start.line && line.history <= end.line {
		if line.command {
			selStart = 0
		} else {
			selStart = contentOffset
		}
		selEnd = len([]rune(line.text))
	}
	if ok && !overlay.flashSelection && line.history == start.line {
		selStart = start.col + contentOffset
	}
	if ok && !overlay.flashSelection && line.history == end.line {
		selEnd = end.col + contentOffset
	}
	if selStart < contentOffset {
		selStart = contentOffset
	}
	if selEnd < selStart {
		selEnd = selStart
	}
	baseStyle := palette.ID(overlayLinePrefix(theme, line.command))
	if baseStyle != 0 {
		builder.Fill(0, len(builder.cells), gridCell{r: ' ', style: baseStyle})
	}
	col := 0
	runes := []rune(line.text)
	for i, glyph := range runes {
		if col >= len(builder.cells) {
			break
		}
		style := baseStyle
		drawRune := glyph
		switch {
		case line.running:
			style = palette.ID(shimmerPrefix(theme, i, len(runes)))
		case i >= selStart && i < selEnd:
			style = palette.ID(highlightPrefix(theme, false))
		case theme != nil && i == 0 && glyph == '█':
			style = palette.ID(theme.outputPrefix())
			drawRune = ' '
		}
		col = builder.PutRune(col, drawRune, style, hudTabWidth)
	}
}

func renderOverlayPromptGridRow(builder *GridLineBuilder, overlay *overlayState, theme *uiTheme, palette *gridStylePalette) {
	if builder == nil {
		return
	}
	style := palette.ID(hudPrefix(theme))
	builder.Fill(0, len(builder.cells), gridCell{r: ' ', style: style})
	if overlay == nil {
		return
	}
	renderGridRunes(builder, 0, overlay.input, hudPrefix(theme), hudTabWidth, palette)
}

func renderGridText(builder *GridLineBuilder, col int, text, style string, tabWidth int, palette *gridStylePalette) int {
	return renderGridRunes(builder, col, []rune(text), style, tabWidth, palette)
}

func renderGridRunes(builder *GridLineBuilder, col int, runes []rune, style string, tabWidth int, palette *gridStylePalette) int {
	styleID := gridStyleID(0)
	if palette != nil {
		styleID = palette.ID(style)
	}
	for _, glyph := range runes {
		if col >= len(builder.cells) {
			break
		}
		col = builder.PutRune(col, glyph, styleID, tabWidth)
	}
	return col
}

func currentBufferAnchors(state *bufferState, overlay *overlayState, rows int) []int {
	anchors := make([]int, rows)
	for i := range anchors {
		anchors[i] = -1
	}
	if state == nil {
		return anchors
	}
	layout := state.visibleLayout(overlay)
	for row := 0; layout != nil && row < len(layout.rows) && row < len(anchors); row++ {
		anchors[row] = layout.rows[row].start
	}
	return anchors
}

func currentOverlayAnchors(overlay *overlayState) []int {
	rows := overlayHistoryRows(overlay)
	anchors := make([]int, rows)
	for i := range anchors {
		anchors[i] = -1
	}
	if overlay == nil || !overlay.visible || rows == 0 {
		return anchors
	}
	lines := overlay.renderLines(rows)
	for row := 0; row < rows && row < len(lines); row++ {
		anchors[row] = lines[row].history
	}
	return anchors
}

func detectAnchorShift(prev, next []int) (int, bool) {
	if len(prev) != len(next) || len(prev) <= 1 {
		return 0, false
	}
	rows := len(prev)
	for shift := 1; shift < rows; shift++ {
		match := true
		for row := 0; row < rows-shift; row++ {
			if prev[row+shift] != next[row] {
				match = false
				break
			}
		}
		if match {
			return shift, true
		}
	}
	for shift := 1; shift < rows; shift++ {
		match := true
		for row := 0; row < rows-shift; row++ {
			if prev[row] != next[row+shift] {
				match = false
				break
			}
		}
		if match {
			return -shift, true
		}
	}
	return 0, false
}

func menuOverlapsRegion(menu *menuState, top, bottom int) bool {
	if menu == nil || !menu.visible || top >= bottom {
		return false
	}
	return menu.y < bottom && menu.y+menu.height > top
}

func dirtyRowCount(grid *ScreenGrid, scrollOps []gridScrollOp) int {
	if grid == nil {
		return 0
	}
	count := 0
	for _, span := range grid.dirty {
		if span.start < span.end {
			count++
		}
	}
	for _, op := range scrollOps {
		count += abs(op.delta)
	}
	return count
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (r *gridRenderer) layerRects() []gridRect {
	return []gridRect{
		visibleGridRect(r.buffer),
		visibleGridRect(r.hudHistory),
		visibleGridRect(r.hudInput),
		visibleGridRect(r.menu),
	}
}

func hudPrefix(theme *uiTheme) string {
	if theme == nil {
		return ""
	}
	return theme.hudPrefix()
}
