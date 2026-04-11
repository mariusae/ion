package term

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	clienttarget "ion/internal/client/target"
)

const minOverlayRows = 1
const overlayPadRows = 1

type overlayEntry struct {
	command bool
	text    string
}

type overlayState struct {
	visible        bool
	mode           overlayMode
	input          []rune
	cursor         int
	maxHeightRows  int
	history        []overlayEntry
	commandIdx     []int
	commandIdxLen  int
	scroll         int
	running        bool
	resizing       bool
	resizeMoved    bool
	resizeStartY   int
	flashSelection bool
	selecting      bool
	selectBtn2     bool
	selectStart    overlaySelectionPos
	selectEnd      overlaySelectionPos
	recallIdx      int
	savedInput     []rune
	picker         *overlayPicker
}

type overlaySelectionPos struct {
	line int
	col  int
}

type overlayRenderLine struct {
	text         string
	history      int
	command      bool
	selected     bool
	pickerActive bool
	offset       int
	running      bool
	prefixRunes  int
	contentStart int
	contentEnd   int
}

func newOverlayState() *overlayState {
	return &overlayState{
		recallIdx:    -1,
		resizeStartY: -1,
		selectStart:  overlaySelectionPos{line: -1},
		selectEnd:    overlaySelectionPos{line: -1},
	}
}

func (o *overlayState) open(prefill string) {
	o.visible = true
	o.mode = overlayModeCommand
	o.input = []rune(prefill)
	o.cursor = len(o.input)
	o.scroll = 0
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
	o.resizing = false
	o.resizeMoved = false
	o.resizeStartY = -1
	o.picker = nil
}

func (o *overlayState) reopen() {
	o.visible = true
	o.mode = overlayModeCommand
	o.running = false
	o.resizing = false
	o.resizeMoved = false
	o.resizeStartY = -1
	o.selecting = false
	o.selectBtn2 = false
	o.selectStart = overlaySelectionPos{line: -1}
	o.selectEnd = overlaySelectionPos{line: -1}
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
	if o.cursor < 0 {
		o.cursor = 0
	}
	if o.cursor > len(o.input) {
		o.cursor = len(o.input)
	}
	o.picker = nil
}

func (o *overlayState) close() {
	o.visible = false
	o.mode = overlayModeCommand
	o.running = false
	o.resizing = false
	o.resizeMoved = false
	o.resizeStartY = -1
	o.selecting = false
	o.selectBtn2 = false
	o.selectStart = overlaySelectionPos{line: -1}
	o.selectEnd = overlaySelectionPos{line: -1}
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
	o.picker = nil
}

func (o *overlayState) clearHistory() {
	o.history = nil
	o.commandIdx = nil
	o.commandIdxLen = 0
	o.scroll = 0
	o.running = false
	o.resizing = false
	o.resizeMoved = false
	o.resizeStartY = -1
	o.selecting = false
	o.selectBtn2 = false
	o.selectStart = overlaySelectionPos{line: -1}
	o.selectEnd = overlaySelectionPos{line: -1}
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
	o.picker = nil
}

func (o *overlayState) insert(text []rune) {
	if len(text) == 0 {
		return
	}
	next := append([]rune{}, o.input[:o.cursor]...)
	next = append(next, text...)
	next = append(next, o.input[o.cursor:]...)
	o.input = next
	o.cursor += len(text)
	o.refreshPicker()
}

