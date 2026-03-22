package term

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

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
)

type bufferState struct {
	text     []rune
	cursor   int
	origin   int
	dotStart int
	dotEnd   int
}

// Run executes the initial terminal-client slice.
//
// For now this shares the command-mode loop with the download client while the
// full terminal UI from term.c is ported behind this package boundary.
func Run(files []string, stdin io.Reader, stdout, stderr io.Writer, svc wire.TermService) error {
	inFile, ok := stdin.(*os.File)
	if !ok || !isTTY(inFile) {
		return download.Run(files, stdin, stderr, svc)
	}
	if err := svc.Bootstrap(files); err != nil {
		return err
	}
	return runTTY(inFile, stdout, stderr, svc)
}

func runTTY(stdin *os.File, stdout, stderr io.Writer, svc wire.TermService) error {
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
		if r == '\r' {
			r = '\n'
		}

		if inBufferMode {
			if r == 0x1b {
				key, err := readBufferKey(reader)
				if err != nil {
					return err
				}
				if key == keyEsc {
					if err := exitBufferMode(stdout); err != nil {
						return err
					}
					inBufferMode = false
					buffer = nil
					continue
				}
				handleBufferKey(buffer, key)
				if err := drawBufferMode(stdout, buffer); err != nil {
					return err
				}
				continue
			}
			handleBufferKey(buffer, int(r))
			if err := drawBufferMode(stdout, buffer); err != nil {
				return err
			}
			continue
		}
		if r == 0x1b {
			next, err := enterBufferMode(stdout, svc)
			if err != nil {
				return err
			}
			buffer = next
			inBufferMode = true
			continue
		}

		switch r {
		case '\n':
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

func enterBufferMode(stdout io.Writer, svc wire.TermService) (*bufferState, error) {
	view, err := svc.CurrentView()
	if err != nil {
		return nil, err
	}
	state := newBufferState(view)
	if err := drawBufferMode(stdout, state); err != nil {
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
		text:     text,
		cursor:   cursor,
		origin:   origin,
		dotStart: cursor,
		dotEnd:   dotEnd,
	}
}

func drawBufferMode(stdout io.Writer, state *bufferState) error {
	if state == nil {
		return nil
	}
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[2J\x1b[H"); err != nil {
		return err
	}
	p := state.origin
	for row := 0; row < bufferRows; row++ {
		if p > len(state.text) {
			break
		}
		lineEndPos := lineEnd(state.text, p)
		if err := drawBufferLine(stdout, state, p, lineEndPos); err != nil {
			return err
		}
		if row != bufferRows-1 {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				return err
			}
		}
		if p >= len(state.text) {
			break
		}
		next := nextLineStart(state.text, p)
		if next == p {
			break
		}
		p = next
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

func handleBufferKey(state *bufferState, key int) {
	if state == nil {
		return
	}
	switch key {
	case keyUp, keyPgUp:
		state.cursor = movePageUp(state.text, state.cursor, bufferRows)
		state.origin = lineStart(state.text, state.cursor)
	case keyDown, keyPgDn:
		state.cursor = movePageDown(state.text, state.cursor, bufferRows)
		state.origin = lineStart(state.text, state.cursor)
	case keyHome:
		state.cursor = 0
		state.origin = 0
	case keyEnd:
		state.cursor = len(state.text)
		state.origin = lastPageOrigin(state.text, bufferRows)
	case keyLeft:
		if state.cursor > 0 {
			state.cursor--
		}
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	case keyRight:
		if state.cursor < len(state.text) {
			state.cursor++
		}
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, bufferRows)
	}
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
		case '5':
			if reader.Buffered() > 0 {
				_, _ = reader.ReadByte()
			}
			return keyPgUp, nil
		case '6':
			if reader.Buffered() > 0 {
				_, _ = reader.ReadByte()
			}
			return keyPgDn, nil
		default:
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

func lastPageOrigin(text []rune, rows int) int {
	pos := len(text)
	if pos > 0 {
		pos = lineStart(text, pos)
	}
	for i := 1; i < rows && pos > 0; i++ {
		next := prevLineStart(text, pos)
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
