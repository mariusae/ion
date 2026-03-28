package term

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strings"
)

type terminalFrame struct {
	title      string
	titleDirty bool
	rows       []frameRow
	cursor     frameCursor
	terminal   frameTerminalState
}

type frameRow struct {
	id    frameRowID
	cells []frameCell
}

type frameCell struct {
	r     rune
	style string
}

type frameCursor struct {
	visible bool
	row     int
	col     int
}

type frameRenderer struct {
	last        *terminalFrame
	initialized bool
	trace       *traceLogger
}

type frameRowID struct {
	kind   frameRowKind
	anchor int
}

type frameRowKind uint8

const (
	frameRowKindNone frameRowKind = iota
	frameRowKindBuffer
	frameRowKindOverlay
	frameRowKindOverlayHistory
	frameRowKindStatus
	frameRowKindMenu
)

type frameTerminalState struct {
	altScreen      bool
	mousePress     bool
	mouseMotion    bool
	focusReporting bool
	mouseSGR       bool
	bracketedPaste bool
	cursorShape    frameCursorShape
}

type frameCursorShape int

const (
	frameCursorShapeBlock frameCursorShape = iota
	frameCursorShapeBar
)

type redrawClass string

const (
	redrawInitial         redrawClass = "initial"
	redrawBufferCursor    redrawClass = "buffer_cursor"
	redrawBufferSelection redrawClass = "buffer_selection"
	redrawBufferViewport  redrawClass = "buffer_viewport"
	redrawBufferContent   redrawClass = "buffer_content"
	redrawBufferStatus    redrawClass = "buffer_status"
	redrawOverlayInput    redrawClass = "overlay_input"
	redrawOverlayHistory  redrawClass = "overlay_history"
	redrawOverlayOpen     redrawClass = "overlay_open"
	redrawOverlayClose    redrawClass = "overlay_close"
	redrawMenuHover       redrawClass = "menu_hover"
	redrawMenuOpen        redrawClass = "menu_open"
	redrawMenuClose       redrawClass = "menu_close"
	redrawTheme           redrawClass = "theme"
	redrawRefresh         redrawClass = "refresh"
	redrawResize          redrawClass = "resize"
	redrawRecover         redrawClass = "recover"
)

type frameRenderStats struct {
	enabled bool
	out     io.Writer
	counts  map[redrawClass]*frameRenderAggregate
}

type frameRenderAggregate struct {
	renders int
	full    int
	diff    int
	rows    int
	bytes   int
}

type frameRenderResult struct {
	full  bool
	rows  int
	bytes int
}

type frameRowEditKind uint8

const (
	frameRowEditNone frameRowEditKind = iota
	frameRowEditReplace
	frameRowEditInsert
	frameRowEditDelete
)

type frameRowEdit struct {
	kind  frameRowEditKind
	start int
	end   int
	count int
}

type countingWriter struct {
	w     io.Writer
	count int
}

func newTerminalFrame(rows, cols int) *terminalFrame {
	frame := &terminalFrame{
		rows:     make([]frameRow, rows),
		terminal: defaultFrameTerminalState(),
	}
	for row := range frame.rows {
		frame.rows[row].cells = make([]frameCell, cols)
		for col := range frame.rows[row].cells {
			frame.rows[row].cells[col] = frameCell{r: ' '}
		}
	}
	return frame
}

func newFrameRenderer() *frameRenderer {
	return &frameRenderer{trace: newTraceLogger()}
}

func defaultFrameTerminalState() frameTerminalState {
	return frameTerminalState{
		altScreen:      true,
		mousePress:     true,
		mouseMotion:    true,
		focusReporting: true,
		mouseSGR:       true,
		bracketedPaste: true,
		cursorShape:    frameCursorShapeBar,
	}
}

func newFrameRenderStats(out io.Writer) *frameRenderStats {
	return &frameRenderStats{
		enabled: strings.TrimSpace(os.Getenv("ION_RENDER_TRACE")) != "",
		out:     out,
		counts:  make(map[redrawClass]*frameRenderAggregate),
	}
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.count += n
	return n, err
}

func (s *frameRenderStats) Record(class redrawClass, result frameRenderResult) {
	if s == nil || !s.enabled {
		return
	}
	agg := s.counts[class]
	if agg == nil {
		agg = &frameRenderAggregate{}
		s.counts[class] = agg
	}
	agg.renders++
	if result.full {
		agg.full++
	} else {
		agg.diff++
	}
	agg.rows += result.rows
	agg.bytes += result.bytes
}

