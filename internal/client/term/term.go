package term

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ion/internal/core/cmdlang"
	"ion/internal/proto/wire"
)

const (
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
	keyMouse
	keyAltLeft
	keyAltRight
	keyAltPageUp
	keyAltSnarf
	keyAltBackspace
)

var (
	termRows = 24
	termCols = 80
)

type bufferState struct {
	name           string
	text           []rune
	cursor         int
	origin         int
	dotStart       int
	dotEnd         int
	flashSelection bool
	markMode       bool
	markPos        int
	status         string
}

type diagnosticReporter interface {
	Diagnostic() string
}

// Run executes the initial terminal-client slice.
//
// For now this shares the command-mode loop with the download client while the
// full terminal UI from term.c is ported behind this package boundary.
func Run(files []string, stdin io.Reader, stdout, stderr io.Writer, svc wire.TermService, capture *OutputCapture) error {
	inFile, ok := stdin.(*os.File)
	if !ok || !isTTY(inFile) {
		return fmt.Errorf("terminal mode requires a tty; use ion -d for command mode")
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
	refreshTerminalSize := func() {
		rows, cols, err := terminalSize(stdin)
		if err != nil {
			return
		}
		if rows > 0 {
			termRows = rows
		}
		if cols > 0 {
			termCols = cols
		}
	}
	refreshTerminalSize()
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)

	parser := cmdlang.NewParserRunes(nil)
	theme, prefetched := detectTerminalTheme(stdin, stdout)
	reader := bufio.NewReader(io.MultiReader(bytes.NewReader(prefetched), stdin))
	var pending []rune
	var linebuf []rune
	inBufferMode := true
	var snarf []rune
	mouseSelecting := false
	mouseSelectStart := 0
	lastMouseClick := time.Time{}
	lastMouseClickPos := -1
	overlay := newOverlayState()
	menu := newMenuState()
	menuLastItem := -1
	buffer, err := enterBufferMode(stdout, svc, overlay, menu, theme)
	if err != nil {
		return err
	}
	defer exitBufferMode(stdout)

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
			script := string(pending[:consumed])
			if consumed > 0 {
				pending = pending[consumed:]
			}
			if cmd == nil {
				return false, nil
			}

			ok, err := svc.Execute(script)
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
				overlay.addOutput(diagnosticText(err))
				return nil
			}
			return err
		}
		buffer = newBufferState(view)
		return nil
	}

	redraw := func() error {
		refreshTerminalSize()
		return drawBufferMode(stdout, buffer, overlay, menu, theme)
	}

	flashBufferSelection := func() error {
		if buffer == nil || buffer.dotStart == buffer.dotEnd {
			return nil
		}
		buffer.flashSelection = true
		if err := redraw(); err != nil {
			buffer.flashSelection = false
			return err
		}
		time.Sleep(80 * time.Millisecond)
		buffer.flashSelection = false
		return redraw()
	}

	executeDirect := func(line string, captureOutput bool) (bool, []string, error) {
		parser.ResetRunes([]rune(line))
		cmd, err := parser.ParseWithFinal(true)
		if err != nil {
			return false, nil, err
		}
		if cmd == nil {
			return false, nil, nil
		}
		var lines []string
		if capture != nil && captureOutput {
			capture.Start(func(line string) {
				lines = append(lines, line)
			})
		}
		ok, err := svc.Execute(line)
		if capture != nil && captureOutput {
			capture.Stop()
		}
		if err != nil {
			return false, lines, err
		}
		return !ok, lines, nil
	}

	submitOverlay := func() (bool, error) {
		type overlayResult struct {
			done bool
			err  error
		}

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
		lineCh := make(chan string, 64)
		resultCh := make(chan overlayResult, 1)
		overlay.setRunning(true)
		if err := redraw(); err != nil {
			return false, err
		}

		go func() {
			if capture != nil {
				capture.Start(func(line string) {
					lineCh <- line
				})
			}
			done, err := executePending(false)
			if capture != nil {
				capture.Stop()
			}
			resultCh <- overlayResult{done: done, err: err}
		}()

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case line := <-lineCh:
				overlay.addOutput(line)
				if err := redraw(); err != nil {
					overlay.setRunning(false)
					return false, err
				}
			case <-ticker.C:
				overlay.advanceSpinner()
				if err := redraw(); err != nil {
					overlay.setRunning(false)
					return false, err
				}
			case result := <-resultCh:
				for {
					select {
					case line := <-lineCh:
						overlay.addOutput(line)
					default:
						overlay.setRunning(false)
						if result.err != nil {
							return false, result.err
						}
						if !result.done {
							if err := refreshBuffer(); err != nil {
								return false, err
							}
						}
						if err := redraw(); err != nil {
							return false, err
						}
						return result.done, nil
					}
				}
			}
		}
	}

	showMenu := func(clickX, clickY int) error {
		refreshTerminalSize()
		files, err := svc.MenuFiles()
		if err != nil {
			return err
		}
		menu = buildContextMenu(buffer, files, clickX, clickY, menuLastItem)
		return redraw()
	}

	executeMenuItem := func(item menuItem) (bool, error) {
		menuLastItem = menu.hover
		menu.dismiss()
		switch item.kind {
		case menuWrite:
			msg, err := svc.Save()
			if err != nil {
				buffer.status = diagnosticText(err)
			} else {
				buffer.status = msg
			}
			return false, nil
		case menuCut:
			snarf = snarfSelection(buffer)
			if len(snarf) == 0 {
				return false, nil
			}
			if err := copyToClipboard(stdout, snarf); err != nil {
				return false, err
			}
			next, err := replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, "")
			if err != nil {
				return false, err
			}
			buffer = next
			buffer.status = "cut"
			return false, nil
		case menuSnarf:
			snarf = snarfSelection(buffer)
			if len(snarf) != 0 {
				if err := copyToClipboard(stdout, snarf); err != nil {
					return false, err
				}
				buffer.status = "snarfed"
			}
			return false, nil
		case menuPaste:
			if len(snarf) == 0 {
				return false, nil
			}
			next, err := replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, string(snarf))
			if err != nil {
				return false, err
			}
			buffer = next
			buffer.status = ""
			return false, nil
		case menuLook:
			next, ok, err := lookInBuffer(svc, buffer, true)
			if err != nil {
				return false, err
			}
			if ok {
				buffer = next
				buffer.status = ""
			} else {
				buffer.status = "?no match"
			}
			return false, nil
		case menuRegexp:
			pattern := parser.LastPatternUTF8()
			if pattern == "" {
				buffer.status = "?no previous regexp"
				return false, nil
			}
			done, _, err := executeDirect("/"+escapeSearchPattern(pattern)+"/\n", false)
			if err != nil {
				buffer.status = diagnosticText(err)
				return false, nil
			}
			if done {
				return true, nil
			}
			if err := refreshBuffer(); err != nil {
				return false, err
			}
			buffer.status = ""
			return false, nil
		case menuPlumb:
			token := plumbToken(buffer)
			if token == "" {
				return false, nil
			}
			done, _, err := executeDirect("B "+token+"\n", false)
			if err != nil {
				buffer.status = diagnosticText(err)
				return false, nil
			}
			if done {
				return true, nil
			}
			if err := refreshBuffer(); err != nil {
				return false, err
			}
			buffer.status = ""
			return false, nil
		case menuFile:
			view, err := svc.FocusFile(item.fileID)
			if err != nil {
				buffer.status = diagnosticText(err)
				return false, nil
			}
			buffer = newBufferState(view)
			buffer.status = ""
			return false, nil
		}
		return false, nil
	}

	handleMenuMouse := func(event mouseEvent) (bool, error) {
		if !menu.visible {
			return false, nil
		}
		btn := event.button & 3
		if event.button >= 32 && event.button < 64 {
			item := menu.itemAt(event.x, event.y)
			if menu.hover != item {
				menu.hover = item
				return false, redraw()
			}
			return false, nil
		}
		if btn == 2 && !event.pressed {
			if menu.hover >= 0 && menu.hover < len(menu.items) {
				done, err := executeMenuItem(menu.items[menu.hover])
				if err != nil {
					return false, err
				}
				if done {
					return true, nil
				}
			} else {
				menu.dismiss()
			}
			return false, redraw()
		}
		if event.pressed || event.button == 64 || event.button == 65 {
			menu.dismiss()
			return false, redraw()
		}
		return false, nil
	}

	handleOverlayMouse := func(event mouseEvent) bool {
		if !overlay.visible {
			return false
		}
		if event.y < overlayTopRow(overlay) {
			if event.pressed {
				overlay.close()
				return true
			}
			return false
		}
		switch event.button {
		case 64:
			overlay.scrollOlder(3)
			overlay.selecting = false
		case 65:
			overlay.scrollNewer(3)
			overlay.selecting = false
		default:
			if event.button >= 32 && event.button < 64 {
				if overlay.selecting {
					overlay.selectEnd = overlay.screenToPos(event.y, event.x)
				}
				return true
			}
			if btn := event.button & 3; btn == 0 || btn == 2 {
				if event.pressed {
					pos := overlay.screenToPos(event.y, event.x)
					if pos.line >= 0 {
						overlay.selecting = true
						overlay.selectBtn2 = btn == 2
						overlay.selectStart = pos
						overlay.selectEnd = pos
						return true
					}
				} else if overlay.selecting {
					overlay.selectEnd = overlay.screenToPos(event.y, event.x)
					overlay.selecting = false
					if overlay.selectBtn2 {
						overlay.selectBtn2 = false
						token := ""
						start, end, ok := overlay.selectionBounds()
						if ok && isOverlayClickSelection(start, end) {
							token = overlay.tokenAt(start)
						} else {
							token = trimOverlaySelection(overlay.selectedText())
						}
						if token != "" {
							done, _, err := executeDirect("B "+token+"\n", false)
							if err == nil && !done {
								_ = refreshBuffer()
							}
						}
						return true
					}
					_ = copyToClipboard(stdout, overlay.selectedText())
					return true
				}
			}
		}
		return true
	}

	handleBufferSpecial := func(key int, mouse *mouseEvent) (bool, error) {
		if key == keyEsc {
			if menu.visible {
				menu.dismiss()
				return false, redraw()
			}
			buffer.markMode = false
			buffer.dotStart = buffer.cursor
			buffer.dotEnd = buffer.cursor
			buffer.status = ""
			mouseSelecting = false
			return false, redraw()
		}
		if key == keyMouse {
			if mouse == nil {
				return false, nil
			}
			done, err := handleMenuMouse(*mouse)
			if err != nil {
				return false, err
			}
			if done {
				if err := exitBufferMode(stdout); err != nil {
					return false, err
				}
				return true, nil
			}
			if menu.visible {
				return false, nil
			}
			if mouse.button == 2 && mouse.pressed {
				return false, showMenu(mouse.x, mouse.y)
			}
			if mouse.button == 8 && mouse.pressed {
				pos, ok := screenToPos(buffer, nil, mouse.y, mouse.x)
				if ok {
					if buffer.dotStart == buffer.dotEnd {
						start, end := wordSpanAt(buffer.text, pos)
						buffer.dotStart = start
						buffer.dotEnd = end
						buffer.cursor = start
					}
					next, ok, err := lookInBuffer(svc, buffer, true)
					if err != nil {
						return false, err
					}
					if ok {
						buffer = next
						buffer.status = ""
						if err := copyToClipboard(stdout, snarfSelection(buffer)); err != nil {
							return false, err
						}
					}
					return false, redraw()
				}
				return false, nil
			}
			if mouse.button == 0 && mouse.pressed {
				pos, ok := screenToPos(buffer, nil, mouse.y, mouse.x)
				if ok {
					now := time.Now()
					doubleClick := lastMouseClickPos == pos &&
						!lastMouseClick.IsZero() &&
						now.Sub(lastMouseClick) < 400*time.Millisecond
					lastMouseClick = now
					lastMouseClickPos = pos

					if doubleClick {
						buffer.markMode = false
						start, end := wordSpanAt(buffer.text, pos)
						if start < end {
							mouseSelecting = false
							buffer.cursor = start
							buffer.dotStart = start
							buffer.dotEnd = end
							if err := copyToClipboard(stdout, snarfSelection(buffer)); err != nil {
								return false, err
							}
							if err := flashBufferSelection(); err != nil {
								return false, err
							}
							return false, redraw()
						}
					}
				}
			}
			if handleMouseEvent(buffer, overlay, *mouse, &mouseSelecting, &mouseSelectStart) {
				if mouse.button&3 == 0 && !mouse.pressed && buffer.dotEnd > buffer.dotStart {
					if err := copyToClipboard(stdout, snarfSelection(buffer)); err != nil {
						return false, err
					}
					if err := flashBufferSelection(); err != nil {
						return false, err
					}
				}
				return false, redraw()
			}
			return false, nil
		}
		switch key {
		case keyAltSnarf:
			snarf = snarfSelection(buffer)
			if len(snarf) != 0 {
				if err := copyToClipboard(stdout, snarf); err != nil {
					return false, err
				}
				buffer.status = "snarfed"
			}
			return false, redraw()
		case keyPaste:
			paste, err := readBracketedPaste(reader)
			if err != nil {
				return false, err
			}
			if len(paste) == 0 {
				return false, nil
			}
			buffer, err = replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, string(paste))
			if err != nil {
				return false, err
			}
			buffer.status = ""
			return false, redraw()
		}
		buffer, err = applyBufferKey(svc, buffer, key)
		if err != nil {
			return false, err
		}
		return false, redraw()
	}

	handleBufferRune := func(r rune) (bool, error) {
		if menu.visible {
			menu.dismiss()
		}
		switch r {
		case '\n':
			overlay.open("")
			return false, redraw()
		case ':':
			overlay.open("")
			return false, redraw()
		case 0x18:
			snarf = snarfSelection(buffer)
			if len(snarf) == 0 {
				return false, nil
			}
			if err := copyToClipboard(stdout, snarf); err != nil {
				return false, err
			}
			next, err := replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, "")
			if err != nil {
				return false, err
			}
			buffer = next
			buffer.status = "cut"
			return false, redraw()
		case 0x19:
			if len(snarf) == 0 {
				return false, nil
			}
			next, err := replaceBufferRange(svc, buffer, buffer.dotStart, buffer.dotEnd, string(snarf))
			if err != nil {
				return false, err
			}
			buffer = next
			buffer.status = ""
			return false, redraw()
		case 0x11:
			done, _, err := executeDirect("q\n", false)
			if err != nil {
				buffer.status = diagnosticText(err)
				return false, redraw()
			}
			if done {
				return true, nil
			}
			buffer.status = ""
			return false, redraw()
		case 0x17:
			if strings.TrimSpace(buffer.name) == "" {
				overlay.open("w ")
				return false, redraw()
			}
			msg, err := svc.Save()
			if err != nil {
				buffer.status = diagnosticText(err)
			} else {
				buffer.status = msg
			}
			return false, redraw()
		case 0x1f:
			overlay.open("/")
			return false, redraw()
		case 0x0c:
			next, ok, err := lookInBuffer(svc, buffer, true)
			if err != nil {
				return false, err
			}
			if ok {
				buffer = next
				buffer.status = ""
			} else {
				buffer.status = "?no match"
			}
			return false, redraw()
		case 0x12:
			next, ok, err := lookInBuffer(svc, buffer, false)
			if err != nil {
				return false, err
			}
			if ok {
				buffer = next
				buffer.status = ""
			} else {
				buffer.status = "?no match"
			}
			return false, redraw()
		}
		if r == '\r' {
			r = '\n'
		}
		next, err := applyBufferKey(svc, buffer, int(r))
		if err != nil {
			return false, err
		}
		buffer = next
		return false, redraw()
	}

	for {
		select {
		case <-winchCh:
			refreshTerminalSize()
			if inBufferMode {
				if err := redraw(); err != nil {
					return err
				}
			}
			continue
		default:
		}
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
					key, mouse, err := readBufferEscape(reader)
					if err != nil {
						return err
					}
					switch key {
					case keyEsc:
						if menu.visible {
							menu.dismiss()
							if err := redraw(); err != nil {
								return err
							}
							continue
						}
						overlay.close()
					case keyMouse:
						if mouse != nil {
							done, err := handleMenuMouse(*mouse)
							if err != nil {
								return err
							}
							if done {
								if err := exitBufferMode(stdout); err != nil {
									return err
								}
								return nil
							}
							if handleOverlayMouse(*mouse) {
								if err := redraw(); err != nil {
									return err
								}
								continue
							}
							if !menu.visible {
								handled := handleMouseEvent(buffer, overlay, *mouse, &mouseSelecting, &mouseSelectStart)
								if !handled {
									overlay.close()
								}
							}
						}
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
					case keyPgUp:
						overlay.scrollOlder(5)
					case keyPgDn:
						overlay.scrollNewer(5)
					case keyDel:
						overlay.deleteForward()
					default:
						overlay.close()
						done, err := handleBufferSpecial(key, mouse)
						if err != nil {
							return err
						}
						if done {
							return nil
						}
						continue
					}
					if err := redraw(); err != nil {
						return err
					}
					continue
				}
				if menu.visible {
					menu.dismiss()
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
					} else {
						overlay.close()
						done, err := handleBufferRune(r)
						if err != nil {
							return err
						}
						if done {
							return nil
						}
						continue
					}
				}
				if err := redraw(); err != nil {
					return err
				}
				continue
			}
			if r == 0x1b {
				key, mouse, err := readBufferEscape(reader)
				if err != nil {
					return err
				}
				done, err := handleBufferSpecial(key, mouse)
				if err != nil {
					return err
				}
				if done {
					return nil
				}
				continue
			}
			done, err := handleBufferRune(r)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
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

