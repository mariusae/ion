package term

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"ion/internal/client/download"
	"ion/internal/core/cmdlang"
	"ion/internal/proto/wire"
)

const (
	bufferRows = 24
	bufferCols = 80

	keyEsc = 0x1000 + iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyPgUp
	keyPgDn
	keyDel
	keyPaste
	keyAltLeft
	keyAltRight
	keyAltSnarf
	keyAltBackspace
)

type bufferState struct {
	name     string
	text     []rune
	cursor   int
	origin   int
	dotStart int
	dotEnd   int
	markMode bool
	markPos  int
	status   string
}

// Run executes the initial terminal-client slice.
//
// For now this shares the command-mode loop with the download client while the
// full terminal UI from term.c is ported behind this package boundary.
func Run(files []string, stdin io.Reader, stdout, stderr io.Writer, svc wire.TermService, capture *OutputCapture) error {
	inFile, ok := stdin.(*os.File)
	if !ok || !isTTY(inFile) {
		return download.Run(files, stdin, stderr, svc)
	}
	if err := svc.Bootstrap(files); err != nil {
		return err
	}
	return runTTY(inFile, stdout, stderr, svc, capture)
}

func runTTY(stdin *os.File, stdout, stderr io.Writer, svc wire.TermService, capture *OutputCapture) error {
	state, err := enterCBreakMode(stdin)
	if err != nil {
		return err
	}
	defer state.restore()

	parser := cmdlang.NewParserRunes(nil)
	reader := bufio.NewReader(stdin)
	var pending []rune
	var linebuf []rune
	inBufferMode := false
	var buffer *bufferState
	var snarf []rune
	overlay := newOverlayState()

	executePending := func(final bool) (bool, error) {
		for {
			parser.ResetRunes(pending)
			cmd, err := parser.ParseWithFinal(final)
			if err != nil {
				if errors.Is(err, cmdlang.ErrNeedMoreInput) {
					return false, nil
				}
				if _, werr := fmt.Fprintf(stderr, "?%v\n", err); werr != nil {
					return false, werr
				}
				if !final {
					pending = discardFailedCommand(pending)
				} else {
					pending = nil
				}
				return false, nil
			}

			consumed := parser.Consumed()
			if consumed > 0 {
				pending = pending[consumed:]
			}
			if cmd == nil {
				return false, nil
			}

			ok, err := svc.Execute(cmd)
			if err != nil {
				if _, werr := fmt.Fprintf(stderr, "?%v\n", err); werr != nil {
					return false, werr
				}
				continue
			}
			if !ok {
				return true, nil
			}
		}
	}

	submitLine := func() (bool, error) {
		if _, err := io.WriteString(stdout, "\n"); err != nil {
			return false, err
		}
		pending = append(pending, linebuf...)
		pending = append(pending, '\n')
		linebuf = linebuf[:0]
		return executePending(false)
	}

	eraseLast := func() error {
		if len(linebuf) == 0 {
			return nil
		}
		linebuf = linebuf[:len(linebuf)-1]
		_, err := io.WriteString(stdout, "\b \b")
		return err
	}

	ctrlC := func() (bool, error) {
		if _, err := io.WriteString(stdout, "^C\n"); err != nil {
			return false, err
		}
		linebuf = linebuf[:0]
		pending = append(pending, '\n')
		return executePending(false)
	}

	refreshBuffer := func() error {
		view, err := svc.CurrentView()
		if err != nil {
			if overlay.visible {
				overlay.addOutput("?" + err.Error())
				return nil
			}
			return err
		}
		buffer = newBufferState(view)
		return nil
	}

	submitOverlay := func() (bool, error) {
		line := string(overlay.input)
		if len(overlay.input) == 0 {
			last, ok := overlay.lastCommand()
			if !ok {
				return false, nil
			}
			line = last
		} else {
			overlay.addCommand(line)
		}
		pending = append(pending, []rune(line)...)
		pending = append(pending, '\n')
		overlay.resetInput()
		if capture != nil {
			capture.Start(func(line string) {
				overlay.addOutput(line)
			})
		}
		done, err := executePending(false)
		if capture != nil {
			capture.Stop()
		}
		if err != nil {
			return false, err
		}
		if !done {
			if err := refreshBuffer(); err != nil {
				return false, err
			}
		}
		return done, nil
	}

	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				pending = append(pending, linebuf...)
				_, err := executePending(true)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		if inBufferMode {
			if overlay.visible {
				if r == 0x1b {
					key, err := readBufferKey(reader)
					if err != nil {
						return err
					}
					switch key {
					case keyEsc:
						overlay.close()
					case keyPaste:
						paste, err := readBracketedPaste(reader)
						if err != nil {
							return err
						}
						filtered := paste[:0]
						for _, pr := range paste {
							if pr == '\r' || pr == '\n' {
								continue
							}
							filtered = append(filtered, pr)
						}
						overlay.insert(filtered)
					case keyLeft:
						overlay.moveLeft()
					case keyRight:
						overlay.moveRight()
					case keyHome:
						overlay.moveHome()
					case keyEnd:
						overlay.moveEnd()
					case keyUp:
						overlay.recallPrev()
					case keyDown:
						overlay.recallNext()
					case keyDel:
						overlay.deleteForward()
					}
					if err := drawBufferMode(stdout, buffer, overlay); err != nil {
						return err
					}
					continue
				}
				switch r {
				case '\r':
					done, err := submitOverlay()
					if err != nil {
						return err
					}
					if done {
						if err := exitBufferMode(stdout); err != nil {
							return err
						}
						return nil
					}
				case '\n':
					overlay.close()
				case 0x7f, 0x08:
					overlay.backspace()
				case 0x04:
					overlay.deleteForward()
				case 0x02:
					overlay.moveLeft()
				case 0x06:
					overlay.moveRight()
				case 0x01:
					overlay.moveHome()
				case 0x05:
					overlay.moveEnd()
				case 0x10:
					overlay.recallPrev()
				case 0x0e:
					overlay.recallNext()
				case 0x15:
					overlay.killLine()
				case 0x17:
					overlay.killWord()
				case 0x0b:
					overlay.clearHistory()
				default:
					if r >= 32 || r == '\t' {
						overlay.insert([]rune{r})
					}
				}
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			}
			if r == 0x1b {
				key, err := readBufferKey(reader)
				if err != nil {
					return err
				}
				if key == keyEsc {
					if _, err := svc.SetDot(buffer.dotStart, buffer.dotEnd); err != nil {
						return err
					}
					if err := exitBufferMode(stdout); err != nil {
						return err
					}
					inBufferMode = false
					buffer = nil
					overlay.close()
					continue
				}
				switch key {
				case keyAltSnarf:
					snarf = snarfSelection(buffer)
					if len(snarf) != 0 {
						buffer.status = "snarfed"
					}
					if err := drawBufferMode(stdout, buffer, overlay); err != nil {
						return err
					}
					continue
				case keyPaste:
					paste, err := readBracketedPaste(reader)
					if err != nil {
						return err
					}
					if len(paste) == 0 {
						continue
					}
					buffer, err = replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, string(paste))
					if err != nil {
						return err
					}
					buffer.status = ""
					if err := drawBufferMode(stdout, buffer, overlay); err != nil {
						return err
					}
					continue
				}
				buffer, err = applyBufferKey(svc, buffer, key)
				if err != nil {
					return err
				}
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			}
			switch r {
			case '\n':
				overlay.open("")
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			case 0x18:
				snarf = snarfSelection(buffer)
				if len(snarf) == 0 {
					continue
				}
				buffer, err = replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, "")
				if err != nil {
					return err
				}
				buffer.status = "cut"
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			case 0x19:
				if len(snarf) == 0 {
					continue
				}
				buffer, err = replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, string(snarf))
				if err != nil {
					return err
				}
				buffer.status = ""
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			case 0x11:
				if _, err := svc.SetDot(buffer.dotStart, buffer.dotEnd); err != nil {
					return err
				}
				if err := exitBufferMode(stdout); err != nil {
					return err
				}
				inBufferMode = false
				buffer = nil
				overlay.close()
				pending = append(pending, []rune("q\n")...)
				done, err := executePending(false)
				if err != nil {
					return err
				}
				if done {
					return nil
				}
				continue
			case 0x17:
				if strings.TrimSpace(buffer.name) == "" {
					if _, err := svc.SetDot(buffer.dotStart, buffer.dotEnd); err != nil {
						return err
					}
					if err := exitBufferMode(stdout); err != nil {
						return err
					}
					inBufferMode = false
					buffer = nil
					overlay.close()
					pending = append(pending, []rune("w ")...)
					continue
				}
				msg, err := svc.Save()
				if err != nil {
					buffer.status = "?" + err.Error()
				} else {
					buffer.status = msg
				}
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			case 0x1f:
				overlay.open("/")
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			case 0x0c:
				next, ok, err := lookInBuffer(svc, buffer, true)
				if err != nil {
					return err
				}
				if ok {
					buffer = next
					buffer.status = ""
				} else {
					buffer.status = "?no match"
				}
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			case 0x12:
				next, ok, err := lookInBuffer(svc, buffer, false)
				if err != nil {
					return err
				}
				if ok {
					buffer = next
					buffer.status = ""
				} else {
					buffer.status = "?no match"
				}
				if err := drawBufferMode(stdout, buffer, overlay); err != nil {
					return err
				}
				continue
			}
			if r == '\r' {
				r = '\n'
			}
			buffer, err = applyBufferKey(svc, buffer, int(r))
			if err != nil {
				return err
			}
			if err := drawBufferMode(stdout, buffer, overlay); err != nil {
				return err
			}
			continue
		}
		if r == 0x1b {
			overlay.close()
			next, err := enterBufferMode(stdout, svc, overlay)
			if err != nil {
				return err
			}
			buffer = next
			inBufferMode = true
			continue
		}

		switch r {
		case '\n', '\r':
			done, err := submitLine()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case 0x7f, 0x08:
			if err := eraseLast(); err != nil {
				return err
			}
		case 0x15:
			for len(linebuf) > 0 {
				if err := eraseLast(); err != nil {
					return err
				}
			}
		case 0x17:
			for len(linebuf) > 0 && linebuf[len(linebuf)-1] == ' ' {
				if err := eraseLast(); err != nil {
					return err
				}
			}
			for len(linebuf) > 0 && linebuf[len(linebuf)-1] != ' ' {
				if err := eraseLast(); err != nil {
					return err
				}
			}
		case 0x03:
			done, err := ctrlC()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case 0x04:
			if len(linebuf) != 0 {
				break
			}
			pending = append(pending, linebuf...)
			done, err := executePending(true)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			return nil
		default:
			if r >= 32 || r == '\t' {
				linebuf = append(linebuf, r)
				if _, err := io.WriteString(stdout, string(r)); err != nil {
					return err
				}
			}
		}
	}
}