func (s *frameRenderStats) Report() error {
	if s == nil || !s.enabled || s.out == nil {
		return nil
	}
	order := []redrawClass{
		redrawInitial,
		redrawRecover,
		redrawResize,
		redrawRefresh,
		redrawBufferCursor,
		redrawBufferSelection,
		redrawBufferViewport,
		redrawBufferContent,
		redrawBufferStatus,
		redrawOverlayOpen,
		redrawOverlayInput,
		redrawOverlayHistory,
		redrawOverlayClose,
		redrawMenuOpen,
		redrawMenuHover,
		redrawMenuClose,
		redrawTheme,
	}
	if _, err := io.WriteString(s.out, "ion: render stats\n"); err != nil {
		return err
	}
	for _, class := range order {
		agg := s.counts[class]
		if agg == nil || agg.renders == 0 {
			continue
		}
		if _, err := fmt.Fprintf(s.out, "  %s: renders=%d full=%d diff=%d rows=%d bytes=%d\n", class, agg.renders, agg.full, agg.diff, agg.rows, agg.bytes); err != nil {
			return err
		}
	}
	return nil
}

func (r *frameRenderer) Reset() {
	if r == nil {
		return
	}
	r.last = nil
	r.initialized = false
}

func (r *frameRenderer) Recover(stdout io.Writer, frame *terminalFrame, class redrawClass, stats *frameRenderStats) error {
	if r == nil {
		return writeFullFrame(stdout, frame)
	}
	if frame == nil {
		return nil
	}
	if r.trace != nil {
		rows := 0
		prevSig := uint64(0)
		nextSig := frameSignature(frame)
		if frame != nil {
			rows = len(frame.rows)
		}
		if r.last != nil {
			prevSig = frameSignature(r.last)
		}
		r.trace.Printf("render recover class=%s initialized=%t rows=%d prevSig=%x nextSig=%x", class, r.initialized, rows, prevSig, nextSig)
	}
	counted := &countingWriter{w: stdout}
	result := frameRenderResult{}
	if !r.initialized || r.last == nil {
		if err := writeFullFrame(counted, frame); err != nil {
			return err
		}
	} else {
		if err := writeRecoveredFrame(counted, r.last, frame); err != nil {
			return err
		}
	}
	result = frameRenderResult{full: true, rows: len(frame.rows), bytes: counted.count}
	r.last = cloneTerminalFrame(frame)
	r.initialized = true
	if r.trace != nil {
		r.trace.Printf("render recover done class=%s bytes=%d nextSig=%x", class, counted.count, frameSignature(frame))
	}
	if stats != nil {
		stats.Record(class, result)
	}
	return nil
}

func (r *frameRenderer) Render(stdout io.Writer, frame *terminalFrame, class redrawClass, forceFull bool, stats *frameRenderStats) error {
	if frame == nil {
		return nil
	}
	if r == nil {
		return writeFullFrame(stdout, frame)
	}
	counted := &countingWriter{w: stdout}
	result := frameRenderResult{}
	if forceFull || !r.initialized || !sameFrameGeometry(r.last, frame) {
		if r.trace != nil {
			prevSig := uint64(0)
			if r.last != nil {
				prevSig = frameSignature(r.last)
			}
			r.trace.Printf("render full class=%s force=%t initialized=%t sameGeometry=%t prevSig=%x nextSig=%x", class, forceFull, r.initialized, sameFrameGeometry(r.last, frame), prevSig, frameSignature(frame))
		}
		if err := writeFullFrame(counted, frame); err != nil {
			return err
		}
		result = frameRenderResult{full: true, rows: len(frame.rows), bytes: counted.count}
		r.last = cloneTerminalFrame(frame)
		r.initialized = true
		if stats != nil {
			stats.Record(class, result)
		}
		return nil
	}
	changedRows := changedFrameRows(r.last, frame)
	if r.trace != nil {
		r.trace.Printf("render diff class=%s changedRows=%d cursorChanged=%t prevSig=%x nextSig=%x", class, len(changedRows), r.last.cursor != frame.cursor, frameSignature(r.last), frameSignature(frame))
	}
	scrollRows := changedRows
	switch class {
	case redrawBufferViewport:
		if _, _, _, exposed, ok := detectFrameRegionRowShift(r.last, frame, frameRowKindBuffer); ok {
			scrollRows = exposed
		}
	case redrawOverlayHistory:
		if _, _, _, exposed, ok := detectFrameRegionRowShift(r.last, frame, frameRowKindOverlayHistory); ok {
			scrollRows = exposed
		}
	}
	if err := writeFrameDiff(counted, r.last, frame); err != nil {
		return err
	}
	result = frameRenderResult{rows: len(scrollRows), bytes: counted.count}
	r.last = cloneTerminalFrame(frame)
	if r.trace != nil {
		r.trace.Printf("render diff done class=%s bytes=%d nextSig=%x", class, counted.count, frameSignature(frame))
	}
	if stats != nil {
		stats.Record(class, result)
	}
	return nil
}

