package term

import "bufio"

type mouseEvent struct {
	button  int
	x       int
	y       int
	pressed bool
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

func readBufferEscape(reader *bufio.Reader) (int, *mouseEvent, error) {
	if reader.Buffered() == 0 {
		return keyEsc, nil, nil
	}
	b, err := reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	switch b {
	case '[':
		if reader.Buffered() == 0 {
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
		if reader.Buffered() == 0 {
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
	case 'b':
		return keyAltLeft, nil, nil
	case 'f':
		return keyAltRight, nil, nil
	case 'v':
		return keyAltPageUp, nil, nil
	case 'w':
		return keyAltSnarf, nil, nil
	case 0x08, 0x7f:
		return keyAltBackspace, nil, nil
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
	}
	return keyEsc
}

func readBufferKey(reader *bufio.Reader) (int, error) {
	key, _, err := readBufferEscape(reader)
	return key, err
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

func handleMouseEvent(state *bufferState, overlay *overlayState, event mouseEvent, selecting *bool, selectStart *int) bool {
	if state == nil {
		return false
	}
	viewRows := bufferViewRows(overlay)
	if overlay != nil && overlay.visible && event.y >= viewRows {
		overlay.close()
		return true
	}
	switch event.button {
	case 64:
		for i := 0; i < 3; i++ {
			next := prevVisualRowStart(state.text, state.origin)
			if next == state.origin {
				break
			}
			state.origin = next
		}
		return true
	case 65:
		for i := 0; i < 3; i++ {
			next := nextVisualRowStart(state.text, state.origin)
			if next == state.origin {
				break
			}
			state.origin = next
		}
		return true
	}
	pos, ok := screenToPos(state, overlay, event.y, event.x)
	if !ok {
		return false
	}
	if event.button >= 32 && event.button < 64 {
		if !*selecting {
			return false
		}
		state.cursor = pos
		state.markMode = false
		updateMouseSelection(state, *selectStart, pos)
		return true
	}
	if event.button&3 != 0 {
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