func (o *overlayState) replaceRange(start, end int, text []rune) {
	if o == nil {
		return
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(o.input) {
		end = len(o.input)
	}
	next := append([]rune{}, o.input[:start]...)
	next = append(next, text...)
	next = append(next, o.input[end:]...)
	o.input = next
	o.cursor = start + len(text)
	o.refreshPicker()
}

func (o *overlayState) backspace() {
	if o.cursor == 0 {
		return
	}
	copy(o.input[o.cursor-1:], o.input[o.cursor:])
	o.cursor--
	o.input = o.input[:len(o.input)-1]
	o.refreshPicker()
}

func (o *overlayState) deleteForward() {
	if o.cursor >= len(o.input) {
		return
	}
	copy(o.input[o.cursor:], o.input[o.cursor+1:])
	o.input = o.input[:len(o.input)-1]
	o.refreshPicker()
}

func (o *overlayState) killLine() {
	if o.cursor >= len(o.input) {
		return
	}
	o.input = o.input[:o.cursor]
	o.refreshPicker()
}

func (o *overlayState) killToStart() {
	if o.cursor <= 0 {
		return
	}
	next := append([]rune{}, o.input[o.cursor:]...)
	o.input = next
	o.cursor = 0
	o.refreshPicker()
}

func (o *overlayState) killWord() {
	if o.cursor == 0 {
		return
	}
	start := prevWordStart(o.input, o.cursor)
	next := append([]rune{}, o.input[:start]...)
	next = append(next, o.input[o.cursor:]...)
	o.input = next
	o.cursor = start
	o.refreshPicker()
}

func (o *overlayState) moveLeft() {
	if o.cursor > 0 {
		o.cursor--
	}
}

func (o *overlayState) moveRight() {
	if o.cursor < len(o.input) {
		o.cursor++
	}
}

func (o *overlayState) moveHome() {
	o.cursor = 0
}

func (o *overlayState) moveEnd() {
	o.cursor = len(o.input)
}

func (o *overlayState) moveWordLeft() {
	o.cursor = prevWordStart(o.input, o.cursor)
}

func (o *overlayState) moveWordRight() {
	o.cursor = nextWordStart(o.input, o.cursor)
}

func (o *overlayState) addCommand(text string) {
	if o.commandIdxLen != len(o.history) {
		o.rebuildCommandIdx()
	}
	o.history = append(o.history, overlayEntry{command: true, text: text})
	o.commandIdx = append(o.commandIdx, len(o.history)-1)
	o.commandIdxLen = len(o.history)
	o.scroll = 0
}

func (o *overlayState) addOutput(text string) {
	if o.commandIdxLen != len(o.history) {
		o.rebuildCommandIdx()
	}
	o.history = append(o.history, overlayEntry{text: sanitizeOverlayOutput(text)})
	o.commandIdxLen = len(o.history)
	o.scroll = 0
}

func sanitizeOverlayOutput(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		switch text[i] {
		case 0x1b:
			next := skipANSIEscape(text, i)
			if next == i {
				i++
			} else {
				i = next
			}
		default:
			r, size := utf8.DecodeRuneInString(text[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			if r < 0x20 && r != '\t' {
				i += size
				continue
			}
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

func skipANSIEscape(text string, start int) int {
	if start+1 >= len(text) {
		return start + 1
	}
	switch text[start+1] {
	case '[':
		i := start + 2
		for i < len(text) {
			c := text[i]
			if c >= 0x40 && c <= 0x7e {
				return i + 1
			}
			i++
		}
		return len(text)
	case ']':
		i := start + 2
		for i < len(text) {
			switch text[i] {
			case 0x07:
				return i + 1
			case 0x1b:
				if i+1 < len(text) && text[i+1] == '\\' {
					return i + 2
				}
			}
			i++
		}
		return len(text)
	default:
		return start + 2
	}
}

func (o *overlayState) lastCommand() (string, bool) {
	indices := o.commandIndices()
	if len(indices) == 0 {
		return "", false
	}
	return o.history[indices[len(indices)-1]].text, true
}

func (o *overlayState) commandHistory() []string {
	if o == nil {
		return nil
	}
	indices := o.commandIndices()
	commands := make([]string, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(o.history) {
			continue
		}
		commands = append(commands, o.history[idx].text)
	}
	return commands
}

func (o *overlayState) recallPrev() bool {
	indices := o.commandIndices()
	next := o.recallIdx + 1
	if next < 0 || next >= len(indices) {
		return false
	}
	if o.recallIdx == -1 {
		o.savedInput = append(o.savedInput[:0], o.input...)
	}
	o.recallIdx = next
	o.loadCommand(indices[len(indices)-1-next])
	return true
}

func (o *overlayState) recallNext() bool {
	indices := o.commandIndices()
	if o.recallIdx > 0 {
		o.recallIdx--
		if o.recallIdx < 0 || o.recallIdx >= len(indices) {
			return false
		}
		o.loadCommand(indices[len(indices)-1-o.recallIdx])
		return true
	}
	if o.recallIdx == 0 {
		o.recallIdx = -1
		o.input = append(o.input[:0], o.savedInput...)
		o.cursor = len(o.input)
		o.refreshPicker()
		return true
	}
	return false
}

func (o *overlayState) resetInput() {
	o.input = o.input[:0]
	o.cursor = 0
	o.scroll = 0
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
	o.refreshPicker()
}

func (o *overlayState) promptLine() string {
	var out []rune
	for i, r := range o.input {
		if i == o.cursor {
			out = append(out, '\x00')
		}
		out = append(out, r)
	}
	if o.cursor == len(o.input) {
		out = append(out, '\x00')
	}
	return string(out)
}

func wrapOverlayRunes(content []rune, prefix string) []overlayRenderLine {
	if termCols <= 0 {
		return nil
	}
	prefixRunes := []rune(prefix)
	lines := make([]overlayRenderLine, 0, 1)
	start := 0
	for {
		line := overlayRenderLine{contentStart: start}
		var b strings.Builder
		col := 0
		for _, glyph := range prefixRunes {
			advance := runeDisplayAdvance(glyph, col, termCols, hudTabWidth)
			if advance <= 0 {
				break
			}
			b.WriteRune(glyph)
			col += advance
			line.prefixRunes++
		}
		line.offset = col
		for start < len(content) {
			advance := runeDisplayAdvance(content[start], col, termCols, hudTabWidth)
			if advance <= 0 {
				break
			}
			b.WriteRune(content[start])
			col += advance
			start++
		}
		line.contentEnd = start
		line.text = b.String()
		lines = append(lines, line)
		if len(content) == 0 || start >= len(content) || line.contentEnd == line.contentStart {
			break
		}
	}
	return lines
}

func (o *overlayState) renderAllLines() []overlayRenderLine {
	if o != nil && o.picker != nil {
		lines := make([]overlayRenderLine, 0, len(o.picker.filtered))
		for order, idx := range o.picker.filtered {
			item := o.picker.items[idx]
			prefix := "  "
			if order == o.picker.selected {
				prefix = "█ "
			}
			wrapped := wrapOverlayRunes([]rune(item.label), prefix)
			for i := range wrapped {
				wrapped[i].history = order
				wrapped[i].pickerActive = order == o.picker.selected
			}
			lines = append(lines, wrapped...)
		}
		return lines
	}
	if o == nil || len(o.history) == 0 {
		return nil
	}
	lines := make([]overlayRenderLine, 0, len(o.history))
	runningIdx := -1
	if o.running {
		indices := o.commandIndices()
		if len(indices) > 0 {
			runningIdx = indices[len(indices)-1]
		}
	}
	for idx, entry := range o.history {
		prefix := ""
		if !entry.command {
			prefix = "█ "
		}
		wrapped := wrapOverlayRunes([]rune(entry.text), prefix)
		for i := range wrapped {
			wrapped[i].history = idx
			wrapped[i].command = entry.command
			wrapped[i].running = idx == runningIdx
		}
		lines = append(lines, wrapped...)
	}
	return lines
}

func (o *overlayState) renderPromptLines() []overlayRenderLine {
	if o == nil || !o.visible || o.running {
		return nil
	}
	return wrapOverlayRunes(o.input, "")
}

func (o *overlayState) renderLines(limit int) []overlayRenderLine {
	if limit <= 0 {
		return nil
	}
	all := o.renderAllLines()
	if o != nil && o.picker != nil {
		if len(all) <= limit {
			return append([]overlayRenderLine(nil), all...)
		}
		selected := o.picker.selected
		if selected < 0 {
			return append([]overlayRenderLine(nil), all[:limit]...)
		}
		selStart := 0
		for i, line := range all {
			if line.history == selected {
				selStart = i
				break
			}
		}
		selEnd := selStart + 1
		for selEnd < len(all) && all[selEnd].history == selected {
			selEnd++
		}
		start := selStart - limit/2
		if start < 0 {
			start = 0
		}
		if selEnd > start+limit {
			start = selEnd - limit
		}
		if start+limit > len(all) {
			start = len(all) - limit
		}
		if start < 0 {
			start = 0
		}
		return append([]overlayRenderLine(nil), all[start:start+limit]...)
	}
	end := len(all) - o.scroll
	if end < 0 {
		end = 0
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	return append([]overlayRenderLine(nil), all[start:end]...)
}

func (o *overlayState) maxScroll(limit int) int {
	if limit <= 0 {
		return 0
	}
	total := len(o.renderAllLines())
	if total <= limit {
		return 0
	}
	return total - limit
}

func (o *overlayState) scrollOlder(lines int) {
	if lines <= 0 {
		return
	}
	limit := overlayHistoryRows(o)
	o.scroll += lines
	if max := o.maxScroll(limit); o.scroll > max {
		o.scroll = max
	}
}

func (o *overlayState) scrollNewer(lines int) {
	if lines <= 0 {
		return
	}
	o.scroll -= lines
	if o.scroll < 0 {
		o.scroll = 0
	}
}

func overlayTopRow(o *overlayState) int {
	if o == nil || !o.visible {
		return termRows
	}
	return termRows - overlayHeight(o)
}

func overlayTopPadRows(o *overlayState) int {
	if o == nil || !o.visible {
		return 0
	}
	return overlayPadRows
}

func overlayBottomPadRows(o *overlayState) int {
	if o == nil || !o.visible {
		return 0
	}
	return overlayPadRows
}

func overlayPromptRows(o *overlayState) int {
	if o == nil || !o.visible {
		return 0
	}
	if o.running {
		return 1
	}
	return len(o.renderPromptLines())
}

func overlayHistoryRows(o *overlayState) int {
	height := overlayHeight(o)
	usedRows := overlayTopPadRows(o) + overlayPromptRows(o) + overlayBottomPadRows(o)
	if height < usedRows {
		return 0
	}
	return height - usedRows
}

func overlayHeight(o *overlayState) int {
	if o == nil || !o.visible {
		return 0
	}
	topPad := overlayTopPadRows(o)
	prompt := overlayPromptRows(o)
	bottomPad := overlayBottomPadRows(o)
	historyLines := len(o.renderAllLines())
	height := historyLines + topPad + prompt + bottomPad
	if height < minOverlayRows {
		height = minOverlayRows
	}
	maxHeight := overlayMaxHeight(o)
	minVisible := topPad + prompt + bottomPad
	if historyLines > 0 {
		minVisible++
	}
	if maxHeight < minVisible {
		maxHeight = minVisible
	}
	if maxHeight < minOverlayRows {
		maxHeight = minOverlayRows
	}
	if height > maxHeight {
		height = maxHeight
	}
	if height > termRows {
		height = termRows
	}
	return height
}

func overlayMaxHeight(o *overlayState) int {
	if o != nil && o.maxHeightRows > 0 {
		return o.maxHeightRows
	}
	return termRows / 3
}

func (o *overlayState) beginResize() bool {
	if o == nil || !o.visible {
		return false
	}
	o.resizing = true
	o.resizeMoved = false
	o.resizeStartY = overlayTopRow(o)
	o.selecting = false
	o.selectBtn2 = false
	return true
}

func (o *overlayState) endResize() {
	if o == nil {
		return
	}
	o.resizing = false
	o.resizeMoved = false
	o.resizeStartY = -1
}

func (o *overlayState) resizeToTopRow(row int) bool {
	if o == nil {
		return false
	}
	if row < 0 {
		row = 0
	}
	if row >= termRows {
		row = termRows - 1
	}
	next := termRows - row
	if next < minOverlayRows {
		next = minOverlayRows
	}
	if next > termRows {
		next = termRows
	}
	if next == termRows/3 {
		next = 0
	}
	if o.maxHeightRows == next {
		return false
	}
	o.maxHeightRows = next
	return true
}

func (o *overlayState) screenToPos(row, col int) overlaySelectionPos {
	pos := overlaySelectionPos{line: -1}
	if o == nil || !o.visible {
		return pos
	}
	lines := o.renderLines(overlayHistoryRows(o))
	top := overlayTopRow(o)
	lineRow := row - top - overlayTopPadRows(o)
	if lineRow < 0 || lineRow >= len(lines) {
		return pos
	}
	line := lines[lineRow]
	pos.line = line.history
	pos.col = line.contentPosForScreenCol(col)
	return pos
}

func (line overlayRenderLine) contentPosForScreenCol(col int) int {
	if col <= line.offset {
		return line.contentStart
	}
	if col < 0 {
		col = 0
	}
	contentCol := line.contentStart
	visualCol := line.offset
	runes := []rune(line.text)
	for i := line.prefixRunes; i < len(runes); i++ {
		advance := runeDisplayAdvance(runes[i], visualCol, termCols, hudTabWidth)
		if advance <= 0 {
			break
		}
		if col < visualCol+advance {
			return contentCol
		}
		visualCol += advance
		contentCol++
	}
	if contentCol > line.contentEnd {
		contentCol = line.contentEnd
	}
	return contentCol
}

func (o *overlayState) hasSelection() bool {
	if o == nil {
		return false
	}
	return o.selectStart.line >= 0 && o.selectEnd.line >= 0 &&
		(o.selectStart.line != o.selectEnd.line || o.selectStart.col != o.selectEnd.col)
}

func (o *overlayState) selectionBounds() (overlaySelectionPos, overlaySelectionPos, bool) {
	if o == nil || o.selectStart.line < 0 || o.selectEnd.line < 0 {
		return overlaySelectionPos{}, overlaySelectionPos{}, false
	}
	start, end := o.selectStart, o.selectEnd
	if end.line < start.line || (end.line == start.line && end.col < start.col) {
		start, end = end, start
	}
	return start, end, true
}

func (o *overlayState) selectedText() []rune {
	start, end, ok := o.selectionBounds()
	if !ok {
		return nil
	}
	var out []rune
	for i := start.line; i <= end.line && i < len(o.history); i++ {
		if i < 0 {
			continue
		}
		text := []rune(o.history[i].text)
		lineStart := 0
		lineEnd := len(text)
		if i == start.line {
			lineStart = start.col
		}
		if i == end.line {
			lineEnd = end.col
		}
		if lineStart < 0 {
			lineStart = 0
		}
		if lineStart > len(text) {
			lineStart = len(text)
		}
		if lineEnd < lineStart {
			lineEnd = lineStart
		}
		if lineEnd > len(text) {
			lineEnd = len(text)
		}
		out = append(out, text[lineStart:lineEnd]...)
		if i < end.line {
			out = append(out, '\n')
		}
	}
	return out
}

func (o *overlayState) historyText(idx int) string {
	if o == nil || idx < 0 || idx >= len(o.history) {
		return ""
	}
	return o.history[idx].text
}

func (o *overlayState) tokenAt(pos overlaySelectionPos) string {
	text := []rune(o.historyText(pos.line))
	if len(text) == 0 {
		return ""
	}
	if pos.col < 0 {
		pos.col = 0
	}
	if pos.col > len(text) {
		pos.col = len(text)
	}
	left := pos.col
	for left > 0 && tokenRune(text[left-1]) {
		left--
	}
	right := pos.col
	for right < len(text) && tokenRune(text[right]) {
		right++
	}
	for right > left && text[right-1] == ':' {
		right--
	}
	return clienttarget.TrimToken(string(text[left:right]))
}

func tokenRune(r rune) bool {
	return r >= 0x21 && r != '"' && r != '`'
}

func trimOverlaySelection(text []rune) string {
	return strings.TrimSpace(string(text))
}

func isOverlayClickSelection(start, end overlaySelectionPos) bool {
	if start.line < 0 || end.line < 0 || start.line != end.line {
		return false
	}
	delta := start.col - end.col
	if delta < 0 {
		delta = -delta
	}
	return delta <= 1
}

func (o *overlayState) setRunning(running bool) {
	o.running = running
}

func (o *overlayState) commandIndices() []int {
	if o == nil {
		return nil
	}
	if o.commandIdxLen != len(o.history) {
		o.rebuildCommandIdx()
	}
	for _, idx := range o.commandIdx {
		if idx < 0 || idx >= len(o.history) {
			o.rebuildCommandIdx()
			break
		}
	}
	return o.commandIdx
}

func (o *overlayState) rebuildCommandIdx() {
	if o == nil {
		return
	}
	o.commandIdx = o.commandIdx[:0]
	for i, entry := range o.history {
		if entry.command {
			o.commandIdx = append(o.commandIdx, i)
		}
	}
	o.commandIdxLen = len(o.history)
}

func (o *overlayState) loadCommand(idx int) {
	o.input = []rune(o.history[idx].text)
	o.cursor = len(o.input)
}

type OutputCapture struct {
	mu        sync.Mutex
	stdout    io.Writer
	stderr    io.Writer
	capturing bool
	partial   []byte
	onLine    func(string)
}

func NewOutputCapture(stdout, stderr io.Writer) *OutputCapture {
	return &OutputCapture{
		stdout: stdout,
		stderr: stderr,
	}
}

func (c *OutputCapture) Stdout() io.Writer {
	return captureWriter{capture: c, dst: c.stdout}
}

func (c *OutputCapture) Stderr() io.Writer {
	return captureWriter{capture: c, dst: c.stderr}
}

func (c *OutputCapture) Start(onLine func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.capturing = true
	c.partial = c.partial[:0]
	c.onLine = onLine
}

func (c *OutputCapture) Stop() {
	c.mu.Lock()
	var tail string
	onLine := c.onLine
	if c.capturing && len(c.partial) > 0 && c.onLine != nil {
		tail = strings.TrimSuffix(string(c.partial), "\r")
	}
	c.capturing = false
	c.partial = c.partial[:0]
	c.onLine = nil
	c.mu.Unlock()
	if tail != "" && onLine != nil {
		onLine(tail)
	}
}

type captureWriter struct {
	capture *OutputCapture
	dst     io.Writer
}

func (w captureWriter) Write(p []byte) (int, error) {
	return w.capture.writeTo(w.dst, p)
}

func (c *OutputCapture) writeTo(dst io.Writer, p []byte) (int, error) {
	c.mu.Lock()
	if !c.capturing {
		c.mu.Unlock()
		if dst == nil {
			return len(p), nil
		}
		return dst.Write(p)
	}
	c.partial = append(c.partial, p...)
	var lines []string
	for {
		idx := bytes.IndexByte(c.partial, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSuffix(string(c.partial[:idx]), "\r")
		lines = append(lines, line)
		c.partial = append([]byte(nil), c.partial[idx+1:]...)
	}
	onLine := c.onLine
	c.mu.Unlock()
	if onLine != nil {
		for _, line := range lines {
			onLine(line)
		}
	}
	return len(p), nil
}