func frameSignature(frame *terminalFrame) uint64 {
	if frame == nil {
		return 0
	}
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s|%t|%t|%d|%d|%t|%t|%t|%t|%t|%d|%d|%d|", frame.title, frame.titleDirty, frame.cursor.visible, frame.cursor.row, frame.cursor.col, frame.terminal.altScreen, frame.terminal.mousePress, frame.terminal.mouseMotion, frame.terminal.focusReporting, frame.terminal.mouseSGR, frame.terminal.cursorShape, len(frame.rows), len(frame.rows))
	for _, row := range frame.rows {
		_, _ = fmt.Fprintf(h, "%d|%d|", row.id.kind, row.id.anchor)
		for _, cell := range row.cells {
			_, _ = fmt.Fprintf(h, "%c|%s|", cell.r, cell.style)
		}
	}
	return h.Sum64()
}

func buildBufferFrame(state *bufferState, overlay *overlayState, menu *menuState, theme *uiTheme, focused bool) *terminalFrame {
	if state == nil {
		return nil
	}
	frame := newTerminalFrame(termRows, termCols)
	frame.title = state.name
	frame.titleDirty = state.dirty

	layout := state.visibleLayout(overlay)
	inactive := bufferInactive(overlay, menu, focused)
	for row := 0; layout != nil && row < len(layout.rows) && row < len(frame.rows); row++ {
		layoutRow := layout.rows[row]
		frame.rows[row].id = frameRowID{kind: frameRowKindBuffer, anchor: layoutRow.start}
		renderBufferFrameRow(frame.rows[row].cells, state, layoutRow.start, layoutRow.end, inactive, theme)
	}

	if overlay != nil && overlay.visible {
		renderOverlayFrame(frame, overlay, theme)
	} else if state.status != "" {
		renderInlineStatusFrame(frame, state.status, theme)
	}

	renderMenuFrame(frame, menu, theme)
	frame.cursor = buildFrameCursor(state, overlay, menu, focused)
	return frame
}

func renderOverlayFrame(frame *terminalFrame, overlay *overlayState, theme *uiTheme) {
	if frame == nil || overlay == nil || !overlay.visible {
		return
	}
	topRow := overlayTopRow(overlay)
	historyRows := overlayHistoryRows(overlay)
	lines := overlay.renderLines(historyRows)

	for row := 0; row < overlayTopPadRows(overlay); row++ {
		frame.rows[topRow+row].id = frameRowID{kind: frameRowKindOverlay, anchor: topRow + row}
		renderFilledFrameRow(frame.rows[topRow+row].cells, theme.hudPrefix())
	}

	startRow := topRow + overlayTopPadRows(overlay)
	for row := 0; row < historyRows; row++ {
		line := overlayRenderLine{}
		if row < len(lines) {
			line = lines[row]
		}
		frame.rows[startRow+row].id = overlayHistoryFrameRowID(row, line)
		renderOverlayFrameLine(frame.rows[startRow+row].cells, line, overlay, theme)
	}

	if overlayPromptRows(overlay) > 0 {
		promptRow := termRows - 1 - overlayBottomPadRows(overlay)
		frame.rows[promptRow].id = frameRowID{kind: frameRowKindOverlay, anchor: promptRow}
		renderOverlayPromptFrameRow(frame.rows[promptRow].cells, overlay, theme)
	}

	bottomStart := topRow + overlayTopPadRows(overlay) + historyRows + overlayPromptRows(overlay)
	for row := 0; row < overlayBottomPadRows(overlay); row++ {
		frame.rows[bottomStart+row].id = frameRowID{kind: frameRowKindOverlay, anchor: bottomStart + row}
		renderFilledFrameRow(frame.rows[bottomStart+row].cells, theme.hudPrefix())
	}
}

func renderInlineStatusFrame(frame *terminalFrame, status string, theme *uiTheme) {
	if frame == nil || status == "" || len(frame.rows) == 0 {
		return
	}
	frame.rows[len(frame.rows)-1].id = frameRowID{kind: frameRowKindStatus, anchor: len(frame.rows) - 1}
	runes := []rune(status)
	if len(runes) > termCols {
		runes = runes[:termCols]
	}
	renderTextFrameRow(frame.rows[len(frame.rows)-1].cells, 0, string(runes), theme.subtlePrefix(), 0)
}