func enterBufferMode(stdout io.Writer, svc wire.TermService, overlay *overlayState) (*bufferState, error) {
	view, err := svc.CurrentView()
	if err != nil {
		return nil, err
	}
	state := newBufferState(view)
	if err := drawBufferMode(stdout, state, overlay); err != nil {
		return nil, err
	}
	return state, nil
}

func exitBufferMode(stdout io.Writer) error {
	_, err := io.WriteString(stdout, "\x1b[?1049l")
	return err
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func discardFailedCommand(pending []rune) []rune {
	for i, r := range pending {
		if r == '\n' {
			return pending[i+1:]
		}
	}
	return nil
}

func newBufferState(view wire.BufferView) *bufferState {
	text := []rune(view.Text)
	cursor := clampIndex(view.DotStart, len(text))
	dotEnd := clampIndex(view.DotEnd, len(text))
	origin := lineStart(text, cursor)
	return &bufferState{
		name:     view.Name,
		text:     text,
		cursor:   cursor,
		origin:   origin,
		dotStart: clampIndex(view.DotStart, len(text)),
		dotEnd:   dotEnd,
	}
}

func applyBufferKey(svc wire.TermService, state *bufferState, key int) (*bufferState, error) {
	if state == nil {
		return state, nil
	}
	switch key {
	case 0:
		if state.markMode {
			state.markMode = false
		} else {
			state.markMode = true
			state.markPos = state.cursor
		}
		updateSelection(state)
		return state, nil
	case 8, 127:
		if state.dotStart != state.dotEnd {
			return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, "")
		}
		if state.cursor == 0 {
			return state, nil
		}
		return replaceBufferRange(svc, state, state.cursor-1, state.cursor, "")
	case keyDel:
		if state.dotStart != state.dotEnd {
			return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, "")
		}
		if state.cursor >= len(state.text) {
			return state, nil
		}
		return replaceBufferRange(svc, state, state.cursor, state.cursor+1, "")
	case keyAltBackspace:
		if state.dotStart != state.dotEnd {
			return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, "")
		}
		start := prevWordStart(state.text, state.cursor)
		if start == state.cursor {
			return state, nil
		}
		return replaceBufferRange(svc, state, start, state.cursor, "")
	case 21, 26:
		view, err := svc.Undo()
		if err != nil {
			return nil, err
		}
		return newBufferState(view), nil
	case 11:
		if state.dotStart != state.dotEnd {
			return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, "")
		}
		end := lineEnd(state.text, state.cursor)
		if state.cursor < end {
			return replaceBufferRange(svc, state, state.cursor, end, "")
		}
		if state.cursor < len(state.text) {
			return replaceBufferRange(svc, state, state.cursor, state.cursor+1, "")
		}
		return state, nil
	case '\t', '\n':
		return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, string(rune(key)))
	default:
		if key >= 32 && key < keyEsc {
			return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, string(rune(key)))
		}
		handleBufferKey(state, key)
		return state, nil
	}
}

