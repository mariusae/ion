package term

import (
	"fmt"
	"io"
	"strings"
)

type terminalFrame struct {
	title  string
	rows   []frameRow
	cursor frameCursor
}

type frameRow struct {
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
}

func newTerminalFrame(rows, cols int) *terminalFrame {
	frame := &terminalFrame{
		rows: make([]frameRow, rows),
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
	return &frameRenderer{}
}

func (r *frameRenderer) Reset() {
	if r == nil {
		return
	}
	r.last = nil
	r.initialized = false
}

func (r *frameRenderer) Render(stdout io.Writer, frame *terminalFrame, forceFull bool) error {
	if frame == nil {
		return nil
	}
	if r == nil {
		return writeFullFrame(stdout, frame)
	}
	if forceFull || !r.initialized || !sameFrameGeometry(r.last, frame) {
		if err := writeFullFrame(stdout, frame); err != nil {
			return err
		}
		r.last = cloneTerminalFrame(frame)
		r.initialized = true
		return nil
	}
	if err := writeFrameDiff(stdout, r.last, frame); err != nil {
		return err
	}
	r.last = cloneTerminalFrame(frame)
	return nil
}

func buildBufferFrame(state *bufferState, overlay *overlayState, menu *menuState, theme *uiTheme, focused bool) *terminalFrame {
	if state == nil {
		return nil
	}
	frame := newTerminalFrame(termRows, termCols)
	frame.title = state.name

	viewRows := bufferViewRows(overlay)
	p := visualRowStartForPos(state.text, state.origin)
	inactive := bufferInactive(overlay, menu, focused)
	for row := 0; row < viewRows; row++ {
		if p <= len(state.text) {
			rowEnd := visualRowEnd(state.text, p)
			renderBufferFrameRow(frame.rows[row].cells, state, p, rowEnd, inactive, theme)
			next := nextVisualRowStart(state.text, p)
			if next != p {
				p = next
				continue
			}
			p = len(state.text) + 1
		}
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
		renderFilledFrameRow(frame.rows[topRow+row].cells, theme.hudPrefix())
	}

	startRow := topRow + overlayTopPadRows(overlay)
	for row := 0; row < historyRows; row++ {
		line := overlayRenderLine{}
		if row < len(lines) {
			line = lines[row]
		}
		renderOverlayFrameLine(frame.rows[startRow+row].cells, line, overlay, theme)
	}

	if overlayPromptRows(overlay) > 0 {
		promptRow := termRows - 1 - overlayBottomPadRows(overlay)
		renderOverlayPromptFrameRow(frame.rows[promptRow].cells, overlay, theme)
	}

	bottomStart := topRow + overlayTopPadRows(overlay) + historyRows + overlayPromptRows(overlay)
	for row := 0; row < overlayBottomPadRows(overlay); row++ {
		renderFilledFrameRow(frame.rows[bottomStart+row].cells, theme.hudPrefix())
	}
}

func renderInlineStatusFrame(frame *terminalFrame, status string, theme *uiTheme) {
	if frame == nil || status == "" || len(frame.rows) == 0 {
		return
	}
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
	renderFrameRowText(frame, row, menu.x, formatMenuBorder(menu.title, inner, '╭', '╮', '─'), frameMenuBorderStyle(theme), 0)
	row++
	for i, item := range menu.items {
		renderFrameRowText(frame, row, menu.x, formatMenuItemLine(item, inner), frameMenuItemStyle(theme, item.current, menu.hover == i), 0)
		row++
		if item.sepAfter && i < len(menu.items)-1 {
			renderFrameRowText(frame, row, menu.x, formatMenuBorder("", inner, '├', '┤', '─'), frameMenuBorderStyle(theme), 0)
			row++
		}
	}
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
	if _, err := io.WriteString(stdout, bufferWindowTitleSequence(frame.title)); err != nil {
		return err
	}
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[?25l\x1b[6 q\x1b[?1000h\x1b[?1002h\x1b[?1004h\x1b[?1006h\x1b[?2004h\x1b[2J"); err != nil {
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

func writeFrameDiff(stdout io.Writer, prev, next *terminalFrame) error {
	if next == nil {
		return nil
	}
	if !sameFrameGeometry(prev, next) {
		return writeFullFrame(stdout, next)
	}

	if prev.title != next.title {
		if _, err := io.WriteString(stdout, bufferWindowTitleSequence(next.title)); err != nil {
			return err
		}
	}

	changedRows := make([]int, 0, len(next.rows))
	for row := range next.rows {
		if !sameFrameRow(prev.rows[row], next.rows[row]) {
			changedRows = append(changedRows, row)
		}
	}

	cursorChanged := prev.cursor != next.cursor
	if len(changedRows) == 0 && !cursorChanged && prev.title == next.title {
		return nil
	}

	if len(changedRows) > 0 {
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
		title:  frame.title,
		rows:   make([]frameRow, len(frame.rows)),
		cursor: frame.cursor,
	}
	for i, row := range frame.rows {
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

func sameFrameRow(a, b frameRow) bool {
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