func renderMenuFrame(frame *terminalFrame, menu *menuState, theme *uiTheme) {
	if frame == nil || menu == nil || !menu.visible {
		return
	}
	inner := menu.width - 2
	row := menu.y
	if row < 0 || row >= len(frame.rows) {
		return
	}
	frame.rows[row].id = frameRowID{kind: frameRowKindMenu, anchor: row}
	renderFrameRowText(frame, row, menu.x, formatMenuBorder(menu.title, inner, '╭', '╮', '─'), frameMenuBorderStyle(theme), 0)
	row++
	for i, item := range menu.items {
		if row >= len(frame.rows) {
			return
		}
		frame.rows[row].id = frameRowID{kind: frameRowKindMenu, anchor: row}
		renderFrameRowText(frame, row, menu.x, formatMenuItemLine(item, inner), frameMenuItemStyle(theme, item.current, menu.hover == i), 0)
		row++
		if item.sepAfter && i < len(menu.items)-1 {
			if row >= len(frame.rows) {
				return
			}
			frame.rows[row].id = frameRowID{kind: frameRowKindMenu, anchor: row}
			renderFrameRowText(frame, row, menu.x, formatMenuBorder("", inner, '├', '┤', '─'), frameMenuBorderStyle(theme), 0)
			row++
		}
	}
	if row >= len(frame.rows) {
		return
	}
	frame.rows[row].id = frameRowID{kind: frameRowKindMenu, anchor: row}
	renderFrameRowText(frame, row, menu.x, formatMenuBorder("", inner, '╰', '╯', '─'), frameMenuBorderStyle(theme), 0)
}

func buildFrameCursor(state *bufferState, overlay *overlayState, menu *menuState, focused bool) frameCursor {
	if overlay != nil && overlay.visible && overlay.running {
		return frameCursor{}
	}
	if !focused || (menu != nil && menu.visible) {
		return frameCursor{}
	}
	row, col := terminalCursorPosition(state, overlay)
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	if row >= termRows {
		row = termRows - 1
	}
	if col >= termCols {
		col = termCols - 1
	}
	return frameCursor{
		visible: true,
		row:     row,
		col:     col,
	}
}

func writeFullFrame(stdout io.Writer, frame *terminalFrame) error {
	if frame == nil {
		return nil
	}
	if _, err := io.WriteString(stdout, bufferWindowTitleSequence(frame.title, frame.titleDirty)); err != nil {
		return err
	}
	prevTerminal := frameTerminalState{}
	if frame.terminal.altScreen {
		if _, err := io.WriteString(stdout, "\x1b[?1049h"); err != nil {
			return err
		}
		prevTerminal.altScreen = true
	}
	if _, err := io.WriteString(stdout, "\x1b[?25l"); err != nil {
		return err
	}
	if err := writeFrameTerminalState(stdout, prevTerminal, frame.terminal); err != nil {
		return err
	}
	if _, err := io.WriteString(stdout, "\x1b[2J"); err != nil {
		return err
	}
	for row := range frame.rows {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
			return err
		}
		if err := writeFrameRow(stdout, frame.rows[row]); err != nil {
			return err
		}
	}
	return writeFrameCursor(stdout, frame.cursor)
}

func writeRecoveredFrame(stdout io.Writer, prev, next *terminalFrame) error {
	if next == nil {
		return nil
	}
	prevTerminal := frameTerminalState{}
	if prev != nil {
		prevTerminal = prev.terminal
		if prev.title != next.title || prev.titleDirty != next.titleDirty {
			if _, err := io.WriteString(stdout, bufferWindowTitleSequence(next.title, next.titleDirty)); err != nil {
				return err
			}
		}
	} else {
		if _, err := io.WriteString(stdout, bufferWindowTitleSequence(next.title, next.titleDirty)); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(stdout, "\x1b[?25l"); err != nil {
		return err
	}
	if err := writeFrameTerminalState(stdout, prevTerminal, next.terminal); err != nil {
		return err
	}
	if _, err := io.WriteString(stdout, "\x1b[2J"); err != nil {
		return err
	}
	for row := range next.rows {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
			return err
		}
		if err := writeFrameRow(stdout, next.rows[row]); err != nil {
			return err
		}
	}
	return writeFrameCursor(stdout, next.cursor)
}