func enterBufferMode(stdout io.Writer, svc wire.TermService, overlay *overlayState, menu *menuState, theme *uiTheme) (*bufferState, error) {
	view, err := svc.CurrentView()
	if err != nil {
		return nil, err
	}
	state := newBufferState(view)
	if err := drawBufferMode(stdout, state, overlay, menu, theme); err != nil {
		return nil, err
	}
	return state, nil
}

func exitBufferMode(stdout io.Writer) error {
	_, err := io.WriteString(stdout, "\x1b[?25h\x1b[0 q\x1b[?1000l\x1b[?1002l\x1b[?1006l\x1b[?2004l\x1b[?1049l")
	return err
}

func diagnosticText(err error) string {
	var diag diagnosticReporter
	if errors.As(err, &diag) {
		return diag.Diagnostic()
	}
	return "?" + err.Error()
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
	cursor := clampIndex(start+len([]rune(repl)), len(next.text))
	next.cursor = cursor
	next.dotStart = cursor
	next.dotEnd = cursor
	next.markMode = false
	if state != nil {
		next.origin = adjustOriginForCursor(next.text, state.origin, cursor, termRows)
		next.status = state.status
	}
	return next, nil
}

func drawBufferMode(stdout io.Writer, state *bufferState, overlay *overlayState, menu *menuState, theme *uiTheme) error {
	if state == nil {
		return nil
	}
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[?25h\x1b[6 q\x1b[?1000h\x1b[?1002h\x1b[?1006h\x1b[?2004h\x1b[2J"); err != nil {
		return err
	}
	viewRows := bufferViewRows(overlay)
	p := state.origin
	for row := 0; row < viewRows; row++ {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
			return err
		}
		if p <= len(state.text) {
			lineEndPos := lineEnd(state.text, p)
			if err := drawBufferLine(stdout, state, p, lineEndPos, theme); err != nil {
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
	for row := viewRows + 1; row <= termRows; row++ {
		if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row); err != nil {
			return err
		}
	}
	if overlay != nil && overlay.visible {
		height := overlayHeight(overlay)
		lines := overlay.renderLines(height - 1)
		startRow := viewRows + 1
		for row := 0; row < height-1; row++ {
			line := overlayRenderLine{}
			if row < len(lines) {
				line = lines[row]
			}
			if err := drawOverlayHistoryLine(stdout, startRow+row-1, line, overlay, theme); err != nil {
				return err
			}
		}
		if err := drawOverlayPrompt(stdout, overlay, theme); err != nil {
			return err
		}
		if err := drawMenu(stdout, menu, theme); err != nil {
			return err
		}
		return positionTerminalCursor(stdout, state, overlay)
	}
	if state.status != "" {
		status := []rune(state.status)
		if len(status) > termCols {
			status = status[:termCols]
		}
		if err := drawHUDLine(stdout, termRows-1, string(status), theme.subtlePrefix(), theme); err != nil {
			return err
		}
	}
	if err := drawMenu(stdout, menu, theme); err != nil {
		return err
	}
	return positionTerminalCursor(stdout, state, overlay)
}

func drawOverlayHistoryLine(stdout io.Writer, row int, line overlayRenderLine, overlay *overlayState, theme *uiTheme) error {
	if line.history < 0 {
		return drawHUDLine(stdout, row, line.text, theme.hudPrefix(), theme)
	}
	start, end, ok := overlay.selectionBounds()
	if !ok || line.history < start.line || line.history > end.line {
		return drawHUDLine(stdout, row, line.text, theme.hudPrefix(), theme)
	}

	offset := line.offset
	selStart := 0
	selEnd := len([]rune(line.text))
	if line.history == start.line {
		selStart = start.col + offset
	}
	if line.history == end.line {
		selEnd = end.col + offset
	}
	if selStart < 0 {
		selStart = 0
	}
	if selEnd < selStart {
		selEnd = selStart
	}

	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
		return err
	}
	prefix := theme.hudPrefix()
	if theme != nil && prefix != "" {
		if _, err := io.WriteString(stdout, prefix); err != nil {
			return err
		}
	}
	runes := []rune(line.text)
	selected := false
	col := 0
	for i, r := range runes {
		if col >= termCols {
			break
		}
		wantSelected := i >= selStart && i < selEnd
		if wantSelected != selected {
			selected = wantSelected
			if selected {
				if _, err := io.WriteString(stdout, highlightPrefix(theme, false)); err != nil {
					return err
				}
			} else if theme != nil && prefix != "" {
				if _, err := io.WriteString(stdout, highlightReset(theme)+prefix); err != nil {
					return err
				}
			} else {
				if _, err := io.WriteString(stdout, highlightReset(theme)); err != nil {
					return err
				}
			}
		}
		if _, err := io.WriteString(stdout, string(r)); err != nil {
			return err
		}
		col++
	}
	if selected {
		if theme != nil && prefix != "" {
			if _, err := io.WriteString(stdout, highlightReset(theme)+prefix); err != nil {
				return err
			}
		} else {
			if _, err := io.WriteString(stdout, highlightReset(theme)); err != nil {
				return err
			}
		}
	}
	if pad := termCols - col; pad > 0 {
		if _, err := io.WriteString(stdout, strings.Repeat(" ", pad)); err != nil {
			return err
		}
	}
	if theme != nil && prefix != "" {
		if _, err := io.WriteString(stdout, styleReset()); err != nil {
			return err
		}
	}
	return nil
}

func drawBufferLine(stdout io.Writer, state *bufferState, start, end int, theme *uiTheme) error {
	col := 0
	for p := start; p < end && col < termCols; p++ {
		selected := !state.flashSelection && p >= state.dotStart && p < state.dotEnd
		if selected {
			if _, err := io.WriteString(stdout, highlightPrefix(theme, false)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(stdout, string(state.text[p])); err != nil {
			return err
		}
		if selected {
			if _, err := io.WriteString(stdout, highlightReset(theme)); err != nil {
				return err
			}
		}
		col++
	}
	return nil
}

func drawOverlayText(stdout io.Writer, text string) error {
	line := []rune(text)
	if len(line) > termCols {
		line = line[:termCols]
	}
	_, err := io.WriteString(stdout, string(line))
	return err
}

func drawOverlayPrompt(stdout io.Writer, overlay *overlayState, theme *uiTheme) error {
	if overlay == nil {
		return nil
	}
	if err := drawHUDLine(stdout, termRows-1, "", theme.hudPrefix(), theme); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H", termRows); err != nil {
		return err
	}
	if theme != nil {
		if _, err := io.WriteString(stdout, theme.titlePrefix()); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(stdout, ": "); err != nil {
		return err
	}
	if theme != nil {
		if _, err := io.WriteString(stdout, theme.hudPrefix()); err != nil {
			return err
		}
	}
	col := 2
	for _, r := range overlay.input {
		if col >= termCols {
			break
		}
		if _, err := io.WriteString(stdout, string(r)); err != nil {
			return err
		}
		col++
	}
	if theme != nil {
		if _, err := io.WriteString(stdout, styleReset()); err != nil {
			return err
		}
	}
	return nil
}

func drawHUDLine(stdout io.Writer, row int, text, prefix string, theme *uiTheme) error {
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
		return err
	}
	if theme == nil || prefix == "" {
		return drawOverlayText(stdout, text)
	}
	line := []rune(text)
	if len(line) > termCols {
		line = line[:termCols]
	}
	plain := string(line)
	if pad := termCols - len(line); pad > 0 {
		plain += strings.Repeat(" ", pad)
	}
	_, err := io.WriteString(stdout, prefix+plain+styleReset())
	return err
}

func positionTerminalCursor(stdout io.Writer, state *bufferState, overlay *overlayState) error {
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
	_, err := fmt.Fprintf(stdout, "\x1b[%d;%dH", row+1, col+1)
	return err
}

func terminalCursorPosition(state *bufferState, overlay *overlayState) (int, int) {
	if overlay != nil && overlay.visible {
		col := 2 + overlay.cursor
		if col > termCols-1 {
			col = termCols - 1
		}
		return termRows - 1, col
	}
	if state == nil {
		return 0, 0
	}
	row := 0
	p := state.origin
	viewRows := bufferViewRows(overlay)
	for row < viewRows {
		lineEndPos := lineEnd(state.text, p)
		if state.cursor >= p && state.cursor <= lineEndPos {
			col := state.cursor - p
			if col > termCols-1 {
				col = termCols - 1
			}
			return row, col
		}
		if p < len(state.text) {
			next := nextLineStart(state.text, p)
			if next != p {
				p = next
				row++
				continue
			}
		}
		break
	}
	return max(viewRows-1, 0), 0
}

func highlightPrefix(theme *uiTheme, cursor bool) string {
	if theme == nil {
		return "\x1b[7m"
	}
	if cursor {
		return theme.cursorPrefix()
	}
	return theme.selectionPrefix()
}

func highlightReset(theme *uiTheme) string {
	if theme == nil {
		return "\x1b[27m"
	}
	return styleReset()
}

func handleBufferKey(state *bufferState, key int) {
	if state == nil {
		return
	}
	rows := bufferViewRows(nil)
	switch key {
	case 0:
		if state.markMode {
			state.markMode = false
		} else {
			state.markMode = true
			state.markPos = state.cursor
		}
	case keyUp, keyPgUp:
		state.cursor = movePageUp(state.text, state.cursor, rows)
		state.origin = lineStart(state.text, state.cursor)
	case keyDown, keyPgDn:
		state.cursor = movePageDown(state.text, state.cursor, rows)
		state.origin = lineStart(state.text, state.cursor)
	case 16:
		state.cursor = moveLineUp(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case 14:
		state.cursor = moveLineDown(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyHome, 1:
		state.cursor = lineStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyEnd, 5:
		state.cursor = lineEnd(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyLeft, 2:
		if state.cursor > 0 {
			state.cursor--
		}
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyRight, 6:
		if state.cursor < len(state.text) {
			state.cursor++
		}
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case 22:
		state.cursor = movePageDown(state.text, state.cursor, rows)
		state.origin = lineStart(state.text, state.cursor)
	case keyAltLeft:
		state.cursor = prevWordStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyAltRight:
		state.cursor = nextWordStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyAltPageUp:
		state.cursor = movePageUp(state.text, state.cursor, rows)
		state.origin = lineStart(state.text, state.cursor)
	}
	updateSelection(state)
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func snarfSelection(state *bufferState) []rune {
	if state == nil || state.dotEnd <= state.dotStart {
		return nil
	}
	return append([]rune(nil), state.text[state.dotStart:state.dotEnd]...)
}

func copyToClipboard(stdout io.Writer, text []rune) error {
	if len(text) == 0 {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(string(text)))
	_, err := fmt.Fprintf(stdout, "\x1b]52;c;%s\x07", encoded)
	return err
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

func wordSpanAt(text []rune, pos int) (start, end int) {
	pos = clampIndex(pos, len(text))
	if pos >= len(text) {
		return pos, pos
	}
	if !isWordRune(text[pos]) {
		if pos == 0 || !isWordRune(text[pos-1]) {
			return pos, pos
		}
		pos--
	}
	start = pos
	for start > 0 && isWordRune(text[start-1]) {
		start--
	}
	end = pos
	for end < len(text) && isWordRune(text[end]) {
		end++
	}
	return start, end
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