func replaceBufferRange(svc wire.TermService, state *bufferState, start, end int, repl string) (*bufferState, error) {
	view, err := svc.Replace(start, end, repl)
	if err != nil {
		return nil, err
	}
	next := newBufferState(view)
	next.status = state.status
	return next, nil
}

func drawBufferMode(stdout io.Writer, state *bufferState, overlay *overlayState) error {
	if state == nil {
		return nil
	}
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[2J"); err != nil {
		return err
	}
	viewRows := bufferRows
	if overlay != nil && overlay.visible {
		viewRows -= overlayRows
	}
	p := state.origin
	for row := 0; row < viewRows; row++ {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
			return err
		}
		if p <= len(state.text) {
			lineEndPos := lineEnd(state.text, p)
			if err := drawBufferLine(stdout, state, p, lineEndPos); err != nil {
				return err
			}
			if p < len(state.text) {
				next := nextLineStart(state.text, p)
				if next != p {
					p = next
					continue
				}
			}
			p = len(state.text) + 1
		}
	}
	for row := viewRows + 1; row <= bufferRows; row++ {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row); err != nil {
			return err
		}
	}
	if overlay != nil && overlay.visible {
		lines := overlay.renderLines(overlayRows - 1)
		startRow := viewRows + 1
		for i, line := range lines {
			if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H", startRow+i); err != nil {
				return err
			}
			if err := drawOverlayText(stdout, line); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H", bufferRows); err != nil {
			return err
		}
		if err := drawOverlayPrompt(stdout, overlay); err != nil {
			return err
		}
		return nil
	}
	if state.status != "" {
		if _, err := io.WriteString(stdout, "\x1b[24;1H"); err != nil {
			return err
		}
		status := []rune(state.status)
		if len(status) > bufferCols {
			status = status[:bufferCols]
		}
		if err := drawOverlayText(stdout, string(status)); err != nil {
			return err
		}
	}
	return nil
}

