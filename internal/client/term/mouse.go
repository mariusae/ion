package term

import (
	"bufio"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

type mouseEvent struct {
	button  int
	x       int
	y       int
	pressed bool
	repeat  int
}

func (e mouseEvent) isMotion() bool {
	return e.button >= 32 && e.button < 64
}

func (e mouseEvent) isWheel() bool {
	return !e.isMotion() && e.button&64 != 0
}

func (e mouseEvent) baseButton() int {
	return e.button & 3
}

func (e mouseEvent) verticalWheelDirection() (int, bool) {
	if !e.isWheel() {
		return 0, false
	}
	switch e.baseButton() {
	case 0:
		return -1, true
	case 1:
		return 1, true
	default:
		return 0, false
	}
}

func (e mouseEvent) count() int {
	if e.repeat > 0 {
		return e.repeat
	}
	return 1
}

func (e mouseEvent) noButtonsDown() bool {
	return e.isMotion() && e.baseButton() == 3
}

func (e mouseEvent) dismissesOverlayOutside() bool {
	if _, ok := e.verticalWheelDirection(); ok {
		return true
	}
	return !e.isMotion() && e.pressed
}

func bufferViewRows(overlay *overlayState) int {
	rows := termRows
	if overlay != nil && overlay.visible {
		rows -= overlayHeight(overlay)
	}
	if rows < 1 {
		return 1
	}
	return rows
}

func readBufferEscape(reader *bufio.Reader, stdin *os.File) (int, *mouseEvent, error) {
	if ok, err := ensureBufferedByte(reader, stdin, escSequenceWait); err != nil {
		return 0, nil, err
	} else if !ok {
		return keyEsc, nil, nil
	}
	b, err := reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	switch b {
	case '[':
		if ok, err := ensureBufferedByte(reader, stdin, escContinuationWait); err != nil {
			return 0, nil, err
		} else if !ok {
			return keyEsc, nil, nil
		}
		b, err = reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		switch b {
		case 'A':
			return keyUp, nil, nil
		case 'B':
			return keyDown, nil, nil
		case 'C':
			return keyRight, nil, nil
		case 'D':
			return keyLeft, nil, nil
		case 'H':
			return keyHome, nil, nil
		case 'F':
			return keyEnd, nil, nil
		case 'I':
			return keyFocusIn, nil, nil
		case 'O':
			return keyFocusOut, nil, nil
		case '<':
			event, err := readMouseEvent(reader)
			if err != nil {
				return 0, nil, err
			}
			event, err = coalesceMouseEvent(reader, stdin, event)
			if err != nil {
				return 0, nil, err
			}
			return keyMouse, &event, nil
		default:
			if b >= '0' && b <= '9' {
				seq := []byte{b}
				for reader.Buffered() > 0 {
					next, err := reader.ReadByte()
					if err != nil {
						return 0, nil, err
					}
					seq = append(seq, next)
					if next >= 0x40 && next <= 0x7e {
						return decodeCSIKey(seq), nil, nil
					}
				}
			}
			return keyEsc, nil, nil
		}
	case 'O':
		if ok, err := ensureBufferedByte(reader, stdin, escContinuationWait); err != nil {
			return 0, nil, err
		} else if !ok {
			return keyEsc, nil, nil
		}
		b, err = reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		switch b {
		case 'A':
			return keyUp, nil, nil
		case 'B':
			return keyDown, nil, nil
		case 'C':
			return keyRight, nil, nil
		case 'D':
			return keyLeft, nil, nil
		case 'H':
			return keyHome, nil, nil
		case 'F':
			return keyEnd, nil, nil
		}
	case 'b', 'f', 'v', 'w', 0x08, 0x7f:
		return metaKey(rune(b)), nil, nil
	}
	if b >= 0x20 && b <= 0x7e {
		return metaKey(rune(b)), nil, nil
	}
	return keyEsc, nil, nil
}

func decodeCSIKey(seq []byte) int {
	if len(seq) == 0 {
		return keyEsc
	}
	final := seq[len(seq)-1]
	switch final {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		return keyRight
	case 'D':
		return keyLeft
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	case '~':
		if key, ok := decodeModifiedOtherKey(seq[:len(seq)-1]); ok {
			return key
		}
		num := 0
		for _, b := range seq[:len(seq)-1] {
			if b < '0' || b > '9' {
				break
			}
			num = num*10 + int(b-'0')
		}
		switch num {
		case 1, 7:
			return keyHome
		case 3:
			return keyDel
		case 4, 8:
			return keyEnd
		case 5:
			return keyPgUp
		case 6:
			return keyPgDn
		case 200:
			return keyPaste
		}
	case 'u':
		if key, ok := decodeCSIUKey(seq[:len(seq)-1]); ok {
			return key
		}
	}
	return keyEsc
}

func decodeCSIUKey(body []byte) (int, bool) {
	params := splitCSIParams(body)
	if len(params) != 2 {
		return 0, false
	}
	if _, ok := parseCSIParamInt(params[0]); !ok {
		return 0, false
	}
	if _, ok := parseCSIParamInt(params[1]); !ok {
		return 0, false
	}
	return 0, false
}

func decodeModifiedOtherKey(body []byte) (int, bool) {
	params := splitCSIParams(body)
	if len(params) != 3 {
		return 0, false
	}
	if num, ok := parseCSIParamInt(params[0]); !ok || num != 27 {
		return 0, false
	}
	if _, ok := parseCSIParamInt(params[1]); !ok {
		return 0, false
	}
	if _, ok := parseCSIParamInt(params[2]); !ok {
		return 0, false
	}
	return 0, false
}

func splitCSIParams(body []byte) [][]byte {
	if len(body) == 0 {
		return nil
	}
	var params [][]byte
	start := 0
	for i, b := range body {
		if b != ';' {
			continue
		}
		params = append(params, body[start:i])
		start = i + 1
	}
	params = append(params, body[start:])
	return params
}

func parseCSIParamInt(param []byte) (int, bool) {
	if len(param) == 0 {
		return 0, false
	}
	n := 0
	for _, b := range param {
		if b < '0' || b > '9' {
			return 0, false
		}
		n = n*10 + int(b-'0')
	}
	return n, true
}

func readBufferKey(reader *bufio.Reader) (int, error) {
	key, _, err := readBufferEscape(reader, nil)
	return key, err
}

const keyMetaBase = 0x2000

func metaKey(r rune) int {
	return keyMetaBase + int(r)
}

func metaRune(key int) (rune, bool) {
	if key < keyMetaBase || key > keyMetaBase+0xff {
		return 0, false
	}
	return rune(key - keyMetaBase), true
}

func legacyAltKey(key int) int {
	r, ok := metaRune(key)
	if !ok {
		return key
	}
	switch r {
	case 'b':
		return keyAltLeft
	case 'f':
		return keyAltRight
	case 0x08, 0x7f:
		return keyAltBackspace
	default:
		return key
	}
}

func ensureBufferedByte(reader *bufio.Reader, stdin *os.File, timeoutUsec int64) (bool, error) {
	if reader.Buffered() > 0 {
		return true, nil
	}
	if stdin == nil {
		return false, nil
	}
	return waitForInputByte(stdin, timeoutUsec)
}

// Keep the initial ESC wait long enough to catch delayed terminal prefixes,
// but use a shorter continuation wait so fragmented arrow-key sequences do
// not pay the full timeout twice.
const escSequenceWait = 45_000     // 45ms in microseconds
const escContinuationWait = 35_000 // 35ms in microseconds
const (
	passiveMotionCoalesceWait = 20_000
)

func waitForInputByte(stdin *os.File, timeoutUsec int64) (bool, error) {
	if stdin == nil {
		return false, nil
	}
	fd := int(stdin.Fd())
	var readfds syscall.FdSet
	fdSetAdd(&readfds, fd)
	tv := timevalFromUsec(timeoutUsec)
	if err := selectRead(fd+1, &readfds, &tv); err != nil {
		if errors.Is(err, syscall.EINTR) {
			return false, nil
		}
		return false, err
	}
	return fdSetHas(&readfds, fd), nil
}

func readMouseEvent(reader *bufio.Reader) (mouseEvent, error) {
	button, err := readMouseNumber(reader)
	if err != nil {
		return mouseEvent{}, err
	}
	if _, err := reader.ReadByte(); err != nil {
		return mouseEvent{}, err
	}
	x, err := readMouseNumber(reader)
	if err != nil {
		return mouseEvent{}, err
	}
	if _, err := reader.ReadByte(); err != nil {
		return mouseEvent{}, err
	}
	y, err := readMouseNumber(reader)
	if err != nil {
		return mouseEvent{}, err
	}
	end, err := reader.ReadByte()
	if err != nil {
		return mouseEvent{}, err
	}
	return mouseEvent{
		button:  button,
		x:       x - 1,
		y:       y - 1,
		pressed: end == 'M',
	}, nil
}

func readMouseNumber(reader *bufio.Reader) (int, error) {
	n := 0
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if b == ';' || b == 'M' || b == 'm' {
			if err := reader.UnreadByte(); err != nil {
				return 0, err
			}
			return n, nil
		}
		n = n*10 + int(b-'0')
	}
}