func writeFrameDiff(stdout io.Writer, prev, next *terminalFrame) error {
	if next == nil {
		return nil
	}
	if !sameFrameGeometry(prev, next) {
		return writeFullFrame(stdout, next)
	}

	if prev.title != next.title || prev.titleDirty != next.titleDirty {
		if _, err := io.WriteString(stdout, bufferWindowTitleSequence(next.title, next.titleDirty)); err != nil {
			return err
		}
	}
	if err := writeFrameTerminalState(stdout, prev.terminal, next.terminal); err != nil {
		return err
	}

	changedRows := changedFrameRows(prev, next)
	usedRowShift := false
	if top, bottom, shift, exposedRows, ok := detectFrameRegionRowShift(prev, next, frameRowKindBuffer); ok {
		if err := writeFrameRegionShift(stdout, next, top, bottom, shift, exposedRows); err != nil {
			return err
		}
		changedRows = exposedRows
		usedRowShift = true
	} else if top, bottom, shift, exposedRows, ok := detectFrameRegionRowShift(prev, next, frameRowKindOverlayHistory); ok {
		if err := writeFrameRegionShift(stdout, next, top, bottom, shift, exposedRows); err != nil {
			return err
		}
		changedRows = exposedRows
		usedRowShift = true
	}

	cursorChanged := prev.cursor != next.cursor
	terminalChanged := prev.terminal != next.terminal
	if len(changedRows) == 0 && !cursorChanged && !terminalChanged && prev.title == next.title && prev.titleDirty == next.titleDirty {
		return nil
	}

	if len(changedRows) > 0 && !usedRowShift {
		if _, err := io.WriteString(stdout, "\x1b[?25l"); err != nil {
			return err
		}
		for _, row := range changedRows {
			if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
				return err
			}
			if err := writeFrameRow(stdout, next.rows[row]); err != nil {
				return err
			}
		}
	}
	if len(changedRows) > 0 || cursorChanged {
		if err := writeFrameCursor(stdout, next.cursor); err != nil {
			return err
		}
	}
	return nil
}

