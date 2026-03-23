package term

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

const minOverlayRows = 3

type overlayEntry struct {
	command bool
	text    string
}

type overlayState struct {
	visible    bool
	input      []rune
	cursor     int
	history    []overlayEntry
	scroll     int
	recallIdx  int
	savedInput []rune
}

func newOverlayState() *overlayState {
	return &overlayState{recallIdx: -1}
}

func (o *overlayState) open(prefill string) {
	o.visible = true
	o.input = []rune(prefill)
	o.cursor = len(o.input)
	o.scroll = 0
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
}

func (o *overlayState) close() {
	o.visible = false
	o.scroll = 0
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
}

func (o *overlayState) clearHistory() {
	o.history = nil
	o.scroll = 0
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
	o.history = append(o.history, overlayEntry{text: text})
	o.scroll = 0
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

func (o *overlayState) renderLines(limit int) []string {
	if limit <= 0 {
		return nil
	}
	end := len(o.history) - o.scroll
	if end < 0 {
		end = 0
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, end-start)
	for _, entry := range o.history[start:end] {
		if entry.command {
			lines = append(lines, "> "+entry.text)
			continue
		}
		lines = append(lines, entry.text)
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
	limit := overlayHeight(o) - 1
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

func overlayHeight(o *overlayState) int {
	if o == nil || !o.visible {
		return 0
	}
	height := len(o.history) + 1
	if height < minOverlayRows {
		height = minOverlayRows
	}
	maxHeight := termRows / 2
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
	defer c.mu.Unlock()
	if c.capturing && len(c.partial) > 0 && c.onLine != nil {
		c.onLine(strings.TrimSuffix(string(c.partial), "\r"))
	}
	c.capturing = false
	c.partial = c.partial[:0]
	c.onLine = nil
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
	defer c.mu.Unlock()
	if !c.capturing {
		if dst == nil {
			return len(p), nil
		}
		return dst.Write(p)
	}
	c.partial = append(c.partial, p...)
	for {
		idx := bytes.IndexByte(c.partial, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSuffix(string(c.partial[:idx]), "\r")
		if c.onLine != nil {
			c.onLine(line)
		}
		c.partial = append([]byte(nil), c.partial[idx+1:]...)
	}
	return len(p), nil
}