func coalesceMouseEvent(reader *bufio.Reader, stdin *os.File, event mouseEvent) (mouseEvent, error) {
	if event.isMotion() {
		latest := event
		timeoutUsec := int64(0)
		if event.noButtonsDown() {
			timeoutUsec = passiveMotionCoalesceWait
		}
		for i := 0; i < 256; i++ {
			next, size, ok, err := peekMouseEvent(reader, stdin, timeoutUsec)
			if err != nil {
				return mouseEvent{}, err
			}
			if !ok || !next.isMotion() {
				return latest, nil
			}
			if _, err := reader.Discard(size); err != nil {
				return mouseEvent{}, err
			}
			latest = next
		}
		return latest, nil
	}
	return event, nil
}

func peekMouseEvent(reader *bufio.Reader, stdin *os.File, timeoutUsec int64) (mouseEvent, int, bool, error) {
	deadline := time.Time{}
	if timeoutUsec > 0 {
		deadline = time.Now().Add(time.Duration(timeoutUsec) * time.Microsecond)
	}
	for i := 0; i < 256; i++ {
		if reader.Buffered() == 0 {
			ok, err := waitForInputByte(stdin, remainingTimeoutUsec(deadline))
			if err != nil {
				return mouseEvent{}, 0, false, err
			}
			if !ok {
				return mouseEvent{}, 0, false, nil
			}
			if _, err := reader.Peek(1); err != nil {
				if errors.Is(err, io.EOF) {
					return mouseEvent{}, 0, false, nil
				}
				return mouseEvent{}, 0, false, err
			}
		}
		buf, err := reader.Peek(reader.Buffered())
		if err != nil {
			return mouseEvent{}, 0, false, err
		}
		event, size, ok, err := parseMouseEvent(buf)
		if err != nil {
			return mouseEvent{}, 0, false, err
		}
		if ok {
			return event, size, true, nil
		}
		if !isMouseEventPrefix(buf) {
			return mouseEvent{}, 0, false, nil
		}
		if stdin == nil {
			return mouseEvent{}, 0, false, nil
		}
		ok, err = waitForInputByte(stdin, remainingTimeoutUsec(deadline))
		if err != nil {
			return mouseEvent{}, 0, false, err
		}
		if !ok {
			return mouseEvent{}, 0, false, nil
		}
		if _, err := reader.Peek(reader.Buffered() + 1); err != nil {
			if errors.Is(err, io.EOF) {
				return mouseEvent{}, 0, false, nil
			}
			return mouseEvent{}, 0, false, err
		}
	}
	return mouseEvent{}, 0, false, nil
}