func writeFrameRow(stdout io.Writer, row frameRow) error {
	last := -1
	for i, cell := range row.cells {
		if cell.r != ' ' || cell.style != "" {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	currentStyle := ""
	for i := 0; i <= last; i++ {
		cell := row.cells[i]
		if cell.style != currentStyle {
			if cell.style == "" {
				if _, err := io.WriteString(stdout, styleReset()); err != nil {
					return err
				}
			} else {
				if _, err := io.WriteString(stdout, cell.style); err != nil {
					return err
				}
			}
			currentStyle = cell.style
		}
		if _, err := io.WriteString(stdout, string(cell.r)); err != nil {
			return err
		}
	}
	if currentStyle != "" {
		if _, err := io.WriteString(stdout, styleReset()); err != nil {
			return err
		}
	}
	return nil
}

func cloneTerminalFrame(frame *terminalFrame) *terminalFrame {
	if frame == nil {
		return nil
	}
	clone := &terminalFrame{
		title:      frame.title,
		titleDirty: frame.titleDirty,
		rows:       make([]frameRow, len(frame.rows)),
		cursor:     frame.cursor,
		terminal:   frame.terminal,
	}
	for i, row := range frame.rows {
		clone.rows[i].id = row.id
		clone.rows[i].cells = append([]frameCell(nil), row.cells...)
	}
	return clone
}

func sameFrameGeometry(a, b *terminalFrame) bool {
	if a == nil || b == nil {
		return false
	}
	if len(a.rows) != len(b.rows) {
		return false
	}
	for i := range a.rows {
		if len(a.rows[i].cells) != len(b.rows[i].cells) {
			return false
		}
	}
	return true
}

func sameFrameCells(a, b frameRow) bool {
	if len(a.cells) != len(b.cells) {
		return false
	}
	for i := range a.cells {
		if a.cells[i] != b.cells[i] {
			return false
		}
	}
	return true
}

func sameFrameRow(a, b frameRow) bool {
	return a.id == b.id && sameFrameCells(a, b)
}

func changedFrameRows(prev, next *terminalFrame) []int {
	if !sameFrameGeometry(prev, next) {
		return nil
	}
	changed := make([]int, 0, len(next.rows))
	for row := range next.rows {
		if !sameFrameRow(prev.rows[row], next.rows[row]) {
			changed = append(changed, row)
		}
	}
	return changed
}

func overlayHistoryFrameRowID(row int, line overlayRenderLine) frameRowID {
	anchor := -1 - row
	if line.history >= 0 {
		anchor = line.history
	}
	return frameRowID{kind: frameRowKindOverlayHistory, anchor: anchor}
}

func detectFrameRegionRowShift(prev, next *terminalFrame, kind frameRowKind) (int, int, int, []int, bool) {
	top, bottom, ok := findFrameRowKindRegion(prev, next, kind)
	if !ok {
		return 0, 0, 0, nil, false
	}
	shift, exposed, ok := detectRowShiftWithinRegion(prev, next, top, bottom)
	if !ok {
		return 0, 0, 0, nil, false
	}
	return top, bottom, shift, exposed, true
}

func findFrameRowKindRegion(prev, next *terminalFrame, kind frameRowKind) (int, int, bool) {
	if !sameFrameGeometry(prev, next) || len(prev.rows) == 0 {
		return 0, 0, false
	}
	top := -1
	bottom := -1
	for i := range prev.rows {
		prevMatch := prev.rows[i].id.kind == kind
		nextMatch := next.rows[i].id.kind == kind
		if prevMatch != nextMatch {
			return 0, 0, false
		}
		if !prevMatch {
			if top >= 0 && bottom < 0 {
				bottom = i
			}
			continue
		}
		if top < 0 {
			top = i
			continue
		}
		if bottom >= 0 {
			return 0, 0, false
		}
	}
	if top < 0 {
		return 0, 0, false
	}
	if bottom < 0 {
		bottom = len(prev.rows)
	}
	return top, bottom, true
}

func detectRowShiftWithinRegion(prev, next *terminalFrame, top, bottom int) (int, []int, bool) {
	rows := bottom - top
	if rows <= 1 {
		return 0, nil, false
	}
	for shift := 1; shift < rows; shift++ {
		match := true
		for row := 0; row < rows-shift; row++ {
			if prev.rows[top+row+shift].id != next.rows[top+row].id || !sameFrameCells(prev.rows[top+row+shift], next.rows[top+row]) {
				match = false
				break
			}
		}
		if match {
			exposed := make([]int, 0, shift)
			for row := rows - shift; row < rows; row++ {
				exposed = append(exposed, top+row)
			}
			return shift, exposed, true
		}
	}
	for shift := 1; shift < rows; shift++ {
		match := true
		for row := 0; row < rows-shift; row++ {
			if prev.rows[top+row].id != next.rows[top+row+shift].id || !sameFrameCells(prev.rows[top+row], next.rows[top+row+shift]) {
				match = false
				break
			}
		}
		if match {
			exposed := make([]int, 0, shift)
			for row := 0; row < shift; row++ {
				exposed = append(exposed, top+row)
			}
			return -shift, exposed, true
		}
	}
	return 0, nil, false
}

func detectViewportRowShift(prev, next *terminalFrame) (int, []int, bool) {
	_, _, shift, exposed, ok := detectFrameRegionRowShift(prev, next, frameRowKindBuffer)
	return shift, exposed, ok
}

func writeFrameRegionShift(stdout io.Writer, next *terminalFrame, top, bottom, shift int, exposedRows []int) error {
	if shift == 0 || len(exposedRows) == 0 {
		return nil
	}
	if _, err := io.WriteString(stdout, "\x1b[?25l"); err != nil {
		return err
	}
	fullHeight := next != nil && top == 0 && bottom == len(next.rows)
	if !fullHeight {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;%dr", top+1, bottom); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H", top+1); err != nil {
		return err
	}
	op := 'L'
	count := -shift
	if shift > 0 {
		op = 'M'
		count = shift
	}
	if _, err := fmt.Fprintf(stdout, "\x1b[%d%c", count, op); err != nil {
		return err
	}
	if !fullHeight {
		if _, err := io.WriteString(stdout, "\x1b[r"); err != nil {
			return err
		}
	}
	for _, row := range exposedRows {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
			return err
		}
		if err := writeFrameRow(stdout, next.rows[row]); err != nil {
			return err
		}
	}
	return nil
}

func detectFrameRowEdit(prev, next frameRow) (frameRowEdit, bool) {
	if prev.id != next.id || prev.id.kind != frameRowKindBuffer || len(prev.cells) != len(next.cells) {
		return frameRowEdit{}, false
	}
	start := 0
	for start < len(prev.cells) && prev.cells[start] == next.cells[start] {
		start++
	}
	if start == len(prev.cells) {
		return frameRowEdit{}, false
	}
	end := len(prev.cells)
	for end > start && prev.cells[end-1] == next.cells[end-1] {
		end--
	}
	maxCount := end - start
	if count, ok := detectFrameRowInsert(prev.cells, next.cells, start, maxCount); ok {
		return frameRowEdit{kind: frameRowEditInsert, start: start, count: count}, true
	}
	if count, ok := detectFrameRowDelete(prev.cells, next.cells, start, maxCount); ok {
		return frameRowEdit{kind: frameRowEditDelete, start: start, count: count}, true
	}
	return frameRowEdit{kind: frameRowEditReplace, start: start, end: end}, true
}

func detectFrameRowInsert(prev, next []frameCell, start, maxCount int) (int, bool) {
	for count := 1; count <= maxCount && start+count <= len(prev); count++ {
		if sameFrameCellSlice(prev[start:len(prev)-count], next[start+count:]) {
			return count, true
		}
	}
	return 0, false
}

func detectFrameRowDelete(prev, next []frameCell, start, maxCount int) (int, bool) {
	for count := 1; count <= maxCount && start+count <= len(prev); count++ {
		if sameFrameCellSlice(prev[start+count:], next[start:len(next)-count]) {
			return count, true
		}
	}
	return 0, false
}

func sameFrameCellSlice(a, b []frameCell) bool {
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

func writeFrameRowEdit(stdout io.Writer, rowIndex int, row frameRow, edit frameRowEdit) error {
	if edit.kind == frameRowEditNone {
		return nil
	}
	if _, err := io.WriteString(stdout, "\x1b[?25l"); err != nil {
		return err
	}
	switch edit.kind {
	case frameRowEditInsert:
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;%dH\x1b[%d@", rowIndex+1, edit.start+1, edit.count); err != nil {
			return err
		}
		return writeFrameRowRange(stdout, row, edit.start, edit.start+edit.count)
	case frameRowEditDelete:
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;%dH\x1b[%dP", rowIndex+1, edit.start+1, edit.count); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;%dH", rowIndex+1, len(row.cells)-edit.count+1); err != nil {
			return err
		}
		return writeFrameRowRange(stdout, row, len(row.cells)-edit.count, len(row.cells))
	default:
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;%dH", rowIndex+1, edit.start+1); err != nil {
			return err
		}
		return writeFrameRowRange(stdout, row, edit.start, edit.end)
	}
}

func writeFrameRowRange(stdout io.Writer, row frameRow, start, end int) error {
	if start < 0 {
		start = 0
	}
	if end > len(row.cells) {
		end = len(row.cells)
	}
	if start >= end {
		return nil
	}
	if _, err := io.WriteString(stdout, styleReset()); err != nil {
		return err
	}
	currentStyle := ""
	for i := start; i < end; i++ {
		cell := row.cells[i]
		if cell.style != currentStyle {
			if cell.style == "" {
				if _, err := io.WriteString(stdout, styleReset()); err != nil {
					return err
				}
			} else {
				if _, err := io.WriteString(stdout, cell.style); err != nil {
					return err
				}
			}
			currentStyle = cell.style
		}
		if _, err := io.WriteString(stdout, string(cell.r)); err != nil {
			return err
		}
	}
	if currentStyle != "" {
		if _, err := io.WriteString(stdout, styleReset()); err != nil {
			return err
		}
	}
	return nil
}

func writeFrameTerminalState(stdout io.Writer, prev, next frameTerminalState) error {
	if prev.altScreen != next.altScreen {
		seq := "\x1b[?1049l"
		if next.altScreen {
			seq = "\x1b[?1049h"
		}
		if _, err := io.WriteString(stdout, seq); err != nil {
			return err
		}
	}
	if prev.cursorShape != next.cursorShape {
		shape := "2"
		if next.cursorShape == frameCursorShapeBar {
			shape = "6"
		}
		if _, err := fmt.Fprintf(stdout, "\x1b[%s q", shape); err != nil {
			return err
		}
	}
	for _, mode := range []struct {
		prev bool
		next bool
		on   string
		off  string
	}{
		{prev.mousePress, next.mousePress, "\x1b[?1002h", "\x1b[?1002l"},
		{prev.mouseMotion, next.mouseMotion, "\x1b[?1003h", "\x1b[?1003l"},
		{prev.focusReporting, next.focusReporting, "\x1b[?1004h", "\x1b[?1004l"},
		{prev.mouseSGR, next.mouseSGR, "\x1b[?1006h", "\x1b[?1006l"},
		{prev.bracketedPaste, next.bracketedPaste, "\x1b[?2004h", "\x1b[?2004l"},
	} {
		if mode.prev == mode.next {
			continue
		}
		seq := mode.off
		if mode.next {
			seq = mode.on
		}
		if _, err := io.WriteString(stdout, seq); err != nil {
			return err
		}
	}
	return nil
}

func writeFrameCursor(stdout io.Writer, cursor frameCursor) error {
	if !cursor.visible {
		_, err := io.WriteString(stdout, "\x1b[?25l")
		return err
	}
	_, err := fmt.Fprintf(stdout, "\x1b[?25h\x1b[%d;%dH", cursor.row+1, cursor.col+1)
	return err
}

func renderBufferFrameRow(cells []frameCell, state *bufferState, start, end int, inactive bool, theme *uiTheme) {
	col := 0
	collapsedPos, collapsedCol, collapsedVisible := collapsedInactiveSelection(state, inactive, start)
	collapsedPainted := false
	for p := start; p < end && col < len(cells); p++ {
		style := ""
		if collapsedVisible {
			switch {
			case collapsedCol >= len(cells):
				if p == end-1 {
					style = highlightPrefix(theme, true)
					collapsedPainted = true
				}
			case p == collapsedPos:
				style = highlightPrefix(theme, true)
				collapsedPainted = true
			}
		} else if !state.flashSelection && p >= state.dotStart && p < state.dotEnd {
			style = highlightPrefix(theme, false)
		}
		col = setFrameRune(cells, state.text[p], col, style, bufferTabWidth)
	}
	if collapsedVisible && !collapsedPainted && collapsedCol == col && col < len(cells) {
		cells[col] = frameCell{r: ' ', style: highlightPrefix(theme, true)}
	}
}

func renderOverlayFrameLine(cells []frameCell, line overlayRenderLine, overlay *overlayState, theme *uiTheme) {
	if line.history < 0 {
		renderFilledFrameRow(cells, theme.hudPrefix())
		renderTextFrameRow(cells, 0, line.text, theme.hudPrefix(), hudTabWidth)
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

	baseStyle := overlayLinePrefix(theme, line.command)
	if baseStyle != "" {
		renderFilledFrameRow(cells, baseStyle)
	}

	runes := []rune(line.text)
	col := 0
	for i, r := range runes {
		if col >= len(cells) {
			break
		}
		style := baseStyle
		drawRune := r
		if line.running {
			style = shimmerPrefix(theme, i, len(runes))
		} else if i >= selStart && i < selEnd {
			style = highlightPrefix(theme, false)
		} else if theme != nil && i == 0 && r == '█' {
			style = theme.outputPrefix()
			drawRune = ' '
		}
		col = setFrameRune(cells, drawRune, col, style, hudTabWidth)
	}
}

func renderOverlayPromptFrameRow(cells []frameCell, overlay *overlayState, theme *uiTheme) {
	renderFilledFrameRow(cells, theme.hudPrefix())
	if overlay == nil {
		return
	}
	renderRunesFrameRow(cells, 0, overlay.input, theme.hudPrefix(), hudTabWidth)
}

func renderFilledFrameRow(cells []frameCell, style string) {
	if style == "" {
		return
	}
	for i := range cells {
		cells[i] = frameCell{r: ' ', style: style}
	}
}

func renderTextFrameRow(cells []frameCell, col int, text, style string, tabWidth int) int {
	return renderRunesFrameRow(cells, col, []rune(text), style, tabWidth)
}

func renderFrameRowText(frame *terminalFrame, row, col int, text, style string, tabWidth int) {
	if frame == nil || row < 0 || row >= len(frame.rows) {
		return
	}
	renderTextFrameRow(frame.rows[row].cells, col, text, style, tabWidth)
}

func renderRunesFrameRow(cells []frameCell, col int, runes []rune, style string, tabWidth int) int {
	for _, r := range runes {
		if col >= len(cells) {
			break
		}
		col = setFrameRune(cells, r, col, style, tabWidth)
	}
	return col
}

func setFrameRune(cells []frameCell, r rune, col int, style string, tabWidth int) int {
	advance := runeDisplayAdvance(r, col, len(cells), tabWidth)
	if advance <= 0 {
		return col
	}
	if r == '\t' {
		for i := 0; i < advance && col+i < len(cells); i++ {
			cells[col+i] = frameCell{r: ' ', style: style}
		}
		return col + advance
	}
	if col < len(cells) {
		cells[col] = frameCell{r: r, style: style}
	}
	return col + 1
}

func frameMenuBorderStyle(theme *uiTheme) string {
	if theme == nil {
		return ""
	}
	return theme.subtlePrefix()
}

func frameMenuItemStyle(theme *uiTheme, current, hover bool) string {
	if theme != nil {
		return menuItemPrefix(theme, current, hover)
	}
	switch {
	case hover && current:
		return "\x1b[1;7m"
	case hover:
		return "\x1b[7m"
	case current:
		return "\x1b[1m"
	default:
		return ""
	}
}

func formatMenuItemLine(item menuItem, inner int) string {
	content := item.label
	if item.shortcut != "" {
		padding := inner - len([]rune(item.label)) - len([]rune(item.shortcut)) - 1
		if padding < 1 {
			padding = 1
		}
		content += strings.Repeat(" ", padding) + item.shortcut
	}
	runes := []rune(content)
	if len(runes) > inner {
		runes = runes[len(runes)-inner:]
	}
	content = string(runes)
	if pad := inner - len([]rune(content)); pad > 0 {
		content += strings.Repeat(" ", pad)
	}
	return "│" + content + "│"
}
