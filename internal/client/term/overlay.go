package term

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const minOverlayRows = 1
const overlayPadRows = 1

type overlayEntry struct {
	command bool
	text    string
}

type overlayState struct {
	visible        bool
	input          []rune
	cursor         int
	history        []overlayEntry
	scroll         int
	running        bool
	flashSelection bool
	selecting      bool
	selectBtn2     bool
	selectStart    overlaySelectionPos
	selectEnd      overlaySelectionPos
	recallIdx      int
	savedInput     []rune
}

type overlaySelectionPos struct {
	line int
	col  int
}

type overlayRenderLine struct {
	text    string
	history int
	command bool
	offset  int
	running bool
}

func newOverlayState() *overlayState {
	return &overlayState{
		recallIdx:   -1,
		selectStart: overlaySelectionPos{line: -1},
		selectEnd:   overlaySelectionPos{line: -1},
	}
}

func (o *overlayState) open(prefill string) {
	o.visible = true
	o.input = []rune(prefill)
	o.cursor = len(o.input)
	o.scroll = 0
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
}

func (o *overlayState) reopen() {
	o.visible = true
	o.scroll = 0
	o.running = false
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
}

func (o *overlayState) close() {
	o.visible = false
	o.scroll = 0
	o.running = false
	o.selecting = false
	o.selectBtn2 = false
	o.selectStart = overlaySelectionPos{line: -1}
	o.selectEnd = overlaySelectionPos{line: -1}
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
}

func (o *overlayState) clearHistory() {
	o.history = nil
	o.scroll = 0
	o.running = false
	o.selecting = false
	o.selectBtn2 = false
	o.selectStart = overlaySelectionPos{line: -1}
	o.selectEnd = overlaySelectionPos{line: -1}
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
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
}

func (o *overlayState) backspace() {
	if o.cursor == 0 {
		return
	}
	copy(o.input[o.cursor-1:], o.input[o.cursor:])
	o.cursor--
	o.input = o.input[:len(o.input)-1]
}

func (o *overlayState) deleteForward() {
	if o.cursor >= len(o.input) {
		return
	}
	copy(o.input[o.cursor:], o.input[o.cursor+1:])
	o.input = o.input[:len(o.input)-1]
}

func (o *overlayState) killLine() {
	o.input = o.input[:0]
	o.cursor = 0
}

func (o *overlayState) killWord() {
	if o.cursor == 0 {
		return
	}
	start := o.cursor
	for start > 0 && o.input[start-1] == ' ' {
		start--
	}
	for start > 0 && o.input[start-1] != ' ' {
		start--
	}
	next := append([]rune{}, o.input[:start]...)
	next = append(next, o.input[o.cursor:]...)
	o.input = next
	o.cursor = start
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

func (o *overlayState) addCommand(text string) {
	o.history = append(o.history, overlayEntry{command: true, text: text})
	o.scroll = 0
}

func (o *overlayState) addOutput(text string) {
	o.history = append(o.history, overlayEntry{text: sanitizeOverlayOutput(text)})
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
	idx := o.findCommand(0)
	if idx < 0 {
		return "", false
	}
	return o.history[idx].text, true
}

func (o *overlayState) recallPrev() bool {
	next := o.recallIdx + 1
	idx := o.findCommand(next)
	if idx < 0 {
		return false
	}
	if o.recallIdx == -1 {
		o.savedInput = append(o.savedInput[:0], o.input...)
	}
	o.recallIdx = next
	o.loadCommand(idx)
	return true
}

func (o *overlayState) recallNext() bool {
	if o.recallIdx > 0 {
		o.recallIdx--
		idx := o.findCommand(o.recallIdx)
		if idx < 0 {
			return false
		}
		o.loadCommand(idx)
		return true
	}
	if o.recallIdx == 0 {
		o.recallIdx = -1
		o.input = append(o.input[:0], o.savedInput...)
		o.cursor = len(o.input)
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

func (o *overlayState) renderLines(limit int) []overlayRenderLine {
	if limit <= 0 {
		return nil
	}
	historyLimit := limit
	end := len(o.history) - o.scroll
	if end < 0 {
		end = 0
	}
	start := end - historyLimit
	if start < 0 {
		start = 0
	}
	lines := make([]overlayRenderLine, 0, end-start)
	runningIdx := -1
	if o.running {
		runningIdx = o.findCommand(0)
	}
	for idx, entry := range o.history[start:end] {
		line := overlayRenderLine{
			history: start + idx,
			command: entry.command,
			running: start+idx == runningIdx,
		}
		if entry.command {
			line.text = entry.text
			lines = append(lines, line)
			continue
		}
		line.text = "█ " + entry.text
		line.offset = 2
		lines = append(lines, line)
	}
	return lines
}

func (o *overlayState) maxScroll(limit int) int {
	if limit <= 0 {
		return 0
	}
	if len(o.history) <= limit {
		return 0
	}
	return len(o.history) - limit
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
	if o == nil || !o.visible || o.running {
		return 0
	}
	return 1
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
	height := len(o.history) + topPad + prompt + bottomPad
	if height < minOverlayRows {
		height = minOverlayRows
	}
	maxHeight := termRows / 2
	minVisible := topPad + prompt + bottomPad
	if len(o.history) > 0 {
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
	runes := []rune(line.text)
	if col < 0 {
		col = 0
	}
	if col > len(runes) {
		col = len(runes)
	}
	pos.line = line.history
	if col <= line.offset {
		pos.col = 0
		return pos
	}
	pos.col = col - line.offset
	return pos
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
	lastNumEnd := -1
	for i := left; i < right; i++ {
		if text[i] != ':' || i+1 >= right || text[i+1] < '0' || text[i+1] > '9' {
			continue
		}
		i++
		for i < right && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		lastNumEnd = i
		i--
	}
	if lastNumEnd > 0 && lastNumEnd < right {
		right = lastNumEnd
	}
	return string(text[left:right])
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

func (o *overlayState) findCommand(n int) int {
	count := 0
	for i := len(o.history) - 1; i >= 0; i-- {
		if !o.history[i].command {
			continue
		}
		if count == n {
			return i
		}
		count++
	}
	return -1
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