func drawBufferLine(stdout io.Writer, state *bufferState, start, end int) error {
	col := 0
	for p := start; p < end && col < bufferCols; p++ {
		selected := p >= state.dotStart && p < state.dotEnd
		if selected {
			if _, err := io.WriteString(stdout, "\x1b[7m"); err != nil {
				return err
			}
		}
		if p == state.cursor && state.dotStart == state.dotEnd {
			if _, err := io.WriteString(stdout, "\x1b[7m"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(stdout, string(state.text[p])); err != nil {
			return err
		}
		if selected || (p == state.cursor && state.dotStart == state.dotEnd) {
			if _, err := io.WriteString(stdout, "\x1b[27m"); err != nil {
				return err
			}
		}
		col++
	}
	if state.cursor == end && state.dotStart == state.dotEnd && col < bufferCols {
		if _, err := io.WriteString(stdout, "\x1b[7m \x1b[27m"); err != nil {
			return err
		}
	}
	return nil
}

func drawOverlayText(stdout io.Writer, text string) error {
	line := []rune(text)
	if len(line) > bufferCols {
		line = line[:bufferCols]
	}
	_, err := io.WriteString(stdout, string(line))
	return err
}

func drawOverlayPrompt(stdout io.Writer, overlay *overlayState) error {
	if overlay == nil {
		return nil
	}
	if _, err := io.WriteString(stdout, ": "); err != nil {
		return err
	}
	col := 2
	for _, r := range []rune(overlay.promptLine()) {
		if col >= bufferCols {
			break
		}
		if r == 0 {
			if _, err := io.WriteString(stdout, "\x1b[7m \x1b[27m"); err != nil {
				return err
			}
			col++
			continue
		}
		if _, err := io.WriteString(stdout, string(r)); err != nil {
			return err
		}
		col++
	}
	return nil
}

func handleBufferKey(state *bufferState, key int) {
	if state == nil {
		return
	}
	switch key {
	case 0:
		if state.markMode {
			state.markMode = false
		} else {
			state.markMode = true
			state.markPos = state.cursor
		}
	case keyUp, keyPgUp:
		state.cursor = movePageUp(state.text, state.cursor, bufferRows)
		state.origin = lineStart(state.text, state.cursor)
	case keyDown, keyPgDn:
		state.cursor = movePageDown(state.text, state.cursor, bufferRows)
		state.origin = lineStart(state.text, state.cursor)
	case 16:
		state.cursor = moveLineUp(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case 14:
		state.cursor = moveLineDown(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case keyHome, 1:
		state.cursor = lineStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case keyEnd, 5:
		state.cursor = lineEnd(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case keyLeft, 2:
		if state.cursor > 0 {
			state.cursor--
		}
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case keyRight, 6:
		if state.cursor < len(state.text) {
			state.cursor++
		}
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case 22:
		state.cursor = movePageDown(state.text, state.cursor, bufferRows)
		state.origin = lineStart(state.text, state.cursor)
	case keyAltLeft:
		state.cursor = prevWordStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case keyAltRight:
		state.cursor = nextWordStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	}
	updateSelection(state)
}

func readBufferKey(reader *bufio.Reader) (int, error) {
	if reader.Buffered() == 0 {
		return keyEsc, nil
	}
	b, err := reader.ReadByte()
	if err != nil {
		return 0, err
	}
	switch b {
	case '[':
		if reader.Buffered() == 0 {
			return keyEsc, nil
		}
		b, err = reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case 'A':
			return keyUp, nil
		case 'B':
			return keyDown, nil
		case 'C':
			return keyRight, nil
		case 'D':
			return keyLeft, nil
		case 'H':
			return keyHome, nil
		case 'F':
			return keyEnd, nil
		default:
			if b >= '0' && b <= '9' {
				num := int(b - '0')
				for reader.Buffered() > 0 {
					next, err := reader.ReadByte()
					if err != nil {
						return 0, err
					}
					if next >= '0' && next <= '9' {
						num = num*10 + int(next-'0')
						continue
					}
					if next == '~' {
						switch num {
						case 3:
							return keyDel, nil
						case 5:
							return keyPgUp, nil
						case 6:
							return keyPgDn, nil
						case 200:
							return keyPaste, nil
						}
					}
					break
				}
			}
			return keyEsc, nil
		}
	case 'O':
		if reader.Buffered() == 0 {
			return keyEsc, nil
		}
		b, err = reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case 'H':
			return keyHome, nil
		case 'F':
			return keyEnd, nil
		}
	case 'b':
		return keyAltLeft, nil
	case 'f':
		return keyAltRight, nil
	case 'w':
		return keyAltSnarf, nil
	case 0x08, 0x7f:
		return keyAltBackspace, nil
	}
	return keyEsc, nil
}

func movePageUp(text []rune, pos, rows int) int {
	for i := 0; i < rows; i++ {
		next := prevLineStart(text, pos)
		if next == pos {
			break
		}
		pos = next
	}
	return pos
}

func movePageDown(text []rune, pos, rows int) int {
	for i := 0; i < rows; i++ {
		next := nextLineStart(text, pos)
		if next == pos {
			break
		}
		pos = next
	}
	return pos
}

func adjustOriginForCursor(text []rune, origin, cursor, rows int) int {
	if cursor < origin {
		return lineStart(text, cursor)
	}
	p := origin
	for i := 0; i < rows; i++ {
		if cursor >= p && cursor <= lineEnd(text, p) {
			return origin
		}
		next := nextLineStart(text, p)
		if next == p {
			break
		}
		p = next
	}
	return lineStart(text, cursor)
}

func moveLineUp(text []rune, pos int) int {
	start := lineStart(text, pos)
	if start == 0 {
		return pos
	}
	col := pos - start
	prev := prevLineStart(text, pos)
	return linePosAtColumn(text, prev, col)
}

func moveLineDown(text []rune, pos int) int {
	start := lineStart(text, pos)
	col := pos - start
	next := nextLineStart(text, pos)
	if next == start {
		return pos
	}
	return linePosAtColumn(text, next, col)
}

func linePosAtColumn(text []rune, start, col int) int {
	end := lineEnd(text, start)
	pos := start + col
	if pos > end {
		return end
	}
	return pos
}

func lineStart(text []rune, pos int) int {
	pos = clampIndex(pos, len(text))
	for pos > 0 && text[pos-1] != '\n' {
		pos--
	}
	return pos
}

func lineEnd(text []rune, pos int) int {
	pos = clampIndex(pos, len(text))
	for pos < len(text) && text[pos] != '\n' {
		pos++
	}
	return pos
}

func nextLineStart(text []rune, pos int) int {
	end := lineEnd(text, pos)
	if end < len(text) {
		return end + 1
	}
	return end
}

func prevLineStart(text []rune, pos int) int {
	start := lineStart(text, pos)
	if start == 0 {
		return 0
	}
	return lineStart(text, start-1)
}

func clampIndex(n, max int) int {
	if n < 0 {
		return 0
	}
	if n > max {
		return max
	}
	return n
}

func snarfSelection(state *bufferState) []rune {
	if state == nil || state.dotEnd <= state.dotStart {
		return nil
	}
	return append([]rune(nil), state.text[state.dotStart:state.dotEnd]...)
}

func readBracketedPaste(reader *bufio.Reader) ([]rune, error) {
	var out []rune
	var pending []rune
	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			return nil, err
		}
		switch len(pending) {
		case 0:
			if r == 0x1b {
				pending = append(pending, r)
				continue
			}
			out = append(out, r)
		case 1:
			if r == '[' {
				pending = append(pending, r)
				continue
			}
			out = append(out, pending...)
			pending = pending[:0]
			out = append(out, r)
		case 2:
			if r == '2' {
				pending = append(pending, r)
				continue
			}
			out = append(out, pending...)
			pending = pending[:0]
			out = append(out, r)
		case 3:
			if r == '0' {
				pending = append(pending, r)
				continue
			}
			out = append(out, pending...)
			pending = pending[:0]
			out = append(out, r)
		case 4:
			if r == '1' {
				pending = append(pending, r)
				continue
			}
			out = append(out, pending...)
			pending = pending[:0]
			out = append(out, r)
		case 5:
			if r == '~' {
				return out, nil
			}
			out = append(out, pending...)
			pending = pending[:0]
			out = append(out, r)
		}
	}
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '_'
}

func prevWordStart(text []rune, pos int) int {
	pos = clampIndex(pos, len(text))
	for pos > 0 && !isWordRune(text[pos-1]) {
		pos--
	}
	for pos > 0 && isWordRune(text[pos-1]) {
		pos--
	}
	return pos
}

func nextWordStart(text []rune, pos int) int {
	pos = clampIndex(pos, len(text))
	for pos < len(text) && isWordRune(text[pos]) {
		pos++
	}
	for pos < len(text) && !isWordRune(text[pos]) {
		pos++
	}
	return pos
}

func updateSelection(state *bufferState) {
	if state == nil {
		return
	}
	if !state.markMode {
		state.dotStart = state.cursor
		state.dotEnd = state.cursor
		return
	}
	if state.cursor < state.markPos {
		state.dotStart = state.cursor
		state.dotEnd = state.markPos
		return
	}
	state.dotStart = state.markPos
	state.dotEnd = state.cursor
}

func lookInBuffer(svc wire.TermService, state *bufferState, forward bool) (*bufferState, bool, error) {
	if state == nil || state.dotEnd <= state.dotStart {
		return state, false, nil
	}
	target := append([]rune(nil), state.text[state.dotStart:state.dotEnd]...)
	start, ok := findSelection(state.text, state.dotStart, state.dotEnd, target, forward)
	if !ok {
		return state, false, nil
	}
	view, err := svc.SetDot(start, start+len(target))
	if err != nil {
		return nil, false, err
	}
	next := newBufferState(view)
	next.status = state.status
	return next, true, nil
}

func findSelection(text []rune, start, end int, target []rune, forward bool) (int, bool) {
	if len(target) == 0 || len(text) < len(target) {
		return 0, false
	}
	if forward {
		for pos := end; pos <= len(text)-len(target); pos++ {
			if hasRunesAt(text, pos, target) {
				return pos, true
			}
		}
		for pos := 0; pos < start; pos++ {
			if hasRunesAt(text, pos, target) {
				return pos, true
			}
		}
		return 0, false
	}
	for pos := len(text) - len(target); pos > start; pos-- {
		if hasRunesAt(text, pos, target) {
			return pos, true
		}
	}
	for pos := start - 1; pos >= 0; pos-- {
		if hasRunesAt(text, pos, target) {
			return pos, true
		}
	}
	return 0, false
}

func hasRunesAt(text []rune, pos int, target []rune) bool {
	if pos < 0 || pos+len(target) > len(text) {
		return false
	}
	for i, r := range target {
		if text[pos+i] != r {
			return false
		}
	}
	return true
}