func remainingTimeoutUsec(deadline time.Time) int64 {
	if deadline.IsZero() {
		return 0
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	timeoutUsec := remaining.Microseconds()
	if timeoutUsec <= 0 {
		return 1
	}
	return timeoutUsec
}

func isMouseEventPrefix(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	prefix := []byte{0x1b, '[', '<'}
	for i := 0; i < len(buf) && i < len(prefix); i++ {
		if buf[i] != prefix[i] {
			return false
		}
	}
	if len(buf) <= len(prefix) {
		return true
	}
	field := 0
	sawDigit := false
	for i := len(prefix); i < len(buf); i++ {
		b := buf[i]
		switch {
		case b >= '0' && b <= '9':
			sawDigit = true
		case b == ';':
			if !sawDigit || field >= 2 {
				return false
			}
			field++
			sawDigit = false
		case b == 'M' || b == 'm':
			return field == 2 && sawDigit && i == len(buf)-1
		default:
			return false
		}
	}
	return true
}

func parseMouseEvent(buf []byte) (mouseEvent, int, bool, error) {
	if len(buf) < 6 || buf[0] != 0x1b || buf[1] != '[' || buf[2] != '<' {
		return mouseEvent{}, 0, false, nil
	}
	button, next, ok := parseMouseField(buf, 3, ';')
	if !ok {
		return mouseEvent{}, 0, false, nil
	}
	x, next, ok := parseMouseField(buf, next, ';')
	if !ok {
		return mouseEvent{}, 0, false, nil
	}
	y, next, ok := parseMouseField(buf, next, 'M', 'm')
	if !ok {
		return mouseEvent{}, 0, false, nil
	}
	end := buf[next-1]
	return mouseEvent{
		button:  button,
		x:       x - 1,
		y:       y - 1,
		pressed: end == 'M',
	}, next, true, nil
}

func parseMouseField(buf []byte, start int, terminators ...byte) (int, int, bool) {
	if start >= len(buf) {
		return 0, 0, false
	}
	n := 0
	for i := start; i < len(buf); i++ {
		b := buf[i]
		for _, term := range terminators {
			if b == term {
				return n, i + 1, true
			}
		}
		if b < '0' || b > '9' {
			return 0, 0, false
		}
		n = n*10 + int(b-'0')
	}
	return 0, 0, false
}

func handleMouseEvent(state *bufferState, overlay *overlayState, event mouseEvent, selecting *bool, selectStart *int) bool {
	if state == nil {
		return false
	}
	viewRows := bufferViewRows(overlay)
	if overlay != nil && overlay.visible && event.y >= viewRows {
		overlay.close()
		return true
	}
	if dir, ok := event.verticalWheelDirection(); ok {
		prevOrigin := state.origin
		lines := 3 * event.count()
		if dir < 0 {
			for i := 0; i < lines; i++ {
				next := prevVisualRowStart(state.text, state.origin)
				if next == state.origin {
					break
				}
				state.origin = next
			}
		} else {
			for i := 0; i < lines; i++ {
				next := nextVisualRowStart(state.text, state.origin)
				if next == state.origin {
					break
				}
				state.origin = next
			}
		}
		return state.origin != prevOrigin
	}
	pos, ok := screenToPos(state, overlay, event.y, event.x)
	if !ok {
		return false
	}
	if event.isMotion() {
		if !*selecting {
			return false
		}
		state.cursor = pos
		state.markMode = false
		updateMouseSelection(state, *selectStart, pos)
		if event.noButtonsDown() {
			*selecting = false
		}
		return true
	}
	if event.baseButton() != 0 {
		return false
	}
	if event.pressed {
		*selecting = true
		*selectStart = pos
		state.cursor = pos
		state.markMode = false
		state.dotStart = pos
		state.dotEnd = pos
		return true
	}
	if !*selecting {
		return true
	}
	*selecting = false
	state.cursor = pos
	state.markMode = false
	updateMouseSelection(state, *selectStart, pos)
	return true
}

func updateMouseSelection(state *bufferState, start, end int) {
	if start <= end {
		state.dotStart = start
		state.dotEnd = end
		return
	}
	state.dotStart = end
	state.dotEnd = start
}

func screenToPos(state *bufferState, overlay *overlayState, row, col int) (int, bool) {
	if state == nil {
		return 0, false
	}
	if row < 0 || row >= bufferViewRows(overlay) {
		return 0, false
	}
	if col < 0 {
		col = 0
	}
	layout := state.visibleLayout(overlay)
	if layout == nil || len(layout.rows) == 0 {
		return 0, true
	}
	if row >= len(layout.rows) {
		row = len(layout.rows) - 1
	}
	return layout.rows[row].posAtColumn(col), true
}
