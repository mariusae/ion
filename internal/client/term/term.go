package term

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	clientdiag "ion/internal/client/commanddiag"
	clienttarget "ion/internal/client/target"
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
	keyFocusIn
	keyFocusOut
)

var (
	termRows = 24
	termCols = 80

	shimmerStart = time.Now()
)

const (
	wakeWinch   = byte('w')
	wakeRefresh = byte('r')
	wakeRecover = byte('c')
	wakeMenu    = byte('m')
)

type bufferState struct {
	fileID         int
	name           string
	dirty          bool
	text           []rune
	layout         *bufferLayout
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

type Options struct {
	AutoIndent bool
	Refresh    <-chan struct{}
	Interrupt  func() error
}

type overlayOutputQueue struct {
	mu     sync.Mutex
	lines  []string
	notify chan struct{}
}

func newOverlayOutputQueue() *overlayOutputQueue {
	return &overlayOutputQueue{
		notify: make(chan struct{}, 1),
	}
}

func (q *overlayOutputQueue) push(line string) {
	q.mu.Lock()
	q.lines = append(q.lines, line)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *overlayOutputQueue) popAll() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.lines) == 0 {
		return nil
	}
	lines := append([]string(nil), q.lines...)
	q.lines = q.lines[:0]
	return lines
}

// Run executes the initial terminal-client slice.
//
// For now this shares the command-mode loop with the download client while the
// full terminal UI from term.c is ported behind this package boundary.
func Run(files []string, stdin io.Reader, stdout, stderr io.Writer, svc wire.TermService, capture *OutputCapture, options Options) error {
	inFile, ok := stdin.(*os.File)
	if !ok || !isTTY(inFile) {
		return fmt.Errorf("terminal mode requires a tty; use ion -d for command mode")
	}
	if err := svc.Bootstrap(files); err != nil {
		return err
	}
	return runTTY(inFile, stdout, stderr, svc, capture, options)
}

// RunBootstrapped starts the terminal UI after the caller has already loaded startup files.
func RunBootstrapped(stdin io.Reader, stdout, stderr io.Writer, svc wire.TermService, capture *OutputCapture, options Options) error {
	inFile, ok := stdin.(*os.File)
	if !ok || !isTTY(inFile) {
		return fmt.Errorf("terminal mode requires a tty; use ion -d for command mode")
	}
	return runTTY(inFile, stdout, stderr, svc, capture, options)
}

func runTTY(stdin *os.File, stdout, stderr io.Writer, svc wire.TermService, capture *OutputCapture, options Options) error {
	if !options.AutoIndent {
		options.AutoIndent = false
	} else {
		options.AutoIndent = true
	}
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
	contCh := make(chan os.Signal, 1)
	signal.Notify(contCh, syscall.SIGCONT)
	defer signal.Stop(winchCh)
	defer signal.Stop(contCh)
	wakeR, wakeW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer wakeR.Close()
	defer wakeW.Close()
	signalDone := make(chan struct{})
	defer close(signalDone)
	go forwardWakeSignals(signalDone, wakeW, winchCh, contCh)
	if options.Refresh != nil {
		go forwardWakeRequests(signalDone, wakeW, options.Refresh, wakeRefresh)
	}

	parser := cmdlang.NewParserRunes(nil)
	theme, prefetched := detectTerminalTheme(stdin, stdout)
	reader := bufio.NewReader(io.MultiReader(bytes.NewReader(prefetched), stdin))
	var pending []rune
	var linebuf []rune
	inBufferMode := true
	focused := true
	var snarf []rune
	mouseSelecting := false
	mouseSelectStart := 0
	scrollOrigins := make(map[int]int)
	lastMouseClick := time.Time{}
	lastMouseClickPos := -1
	overlay := newOverlayState()
	menu := newMenuState()
	menuSticky := menuStickyState{itemIndex: -1}
	menuSawHover := false
	menuLostReleaseSuspect := false
	var menuLostReleaseTimer *time.Timer
	renderer := newGridRenderer()
	var renderQueue renderScheduler
	renderStats := newFrameRenderStats(stderr)
	defer renderStats.Report()
	if renderer != nil && renderer.trace != nil {
		defer renderer.trace.Close()
	}
	buffer, err := enterBufferMode(stdout, svc, renderer, renderStats, overlay, menu, theme, focused)
	if err != nil {
		return err
	}
	defer exitBufferMode(stdout)

	applyBufferView := func(view wire.BufferView) {
		previous := buffer
		buffer = bufferStateFromView(view, buffer, scrollOrigins)
		refreshCurrentBufferDirty(svc, buffer)
		buffer = revealOverlaySelection(previous, buffer, overlay)
		renderQueue.Request(classifyBufferRenderRequest(previous, buffer, overlay, menu, focused))
	}

	showOverlayDiagnostic := func(message string) {
		if strings.TrimSpace(message) == "" {
			return
		}
		menu.dismiss()
		if overlay.visible {
			overlay.reopen()
		} else {
			overlay.open("")
		}
		overlay.addOutput(message)
		if buffer != nil {
			buffer.status = ""
		}
	}

	openTargetToken := func(token string) error {
		if token == "" {
			return nil
		}
		view, err := clienttarget.OpenToken(svc, token)
		if err != nil {
			showOverlayDiagnostic(diagnosticText(err))
			return nil
		}
		applyBufferView(view)
		if buffer != nil {
			buffer.status = ""
		}
		return nil
	}

	copyBufferSelectionLocal := func() error {
		copied, status, err := copyBufferSelection(stdout, buffer)
		if err != nil {
			return err
		}
		snarf = copied
		if buffer != nil {
			buffer.status = status
		}
		return nil
	}

	cutBufferSelectionLocal := func() error {
		next, copied, status, err := cutBufferSelection(stdout, svc, buffer)
		if err != nil {
			return err
		}
		buffer = next
		snarf = copied
		if buffer != nil {
			buffer.status = status
		}
		return nil
	}

	pasteBufferSelectionLocal := func() error {
		next, status, err := pasteBufferSnarf(svc, buffer, snarf)
		if err != nil {
			return err
		}
		buffer = next
		if buffer != nil {
			buffer.status = status
		}
		return nil
	}

	var refreshBuffer func() error

	reportCommandDiagnostic := func(report func(string) error, err error) error {
		line := diagnosticText(err)
		if report != nil {
			return report(line)
		}
		_, werr := fmt.Fprintln(stderr, line)
		return werr
	}

	executePending := func(final bool, report func(string) error) (bool, error) {
		for {
			if script, consumed, ok := extractRawCommand(pending, final); ok {
				pending = pending[consumed:]
				ok, err := svc.Execute(script)
				if err != nil {
					if werr := reportCommandDiagnostic(report, err); werr != nil {
						return false, werr
					}
					continue
				}
				if !ok {
					return true, nil
				}
				if err := refreshBuffer(); err != nil {
					return false, err
				}
				continue
			}

			parser.ResetRunes(pending)
			cmd, err := parser.ParseWithFinal(final)
			if err != nil {
				if errors.Is(err, cmdlang.ErrNeedMoreInput) {
					return false, nil
				}
				err = clientdiag.RewriteParseError(clientdiag.PendingScript(pending), err)
				if werr := reportCommandDiagnostic(report, err); werr != nil {
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
				if werr := reportCommandDiagnostic(report, err); werr != nil {
					return false, werr
				}
				continue
			}
			if !ok {
				return true, nil
			}
			if err := refreshBuffer(); err != nil {
				return false, err
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
		return executePending(false, nil)
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
		return executePending(false, nil)
	}

	refreshBuffer = func() error {
		view, err := svc.CurrentView()
		if err != nil {
			if overlay.visible {
				overlay.addOutput(diagnosticText(err))
				return nil
			}
			return err
		}
		applyBufferView(view)
		return nil
	}

	syncBufferDot := func() error {
		next, err := syncBufferState(svc, buffer)
		if err != nil {
			return err
		}
		buffer = next
		return nil
	}

	flushRender := func(reason string) error {
		req, ok := renderQueue.Drain()
		if !ok {
			return nil
		}
		refreshTerminalSize()
		if renderer != nil && renderer.trace != nil {
			renderer.trace.Printf("render-flush reason=%s class=%s force=%t invalidation=%d file=%q dirty=%t cursor=%d dot=%d:%d origin=%d overlay=%t menu=%t hover=%d", reason, req.class, req.forceFull, req.invalidation, buffer.name, buffer.dirty, buffer.cursor, buffer.dotStart, buffer.dotEnd, buffer.origin, overlay.visible, menu.visible, menu.hover)
		}
		err := drawBufferModeRequest(stdout, renderer, renderStats, req, buffer, overlay, menu, theme, focused)
		if renderer != nil && renderer.trace != nil {
			renderer.trace.Printf("render-flush done reason=%s class=%s err=%v", reason, req.class, err)
		}
		return err
	}

	redraw := func(req renderRequest) error {
		renderQueue.Request(req)
		if reader.Buffered() == 0 {
			return flushRender("idle")
		}
		return nil
	}

	bufferRedraw := func(class redrawClass) error {
		return redraw(bufferRenderRequest(class, overlay, menu, focused))
	}

	classifiedBufferRedraw := func(previous *bufferState) error {
		return redraw(classifyBufferRenderRequest(previous, buffer, overlay, menu, focused))
	}

	menuRedraw := func(class redrawClass) error {
		return redraw(renderRequestForLayers(class, renderInvalidateMenu))
	}

	overlayHistoryRedraw := func(class redrawClass) error {
		return redraw(renderRequestForLayers(class, renderInvalidateOverlayHistory))
	}

	overlayInputRedraw := func(class redrawClass) error {
		return redraw(renderRequestForLayers(class, renderInvalidateOverlayInput))
	}

	overlaySurfaceRedraw := func(class redrawClass) error {
		return redraw(renderRequestForLayers(class, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput))
	}

	allLayersRedraw := func(class redrawClass) error {
		return redraw(renderRequestForLayers(class, renderInvalidateAllLayers))
	}

	fullRedraw := func(class redrawClass) error {
		return redraw(fullRenderRequest(class))
	}

	stopMenuLostReleaseTimer := func() {
		if menuLostReleaseTimer == nil {
			return
		}
		menuLostReleaseTimer.Stop()
		menuLostReleaseTimer = nil
	}

	clearMenuLostRelease := func() {
		stopMenuLostReleaseTimer()
		menuLostReleaseSuspect = false
	}

	scheduleMenuLostRelease := func() {
		if !menuLostReleaseSuspect {
			return
		}
		stopMenuLostReleaseTimer()
		if renderer != nil && renderer.trace != nil {
			renderer.trace.Printf("menu-lost-release schedule")
		}
		menuLostReleaseTimer = time.AfterFunc(350*time.Millisecond, func() {
			if renderer != nil && renderer.trace != nil {
				renderer.trace.Printf("menu-lost-release fire")
			}
			_, _ = wakeW.Write([]byte{wakeMenu})
		})
	}

	redrawRunningCommand := func() error {
		if overlay == nil || !overlay.visible || !overlay.running {
			return nil
		}
		return overlayHistoryRedraw(redrawOverlayHistory)
	}

	flashBufferSelection := func() error {
		if buffer == nil || buffer.dotStart == buffer.dotEnd {
			return nil
		}
		buffer.flashSelection = true
		if err := bufferRedraw(redrawBufferSelection); err != nil {
			buffer.flashSelection = false
			return err
		}
		time.Sleep(80 * time.Millisecond)
		buffer.flashSelection = false
		return bufferRedraw(redrawBufferSelection)
	}

	flashOverlaySelection := func() error {
		if overlay == nil || !overlay.hasSelection() {
			return nil
		}
		overlay.flashSelection = true
		if err := overlayHistoryRedraw(redrawOverlayHistory); err != nil {
			overlay.flashSelection = false
			return err
		}
		time.Sleep(80 * time.Millisecond)
		overlay.flashSelection = false
		return overlayHistoryRedraw(redrawOverlayHistory)
	}

	clearTransientStatus := func() {
		if buffer != nil {
			buffer.status = ""
		}
	}

	refreshThemeOnFocus := func() error {
		nextTheme, prefetched := detectTerminalTheme(stdin, stdout)
		if len(prefetched) > 0 {
			reader = bufio.NewReader(io.MultiReader(bytes.NewReader(prefetched), reader))
		}
		theme = nextTheme
		return nil
	}

	saveWithCapture := func() (string, []string, error) {
		var lines []string
		if capture != nil {
			capture.Start(func(line string) {
				lines = append(lines, line)
			})
		}
		status, err := svc.Save()
		if capture != nil {
			capture.Stop()
		}
		if err == nil {
			if view, viewErr := svc.CurrentView(); viewErr == nil {
				applyBufferView(view)
			} else {
				err = viewErr
			}
		}
		return status, lines, err
	}

	applyStatusResult := func(status string, lines []string) {
		inline, hud := normalizeStatusResult(status, lines)
		if len(hud) == 0 {
			buffer.status = inline
			return
		}
		overlay.open("")
		for _, line := range hud {
			overlay.addOutput(line)
		}
		buffer.status = ""
	}

	executeDirect := func(line string, captureOutput bool) (bool, []string, error) {
		parser.ResetRunes([]rune(line))
		cmd, err := parser.ParseWithFinal(true)
		if err != nil {
			err = clientdiag.RewriteParseError(line, err)
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
		queue := newOverlayOutputQueue()
		resultCh := make(chan overlayResult, 1)
		drainOverlayLines := func() {
			for _, line := range queue.popAll() {
				overlay.addOutput(line)
			}
		}
		overlay.setRunning(true)
		if err := overlayHistoryRedraw(redrawOverlayHistory); err != nil {
			return false, err
		}

		checkOverlayInterrupt := func() error {
			if options.Interrupt == nil || stdin == nil {
				return nil
			}
			if reader.Buffered() == 0 {
				ok, err := waitForInputByte(stdin, 0)
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			peek, err := reader.Peek(1)
			if err != nil || len(peek) == 0 || peek[0] != 0x03 {
				return err
			}
			if _, _, err := reader.ReadRune(); err != nil {
				return err
			}
			if err := options.Interrupt(); err != nil {
				return err
			}
			overlay.addOutput("^C")
			return overlayHistoryRedraw(redrawOverlayHistory)
		}

		go func() {
			if capture != nil {
				capture.Start(func(line string) {
					queue.push(line)
				})
			}
			done, err := executePending(false, func(line string) error {
				queue.push(line)
				return nil
			})
			if capture != nil {
				capture.Stop()
			}
			resultCh <- overlayResult{done: done, err: err}
		}()

		ticker := time.NewTicker(32 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-queue.notify:
				drainOverlayLines()
				if err := overlayHistoryRedraw(redrawOverlayHistory); err != nil {
					overlay.setRunning(false)
					return false, err
				}
			case <-ticker.C:
				if err := checkOverlayInterrupt(); err != nil {
					overlay.setRunning(false)
					return false, err
				}
				if err := redrawRunningCommand(); err != nil {
					overlay.setRunning(false)
					return false, err
				}
			case result := <-resultCh:
				drainOverlayLines()
				overlay.setRunning(false)
				if result.err != nil {
					return false, result.err
				}
				if !result.done {
					if err := refreshBuffer(); err != nil {
						return false, err
					}
				}
				if err := overlayHistoryRedraw(redrawOverlayHistory); err != nil {
					return false, err
				}
				return result.done, nil
			}
		}
	}

	showMenu := func(clickX, clickY int) error {
		refreshTerminalSize()
		files, err := svc.MenuFiles()
		if err != nil {
			return err
		}
		nav, err := svc.NavigationStack()
		if err != nil {
			return err
		}
		menu = buildContextMenu(buffer, files, nav, clickX, clickY, menuSticky)
		clearMenuLostRelease()
		menuSawHover = menu.hover >= 0
		return menuRedraw(redrawMenuOpen)
	}

	executeMenuItem := func(item menuItem) (bool, error) {
		menuSticky = nextMenuStickyState(menu, menu.hover, item)
		menu.dismiss()
		clearMenuLostRelease()
		menuSawHover = false
		switch item.kind {
		case menuWrite:
			msg, lines, err := saveWithCapture()
			if err != nil {
				buffer.status = diagnosticText(err)
			} else {
				applyStatusResult(msg, lines)
			}
			return false, nil
		case menuCut:
			if err := cutBufferSelectionLocal(); err != nil {
				return false, err
			}
			return false, nil
		case menuSnarf:
			if err := copyBufferSelectionLocal(); err != nil {
				return false, err
			}
			return false, nil
		case menuPaste:
			if err := pasteBufferSelectionLocal(); err != nil {
				return false, err
			}
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
		case menuHistoryPrev, menuHistoryNext:
			line := "P\n"
			if item.kind == menuHistoryNext {
				line = "N\n"
			}
			done, _, err := executeDirect(line, true)
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
			if err := openTargetToken(token); err != nil {
				return false, nil
			}
			return false, nil
		case menuFile:
			view, err := svc.FocusFile(item.fileID)
			if err != nil {
				buffer.status = diagnosticText(err)
				return false, nil
			}
			applyBufferView(view)
			buffer.status = ""
			return false, nil
		}
		return false, nil
	}

	handleMenuMouse := func(event mouseEvent) (bool, error) {
		if renderer != nil && renderer.trace != nil {
			renderer.trace.Printf("menu-mouse button=%d repeat=%d pressed=%t x=%d y=%d visible=%t hover=%d suspect=%t sawHover=%t", event.button, event.count(), event.pressed, event.x, event.y, menu.visible, menu.hover, menuLostReleaseSuspect, menuSawHover)
		}
		if !menu.visible {
			return false, nil
		}
		btn := event.baseButton()
		if !event.pressed && (btn == 2 || btn == 3) {
			clearMenuLostRelease()
			menuSawHover = false
			if menu.hover >= 0 && menu.hover < len(menu.items) {
				done, err := executeMenuItem(menu.items[menu.hover])
				if err != nil {
					return false, err
				}
				if done {
					return true, nil
				}
				// Menu actions can mutate buffer, overlay, and status state before the menu closes.
				// Repaint all composed layers so those changes are not deferred until a later input event.
				return false, redraw(renderRequestForLayers(redrawMenuClose, renderInvalidateAllLayers))
			} else {
				menu.dismiss()
			}
			return false, menuRedraw(redrawMenuClose)
		}
		if event.isMotion() {
			item := menu.itemAt(event.x, event.y)
			if item >= 0 {
				menuSawHover = true
				clearMenuLostRelease()
			}
			if item < 0 && menuSawHover && (btn == 2 || btn == 3) {
				menuLostReleaseSuspect = true
			}
			if menuLostReleaseSuspect && (btn == 2 || btn == 3) {
				scheduleMenuLostRelease()
			} else if btn != 2 && btn != 3 {
				clearMenuLostRelease()
			}
			if menu.hover != item {
				menu.hover = item
				return false, menuRedraw(redrawMenuHover)
			}
			return false, nil
		}
		if _, wheel := event.verticalWheelDirection(); event.pressed || wheel {
			menu.dismiss()
			clearMenuLostRelease()
			menuSawHover = false
			return false, menuRedraw(redrawMenuClose)
		}
		return false, nil
	}

	handleOverlayMouse := func(event mouseEvent) (bool, error) {
		return handleOverlayMouseEvent(stdout, overlay, event, openTargetToken, flashOverlaySelection)
	}

	handleBufferSpecial := func(key int, mouse *mouseEvent) (bool, error) {
		if renderer != nil && renderer.trace != nil {
			if mouse != nil {
				renderer.trace.Printf("buffer-special key=%d mouseButton=%d repeat=%d pressed=%t x=%d y=%d overlay=%t menu=%t", key, mouse.button, mouse.count(), mouse.pressed, mouse.x, mouse.y, overlay.visible, menu.visible)
			} else {
				renderer.trace.Printf("buffer-special key=%d overlay=%t menu=%t", key, overlay.visible, menu.visible)
			}
		}
		if key == keyEsc {
			if menu.visible {
				menu.dismiss()
				return false, menuRedraw(redrawMenuClose)
			}
			previous := snapshotBufferState(buffer)
			buffer.markMode = false
			buffer.dotStart = buffer.cursor
			buffer.dotEnd = buffer.cursor
			buffer.status = ""
			mouseSelecting = false
			if err := syncBufferDot(); err != nil {
				return false, err
			}
			return false, classifiedBufferRedraw(previous)
		}
		if key == keyFocusIn {
			focused = true
			if err := refreshThemeOnFocus(); err != nil {
				return false, err
			}
			return false, allLayersRedraw(redrawTheme)
		}
		if key == keyFocusOut {
			focused = false
			if menu.visible {
				menu.dismiss()
				clearMenuLostRelease()
				menuSawHover = false
				return false, menuRedraw(redrawMenuClose)
			}
			return false, allLayersRedraw(redrawTheme)
		}
		if key == keyMouse {
			if mouse == nil {
				return false, nil
			}
			menuWasVisible := menu.visible
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
			if menuWasVisible {
				return false, nil
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
						if err := syncBufferDot(); err != nil {
							return false, err
						}
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
					return false, bufferRedraw(redrawBufferSelection)
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
						start, end := doubleClickSpanAt(buffer.text, pos)
						if start < end {
							mouseSelecting = false
							buffer.cursor = start
							buffer.dotStart = start
							buffer.dotEnd = end
							if err := syncBufferDot(); err != nil {
								return false, err
							}
							if err := copyToClipboard(stdout, snarfSelection(buffer)); err != nil {
								return false, err
							}
							if err := flashBufferSelection(); err != nil {
								return false, err
							}
							return false, bufferRedraw(redrawBufferSelection)
						}
					}
				}
			}
			previous := snapshotBufferState(buffer)
			wasSelecting := mouseSelecting
			if handleMouseEvent(buffer, overlay, *mouse, &mouseSelecting, &mouseSelectStart) {
				if _, wheel := mouse.verticalWheelDirection(); !wheel {
					if err := syncBufferDot(); err != nil {
						return false, err
					}
				}
				selectionCompleted := wasSelecting && !mouseSelecting &&
					((mouse.baseButton() == 0 && !mouse.pressed) || mouse.noButtonsDown())
				if selectionCompleted && buffer.dotEnd > buffer.dotStart {
					if err := copyToClipboard(stdout, snarfSelection(buffer)); err != nil {
						return false, err
					}
					if err := flashBufferSelection(); err != nil {
						return false, err
					}
				}
				return false, classifiedBufferRedraw(previous)
			}
			return false, nil
		}
		switch key {
		case keyAltSnarf:
			if err := copyBufferSelectionLocal(); err != nil {
				return false, err
			}
			return false, bufferRedraw(redrawBufferStatus)
		case keyPaste:
			previous := snapshotBufferState(buffer)
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
			return false, classifiedBufferRedraw(previous)
		}
		previous := snapshotBufferState(buffer)
		buffer, err = applyBufferKeyWithOptions(svc, buffer, key, options)
		if err != nil {
			return false, err
		}
		return false, classifiedBufferRedraw(previous)
	}

	handleBufferRune := func(r rune) (bool, error) {
		if menu.visible {
			menu.dismiss()
		}
		switch r {
		case 0x03:
			if err := copyBufferSelectionLocal(); err != nil {
				return false, err
			}
			return false, bufferRedraw(redrawBufferStatus)
		case '\n':
			overlay.reopen()
			return false, overlaySurfaceRedraw(redrawOverlayOpen)
		case 0x18:
			previous := snapshotBufferState(buffer)
			if err := cutBufferSelectionLocal(); err != nil {
				return false, err
			}
			return false, classifiedBufferRedraw(previous)
		case 0x16, 0x19:
			previous := snapshotBufferState(buffer)
			if err := pasteBufferSelectionLocal(); err != nil {
				return false, err
			}
			return false, classifiedBufferRedraw(previous)
		case 0x11:
			dirty, err := hasDirtyFiles(svc)
			if err != nil {
				buffer.status = diagnosticText(err)
				return false, bufferRedraw(redrawBufferStatus)
			}
			if dirty {
				done, lines, err := executeDirect("q\n", true)
				overlay.open("")
				for _, line := range lines {
					overlay.addOutput(line)
				}
				if err != nil {
					overlay.addOutput(diagnosticText(err))
					return false, overlayHistoryRedraw(redrawOverlayHistory)
				}
				if done {
					return true, nil
				}
				return false, overlayHistoryRedraw(redrawOverlayHistory)
			}
			done, _, err := executeDirect("q\n", false)
			if err != nil {
				buffer.status = diagnosticText(err)
				return false, bufferRedraw(redrawBufferStatus)
			}
			if done {
				return true, nil
			}
			buffer.status = ""
			return false, bufferRedraw(redrawBufferStatus)
		case 0x17:
			if strings.TrimSpace(buffer.name) == "" {
				overlay.open("w ")
				return false, overlaySurfaceRedraw(redrawOverlayOpen)
			}
			previous := snapshotBufferState(buffer)
			msg, lines, err := saveWithCapture()
			if err != nil {
				buffer.status = diagnosticText(err)
			} else {
				applyStatusResult(msg, lines)
			}
			return false, classifiedBufferRedraw(previous)
		case 0x1f:
			overlay.open("/")
			return false, overlaySurfaceRedraw(redrawOverlayOpen)
		case 0x0c:
			previous := snapshotBufferState(buffer)
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
			return false, classifiedBufferRedraw(previous)
		case 0x12:
			previous := snapshotBufferState(buffer)
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
			return false, classifiedBufferRedraw(previous)
		}
		if r == '\r' {
			r = '\n'
		}
		previous := snapshotBufferState(buffer)
		next, err := applyBufferKeyWithOptions(svc, buffer, int(r), options)
		if err != nil {
			return false, err
		}
		buffer = next
		return false, classifiedBufferRedraw(previous)
	}

	for {
		if reader.Buffered() == 0 {
			if reader.Buffered() == 0 {
				if inBufferMode {
					if err := flushRender("before-wait"); err != nil {
						return err
					}
				}
				wake, err := waitForTTYReady(stdin, wakeR)
				if err != nil {
					return err
				}
				if wake {
					tags, err := readWakeTags(wakeR)
					if err != nil {
						return err
					}
					for _, tag := range tags {
						if renderer != nil && renderer.trace != nil {
							renderer.trace.Printf("wake tag=%q inBuffer=%t overlay=%t menu=%t", tag, inBufferMode, overlay.visible, menu.visible)
						}
						switch tag {
						case wakeWinch:
							refreshTerminalSize()
							if inBufferMode {
								if err := fullRedraw(redrawResize); err != nil {
									return err
								}
							}
						case wakeRefresh:
							if inBufferMode {
								if err := refreshBuffer(); err != nil {
									return err
								}
								if err := allLayersRedraw(redrawRefresh); err != nil {
									return err
								}
							}
						case wakeRecover:
							if inBufferMode {
								if err := fullRedraw(redrawRecover); err != nil {
									return err
								}
							}
						case wakeMenu:
							if inBufferMode && menu.visible && menuLostReleaseSuspect {
								menu.dismiss()
								clearMenuLostRelease()
								menuSawHover = false
								if err := menuRedraw(redrawMenuClose); err != nil {
									return err
								}
							}
						}
					}
					continue
				}
				if renderer != nil && renderer.trace != nil {
					renderer.trace.Printf("stdin-ready buffered=0")
				}
			}
		} else if renderer != nil && renderer.trace != nil {
			renderer.trace.Printf("stdin-ready buffered=%d", reader.Buffered())
		}
		r, _, err := reader.ReadRune()
		if err != nil {
			if renderer != nil && renderer.trace != nil {
				renderer.trace.Printf("read-rune err=%v", err)
			}
			if errors.Is(err, io.EOF) {
				pending = append(pending, linebuf...)
				_, err := executePending(true, nil)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		if renderer != nil && renderer.trace != nil {
			renderer.trace.Printf("read-rune value=%U char=%q buffered=%d inBuffer=%t overlay=%t menu=%t", r, r, reader.Buffered(), inBufferMode, overlay.visible, menu.visible)
		}
		if inBufferMode {
			if overlay.visible {
				if r == 0x1b {
					key, mouse, err := readBufferEscape(reader, stdin)
					if err != nil {
						return err
					}
					if key != keyFocusIn && key != keyFocusOut {
						clearTransientStatus()
					}
					switch key {
					case keyEsc:
						if menu.visible {
							menu.dismiss()
							if err := menuRedraw(redrawMenuClose); err != nil {
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
							handled, err := handleOverlayMouse(*mouse)
							if err != nil {
								return err
							}
							if handled {
								req := renderRequestForLayers(redrawOverlayHistory, renderInvalidateOverlayHistory)
								if !overlay.visible {
									req = renderRequestForLayers(redrawOverlayClose, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput)
								}
								if err := redraw(req); err != nil {
									return err
								}
								continue
							}
							continue
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
						if err := overlayInputRedraw(redrawOverlayInput); err != nil {
							return err
						}
						continue
					case keyLeft:
						overlay.moveLeft()
					case keyRight:
						overlay.moveRight()
					case keyAltLeft:
						overlay.moveWordLeft()
					case keyAltRight:
						overlay.moveWordRight()
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
					case keyAltPageUp:
						overlay.scrollOlder(5)
					case keyPgDn:
						overlay.scrollNewer(5)
					case keyDel:
						overlay.deleteForward()
					case keyAltBackspace:
						overlay.killWord()
					case keyFocusIn:
						focused = true
						if err := refreshThemeOnFocus(); err != nil {
							return err
						}
						if err := allLayersRedraw(redrawTheme); err != nil {
							return err
						}
						continue
					case keyFocusOut:
						focused = false
						if menu.visible {
							menu.dismiss()
							clearMenuLostRelease()
							menuSawHover = false
							if err := menuRedraw(redrawMenuClose); err != nil {
								return err
							}
							continue
						}
						if err := allLayersRedraw(redrawTheme); err != nil {
							return err
						}
						continue
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
					overlayReq := renderRequestForLayers(redrawOverlayInput, renderInvalidateOverlayInput)
					switch key {
					case keyUp, keyDown, keyPgUp, keyAltPageUp, keyPgDn:
						overlayReq = renderRequestForLayers(redrawOverlayHistory, renderInvalidateOverlayHistory)
					}
					if !overlay.visible {
						overlayReq = renderRequestForLayers(redrawOverlayClose, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput)
					}
					if err := redraw(overlayReq); err != nil {
						return err
					}
					continue
				}
				if menu.visible {
					menu.dismiss()
				}
				clearTransientStatus()
				overlayReq := renderRequestForLayers(redrawOverlayInput, renderInvalidateOverlayInput)
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
					overlayReq = renderRequestForLayers(redrawOverlayClose, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput)
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
					overlayReq = renderRequestForLayers(redrawOverlayHistory, renderInvalidateOverlayHistory)
				case 0x0e:
					overlay.recallNext()
					overlayReq = renderRequestForLayers(redrawOverlayHistory, renderInvalidateOverlayHistory)
				case 0x15:
					overlay.killToStart()
				case 0x17:
					overlay.killWord()
				case 0x0b:
					overlay.killLine()
				case 0x16:
					overlay.scrollNewer(5)
					overlayReq = renderRequestForLayers(redrawOverlayHistory, renderInvalidateOverlayHistory)
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
				if err := redraw(overlayReq); err != nil {
					return err
				}
				continue
			}
			if r == 0x1b {
				key, mouse, err := readBufferEscape(reader, stdin)
				if err != nil {
					return err
				}
				if key != keyFocusIn && key != keyFocusOut {
					clearTransientStatus()
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
			clearTransientStatus()
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
			done, err := executePending(true, nil)
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

func enterBufferMode(stdout io.Writer, svc wire.TermService, renderer *gridRenderer, stats *frameRenderStats, overlay *overlayState, menu *menuState, theme *uiTheme, focused bool) (*bufferState, error) {
	view, err := svc.CurrentView()
	if err != nil {
		return nil, err
	}
	state := newBufferState(view)
	refreshCurrentBufferDirty(svc, state)
	if err := drawBufferModeRequest(stdout, renderer, stats, fullRenderRequest(redrawInitial), state, overlay, menu, theme, focused); err != nil {
		return nil, err
	}
	return state, nil
}

func exitBufferMode(stdout io.Writer) error {
	_, err := io.WriteString(stdout, "\x1b[?25h\x1b[0 q\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1006l\x1b[?2004l\x1b[?1049l")
	return err
}

func diagnosticText(err error) string {
	var diag diagnosticReporter
	if errors.As(err, &diag) {
		return diag.Diagnostic()
	}
	return "?" + err.Error()
}

func normalizeStatusResult(status string, captured []string) (string, []string) {
	hud := make([]string, 0, len(captured)+4)
	appendHUD := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		hud = append(hud, line)
	}
	for _, line := range captured {
		appendHUD(line)
	}

	inline := ""
	statusPrefix := ""
	for _, raw := range strings.Split(status, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if idx := strings.Index(line, "?warning:"); idx >= 0 {
			appendHUD(line[idx:])
			statusPrefix = strings.TrimSpace(line[:idx])
			continue
		}
		if statusPrefix != "" {
			appendHUD(strings.TrimSpace(statusPrefix + " " + line))
			statusPrefix = ""
			continue
		}
		if inline == "" && len(hud) == 0 {
			inline = line
			continue
		}
		if inline != "" {
			appendHUD(inline)
			inline = ""
		}
		appendHUD(line)
	}
	return inline, hud
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func forwardWakeSignals(done <-chan struct{}, wake io.Writer, winchCh, contCh <-chan os.Signal) {
	for {
		select {
		case <-done:
			return
		case <-winchCh:
			_, _ = wake.Write([]byte{wakeWinch})
		case <-contCh:
			_, _ = wake.Write([]byte{wakeRecover})
		}
	}
}

func forwardWakeRequests(done <-chan struct{}, wake io.Writer, ch <-chan struct{}, tag byte) {
	for {
		select {
		case <-done:
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			_, _ = wake.Write([]byte{tag})
		}
	}
}

func waitForTTYReady(stdin, wake *os.File) (bool, error) {
	stdinFD := int(stdin.Fd())
	wakeFD := int(wake.Fd())
	maxFD := stdinFD
	if wakeFD > maxFD {
		maxFD = wakeFD
	}
	var readfds syscall.FdSet
	fdSetAdd(&readfds, stdinFD)
	fdSetAdd(&readfds, wakeFD)
	if err := selectRead(maxFD+1, &readfds, nil); err != nil {
		if errors.Is(err, syscall.EINTR) {
			return true, nil
		}
		return false, err
	}
	return fdSetHas(&readfds, wakeFD), nil
}

func readWakeTags(wake *os.File) ([]byte, error) {
	var tags []byte
	buf := make([]byte, 16)
	if err := syscall.SetNonblock(int(wake.Fd()), true); err == nil {
		defer syscall.SetNonblock(int(wake.Fd()), false)
	}
	for {
		n, err := wake.Read(buf)
		if n > 0 {
			tags = append(tags, buf[:n]...)
		}
		if err != nil {
			var pathErr *os.PathError
			if errors.As(err, &pathErr) && errors.Is(pathErr.Err, syscall.EAGAIN) {
				break
			}
			if errors.Is(err, syscall.EAGAIN) {
				break
			}
			return tags, err
		}
		if n < len(buf) {
			break
		}
	}
	return tags, nil
}

func fdSetAdd(set *syscall.FdSet, fd int) {
	bitsPerWord := int(unsafe.Sizeof(set.Bits[0]) * 8)
	index := fd / bitsPerWord
	offset := uint(fd % bitsPerWord)
	set.Bits[index] |= 1 << offset
}

func fdSetHas(set *syscall.FdSet, fd int) bool {
	bitsPerWord := int(unsafe.Sizeof(set.Bits[0]) * 8)
	index := fd / bitsPerWord
	offset := uint(fd % bitsPerWord)
	return set.Bits[index]&(1<<offset) != 0
}

func discardFailedCommand(pending []rune) []rune {
	for i, r := range pending {
		if r == '\n' {
			return pending[i+1:]
		}
	}
	return nil
}

func extractRawCommand(pending []rune, final bool) (string, int, bool) {
	if len(pending) == 0 {
		return "", 0, false
	}
	for i, r := range pending {
		if r != '\n' {
			continue
		}
		script := string(pending[:i+1])
		if !isRawCommandScript(script) {
			return "", 0, false
		}
		return normalizeRawCommandScript(script), i + 1, true
	}
	script := string(pending)
	if !isRawCommandScript(script) {
		return "", 0, false
	}
	if !final {
		return "", 0, false
	}
	script = normalizeRawCommandScript(script)
	if strings.HasSuffix(script, "\n") {
		return script, len(pending), true
	}
	return script + "\n", len(pending), true
}

func isRawCommandScript(script string) bool {
	if strings.HasPrefix(script, ":") {
		return true
	}
	trimmed := strings.TrimSpace(script)
	return trimmed == "Q" || trimmed == ":ion:Q"
}

func normalizeRawCommandScript(script string) string {
	if trimmed := strings.TrimSpace(script); trimmed == "Q" || trimmed == ":ion:Q" {
		return ":ion:Q\n"
	}
	return script
}

func newBufferState(view wire.BufferView) *bufferState {
	return newBufferStateWithPrevious(view, nil)
}

func newBufferStateWithPrevious(view wire.BufferView, previous *bufferState) *bufferState {
	reusePrevious := previous
	if previous != nil && previous.fileID != 0 && previous.fileID != view.ID {
		reusePrevious = nil
	}
	text, reused := bufferTextForView(view.Text, reusePrevious)
	cursor := clampIndex(view.DotStart, len(text))
	dotEnd := clampIndex(view.DotEnd, len(text))
	origin := visualRowStartForPos(text, cursor)
	state := &bufferState{
		fileID:   view.ID,
		name:     view.Name,
		text:     text,
		cursor:   cursor,
		origin:   origin,
		dotStart: clampIndex(view.DotStart, len(text)),
		dotEnd:   dotEnd,
	}
	if reused && reusePrevious != nil {
		state.layout = reusePrevious.layout
	}
	return state
}

func rememberBufferOrigin(origins map[int]int, state *bufferState) {
	if origins == nil || state == nil || state.fileID == 0 {
		return
	}
	origins[state.fileID] = visualRowStartForPos(state.text, state.origin)
}

func bufferStateFromView(view wire.BufferView, previous *bufferState, origins map[int]int) *bufferState {
	rememberBufferOrigin(origins, previous)
	next := newBufferStateWithPrevious(view, previous)
	if previous != nil {
		next.status = previous.status
	}
	if origin, ok := origins[next.fileID]; ok {
		next.origin = restoreBufferOrigin(next, origin)
	}
	return next
}

func applyBufferKey(svc wire.TermService, state *bufferState, key int) (*bufferState, error) {
	return applyBufferKeyWithOptions(svc, state, key, Options{AutoIndent: true})
}

func applyBufferKeyWithOptions(svc wire.TermService, state *bufferState, key int, options Options) (*bufferState, error) {
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
		return syncBufferState(svc, state)
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
		next := newBufferState(view)
		refreshCurrentBufferDirty(svc, next)
		return next, nil
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
	case '\t':
		return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, string(rune(key)))
	case '\n':
		replacement := "\n"
		if options.AutoIndent {
			replacement = autoIndentNewline(state)
		}
		return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, replacement)
	default:
		if key >= 32 && key < keyEsc {
			return replaceBufferRange(svc, state, state.dotStart, state.dotEnd, string(rune(key)))
		}
		handleBufferKey(state, key)
		return syncBufferState(svc, state)
	}
}

func autoIndentNewline(state *bufferState) string {
	if state == nil {
		return "\n"
	}
	start := lineStart(state.text, state.cursor)
	end := start
	for end < len(state.text) {
		switch state.text[end] {
		case ' ', '\t':
			end++
		default:
			return "\n" + string(state.text[start:end])
		}
	}
	return "\n" + string(state.text[start:end])
}

func syncBufferState(svc wire.TermService, state *bufferState) (*bufferState, error) {
	if svc == nil || state == nil {
		return state, nil
	}
	view, err := svc.SetDot(state.dotStart, state.dotEnd)
	if err != nil {
		return nil, err
	}
	next := newBufferStateWithPrevious(view, state)
	next.cursor = clampIndex(state.cursor, len(next.text))
	next.origin = adjustOriginForCursor(next.text, state.origin, next.cursor, termRows)
	next.dotStart = clampIndex(view.DotStart, len(next.text))
	next.dotEnd = clampIndex(view.DotEnd, len(next.text))
	next.markMode = state.markMode
	next.markPos = clampIndex(state.markPos, len(next.text))
	next.flashSelection = state.flashSelection
	next.status = state.status
	refreshCurrentBufferDirty(svc, next)
	return next, nil
}

func restoreBufferOrigin(state *bufferState, origin int) int {
	if state == nil {
		return 0
	}
	clamped := clampIndex(origin, len(state.text))
	return visualRowStartForPos(state.text, clamped)
}

func replaceBufferRange(svc wire.TermService, state *bufferState, start, end int, repl string) (*bufferState, error) {
	view, err := svc.Replace(start, end, repl)
	if err != nil {
		return nil, err
	}
	next := newBufferStateWithPrevious(view, state)
	cursor := clampIndex(start+len([]rune(repl)), len(next.text))
	next.cursor = cursor
	next.dotStart = cursor
	next.dotEnd = cursor
	next.markMode = false
	if state != nil {
		next.origin = adjustOriginForCursor(next.text, state.origin, cursor, termRows)
		next.status = state.status
	}
	refreshCurrentBufferDirty(svc, next)
	return next, nil
}

func drawBufferModeRequest(stdout io.Writer, renderer *gridRenderer, stats *frameRenderStats, req renderRequest, state *bufferState, overlay *overlayState, menu *menuState, theme *uiTheme, focused bool) error {
	if renderer == nil {
		renderer = newGridRenderer()
	}
	return renderer.Draw(stdout, req, state, overlay, menu, theme, focused, stats)
}

func handleOverlayMouseEvent(stdout io.Writer, overlay *overlayState, event mouseEvent, openTarget func(string) error, flashSelection func() error) (bool, error) {
	if !overlay.visible {
		return false, nil
	}
	if event.y < overlayTopRow(overlay) {
		if event.dismissesOverlayOutside() {
			overlay.close()
			return true, nil
		}
		return false, nil
	}
	if dir, ok := event.verticalWheelDirection(); ok {
		prevScroll := overlay.scroll
		lines := event.count()
		if maxStep := overlayHistoryRows(overlay) - 1; maxStep > 0 && lines > maxStep {
			lines = maxStep
		}
		if dir < 0 {
			overlay.scrollOlder(lines)
		} else {
			overlay.scrollNewer(lines)
		}
		overlay.selecting = false
		return overlay.scroll != prevScroll, nil
	}
	if event.isMotion() {
		if overlay.selecting {
			overlay.selectEnd = overlay.screenToPos(event.y, event.x)
			if event.noButtonsDown() {
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
					if token != "" && openTarget != nil {
						if err := openTarget(token); err == nil {
							return true, nil
						}
					}
					return true, nil
				}
				_ = copyToClipboard(stdout, overlay.selectedText())
				if flashSelection == nil {
					return true, nil
				}
				return true, flashSelection()
			}
			return true, nil
		}
		return false, nil
	}
	if !event.isWheel() {
		if btn := event.baseButton(); btn == 0 || btn == 2 {
			if event.pressed {
				pos := overlay.screenToPos(event.y, event.x)
				if pos.line >= 0 {
					overlay.selecting = true
					overlay.selectBtn2 = btn == 2
					overlay.selectStart = pos
					overlay.selectEnd = pos
					return true, nil
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
					if token != "" && openTarget != nil {
						if err := openTarget(token); err == nil {
							return true, nil
						}
					}
					return true, nil
				}
				_ = copyToClipboard(stdout, overlay.selectedText())
				if flashSelection == nil {
					return true, nil
				}
				return true, flashSelection()
			}
		}
	}
	return false, nil
}

func drawOverlayHistoryLine(stdout io.Writer, row int, line overlayRenderLine, overlay *overlayState, theme *uiTheme) error {
	if line.history < 0 {
		return drawHUDLine(stdout, row, line.text, theme.hudPrefix(), theme)
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

	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
		return err
	}
	if theme == nil {
		prefix := overlayLinePrefix(nil, line.command)
		runes := []rune(line.text)
		selected := false
		col := 0
		if prefix != "" {
			if _, err := io.WriteString(stdout, prefix); err != nil {
				return err
			}
		}
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
				} else if prefix != "" {
					if _, err := io.WriteString(stdout, highlightReset(theme)+prefix); err != nil {
						return err
					}
				} else {
					if _, err := io.WriteString(stdout, highlightReset(theme)); err != nil {
						return err
					}
				}
			}
			nextCol, err := writeHUDRune(stdout, r, col, termCols)
			if err != nil {
				return err
			}
			col = nextCol
		}
		if selected {
			if prefix != "" {
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
		if prefix != "" {
			if _, err := io.WriteString(stdout, styleReset()); err != nil {
				return err
			}
		}
		return nil
	}

	prefix := overlayLinePrefix(theme, line.command)
	if line.running {
		return drawShimmerHUDLine(stdout, row, line.text, prefix, theme)
	}
	if prefix != "" {
		if _, err := io.WriteString(stdout, prefix); err != nil {
			return err
		}
	}
	runes := []rune(line.text)
	currentPrefix := prefix
	col := 0
	for i, r := range runes {
		if col >= termCols {
			break
		}
		nextPrefix := prefix
		if i >= selStart && i < selEnd {
			nextPrefix = highlightPrefix(theme, false)
		} else if i == 0 && r == '█' {
			nextPrefix = theme.outputPrefix()
		}
		if nextPrefix != currentPrefix {
			if _, err := io.WriteString(stdout, nextPrefix); err != nil {
				return err
			}
			currentPrefix = nextPrefix
		}
		drawRune := r
		if i == 0 && r == '█' {
			drawRune = ' '
		}
		nextCol, err := writeHUDRune(stdout, drawRune, col, termCols)
		if err != nil {
			return err
		}
		col = nextCol
	}
	if pad := termCols - col; pad > 0 {
		if _, err := io.WriteString(stdout, strings.Repeat(" ", pad)); err != nil {
			return err
		}
	}
	if prefix != "" {
		if _, err := io.WriteString(stdout, styleReset()); err != nil {
			return err
		}
	}
	return nil
}

type bufferHighlightKind int

const (
	bufferHighlightNone bufferHighlightKind = iota
	bufferHighlightSelection
	bufferHighlightCollapsed
)

func drawBufferLine(stdout io.Writer, state *bufferState, start, end int, inactive bool, theme *uiTheme) error {
	current := bufferHighlightNone
	col := 0
	collapsedPos, collapsedCol, collapsedVisible := collapsedInactiveSelection(state, inactive, start)
	collapsedPainted := false
	switchHighlight := func(next bufferHighlightKind) error {
		if next == current {
			return nil
		}
		switch next {
		case bufferHighlightNone:
			if current != bufferHighlightNone {
				if _, err := io.WriteString(stdout, highlightReset(theme)); err != nil {
					return err
				}
			}
		case bufferHighlightSelection:
			if _, err := io.WriteString(stdout, highlightPrefix(theme, false)); err != nil {
				return err
			}
		case bufferHighlightCollapsed:
			if _, err := io.WriteString(stdout, highlightPrefix(theme, true)); err != nil {
				return err
			}
		}
		current = next
		return nil
	}
	for p := start; p < end && col < termCols; p++ {
		next := bufferHighlightNone
		if collapsedVisible {
			if collapsedCol >= termCols {
				if p == end-1 {
					next = bufferHighlightCollapsed
					collapsedPainted = true
				}
			} else if p == collapsedPos {
				next = bufferHighlightCollapsed
				collapsedPainted = true
			}
		} else if !state.flashSelection && p >= state.dotStart && p < state.dotEnd {
			next = bufferHighlightSelection
		}
		if err := switchHighlight(next); err != nil {
			return err
		}
		nextCol, err := writeBufferRune(stdout, state.text[p], col, termCols)
		if err != nil {
			return err
		}
		col = nextCol
	}
	if collapsedVisible && !collapsedPainted && collapsedCol == col && col < termCols {
		if err := switchHighlight(bufferHighlightCollapsed); err != nil {
			return err
		}
		nextCol, err := writeBufferRune(stdout, ' ', col, termCols)
		if err != nil {
			return err
		}
		col = nextCol
	}
	if current != bufferHighlightNone {
		if _, err := io.WriteString(stdout, highlightReset(theme)); err != nil {
			return err
		}
	}
	return nil
}

func drawOverlayText(stdout io.Writer, text string) error {
	_, err := writeHUDText(stdout, text, 0, termCols)
	return err
}

func drawOverlayPrompt(stdout io.Writer, overlay *overlayState, theme *uiTheme) error {
	if overlay == nil {
		return nil
	}
	row := termRows - 1 - overlayBottomPadRows(overlay)
	if err := drawHUDLine(stdout, row, "", theme.hudPrefix(), theme); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H", row+1); err != nil {
		return err
	}
	if theme != nil {
		if _, err := io.WriteString(stdout, theme.hudPrefix()); err != nil {
			return err
		}
	}
	col := 0
	for _, r := range overlay.input {
		if col >= termCols {
			break
		}
		nextCol, err := writeHUDRune(stdout, r, col, termCols)
		if err != nil {
			return err
		}
		col = nextCol
	}
	if theme != nil {
		if _, err := io.WriteString(stdout, styleReset()); err != nil {
			return err
		}
	}
	return nil
}

func overlayLinePrefix(theme *uiTheme, command bool) string {
	if command {
		if theme != nil {
			return theme.commandPrefix()
		}
		return "\x1b[1m"
	}
	if theme != nil {
		return theme.hudPrefix()
	}
	return ""
}

func drawHUDLine(stdout io.Writer, row int, text, prefix string, theme *uiTheme) error {
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
		return err
	}
	if theme == nil || prefix == "" {
		return drawOverlayText(stdout, text)
	}
	if _, err := io.WriteString(stdout, prefix); err != nil {
		return err
	}
	col, err := writeHUDText(stdout, text, 0, termCols)
	if err != nil {
		return err
	}
	if pad := termCols - col; pad > 0 {
		if _, err := io.WriteString(stdout, strings.Repeat(" ", pad)); err != nil {
			return err
		}
	}
	_, err = io.WriteString(stdout, styleReset())
	return err
}

func drawInlineHUDLabel(stdout io.Writer, row int, text, prefix string, theme *uiTheme) error {
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
		return err
	}
	if theme == nil || prefix == "" {
		return drawOverlayText(stdout, text)
	}
	if _, err := io.WriteString(stdout, prefix); err != nil {
		return err
	}
	if _, err := writeHUDText(stdout, text, 0, termCols); err != nil {
		return err
	}
	_, err := io.WriteString(stdout, styleReset())
	return err
}

func drawShimmerHUDLine(stdout io.Writer, row int, text, basePrefix string, theme *uiTheme) error {
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;1H\x1b[2K", row+1); err != nil {
		return err
	}
	runes := []rune(text)
	currentPrefix := basePrefix
	if currentPrefix != "" {
		if _, err := io.WriteString(stdout, currentPrefix); err != nil {
			return err
		}
	}
	col := 0
	for i, r := range runes {
		if col >= termCols {
			break
		}
		nextPrefix := shimmerPrefix(theme, i, len(runes))
		if nextPrefix != currentPrefix {
			transition := nextPrefix
			if transition == "" {
				transition = styleReset()
				if basePrefix != "" {
					transition += basePrefix
				}
			}
			if _, err := io.WriteString(stdout, transition); err != nil {
				return err
			}
			currentPrefix = nextPrefix
		}
		nextCol, err := writeHUDRune(stdout, r, col, termCols)
		if err != nil {
			return err
		}
		col = nextCol
	}
	if currentPrefix != basePrefix {
		transition := basePrefix
		if transition == "" {
			transition = styleReset()
		}
		if _, err := io.WriteString(stdout, transition); err != nil {
			return err
		}
	}
	if pad := termCols - col; pad > 0 {
		if _, err := io.WriteString(stdout, strings.Repeat(" ", pad)); err != nil {
			return err
		}
	}
	if basePrefix != "" || len(runes) > 0 {
		if _, err := io.WriteString(stdout, styleReset()); err != nil {
			return err
		}
	}
	return nil
}

const (
	hudTabWidth    = 8
	bufferTabWidth = 8
)

func writeHUDText(stdout io.Writer, text string, startCol, maxCols int) (int, error) {
	col := startCol
	for _, r := range text {
		if col >= maxCols {
			break
		}
		nextCol, err := writeHUDRune(stdout, r, col, maxCols)
		if err != nil {
			return col, err
		}
		col = nextCol
	}
	return col, nil
}

func writeHUDRune(stdout io.Writer, r rune, col, maxCols int) (int, error) {
	return writeDisplayRune(stdout, r, col, maxCols, hudTabWidth)
}

func writeBufferRune(stdout io.Writer, r rune, col, maxCols int) (int, error) {
	return writeDisplayRune(stdout, r, col, maxCols, bufferTabWidth)
}

func writeDisplayRune(stdout io.Writer, r rune, col, maxCols, tabWidth int) (int, error) {
	advance := runeDisplayAdvance(r, col, maxCols, tabWidth)
	if advance <= 0 {
		return col, nil
	}
	if r == '\t' {
		if _, err := io.WriteString(stdout, strings.Repeat(" ", advance)); err != nil {
			return col, err
		}
		return col + advance, nil
	}
	if _, err := io.WriteString(stdout, string(r)); err != nil {
		return col, err
	}
	return col + 1, nil
}

func runeDisplayAdvance(r rune, col, maxCols, tabWidth int) int {
	if maxCols <= 0 || col >= maxCols {
		return 0
	}
	if r == '\t' {
		advance := tabWidth - (col % tabWidth)
		if advance <= 0 {
			advance = tabWidth
		}
		if col+advance > maxCols {
			advance = maxCols - col
		}
		return advance
	}
	return 1
}

func shimmerPrefix(theme *uiTheme, index, length int) string {
	dimness := shimmerBandDimness(index, length, time.Since(shimmerStart))
	if theme != nil && theme.mode == colorModeTrueColor {
		bg := theme.hudBG
		fg := blendColors(contrastColor(bg), bg, dimness*0.55)
		return sgr("1", theme.bgCode(bg), theme.fgCode(fg))
	}
	attr := ""
	switch {
	case dimness < 0.2:
		attr = "1"
	case dimness < 0.6:
		attr = "22"
	default:
		attr = "2"
	}
	if theme != nil {
		return sgr(attr, theme.bgCode(theme.hudBG))
	}
	return sgr(attr)
}

func shimmerIntensity(index, length int, elapsed time.Duration) float64 {
	if length <= 0 {
		return 0.28
	}
	sweep := float64(length + 20)
	pos := math.Floor(math.Mod(elapsed.Seconds(), 2.0) / 2.0 * sweep)
	iPos := float64(index + 10)
	dist := math.Abs(iPos - pos)
	if dist > 5 {
		return 0.28
	}
	t := 0.5 * (1 + math.Cos(math.Pi*dist/5))
	if t < 0.28 {
		return 0.28
	}
	return t
}

func shimmerBandDimness(index, length int, elapsed time.Duration) float64 {
	intensity := shimmerIntensity(index, length, elapsed)
	dimness := (intensity - 0.28) / (1.0 - 0.28)
	if dimness < 0 {
		return 0
	}
	if dimness > 1 {
		return 1
	}
	return dimness
}

func contrastColor(bg rgbColor) rgbColor {
	if luminance(bg) > 128 {
		return rgbColor{}
	}
	return rgbColor{r: 255, g: 255, b: 255}
}

func blendColors(base, target rgbColor, alpha float64) rgbColor {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	blend := func(a, b uint8) uint8 {
		value := math.Floor(float64(a)*(1-alpha) + float64(b)*alpha)
		if value < 0 {
			value = 0
		}
		if value > 255 {
			value = 255
		}
		return uint8(value)
	}
	return rgbColor{
		r: blend(base.r, target.r),
		g: blend(base.g, target.g),
		b: blend(base.b, target.b),
	}
}

func positionTerminalCursor(stdout io.Writer, state *bufferState, overlay *overlayState, menu *menuState, focused bool) error {
	if overlay != nil && overlay.visible && overlay.running {
		_, err := io.WriteString(stdout, "\x1b[?25l")
		return err
	}
	if !focused || (menu != nil && menu.visible) {
		_, err := io.WriteString(stdout, "\x1b[?25l")
		return err
	}
	if !terminalCursorVisible(state, overlay) {
		_, err := io.WriteString(stdout, "\x1b[?25l")
		return err
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
	_, err := fmt.Fprintf(stdout, "\x1b[?25h\x1b[%d;%dH", row+1, col+1)
	return err
}

func terminalCursorVisible(state *bufferState, overlay *overlayState) bool {
	if overlay != nil && overlay.visible {
		return true
	}
	if state == nil {
		return true
	}
	layout := state.visibleLayout(overlay)
	if layout == nil || len(layout.rows) == 0 {
		return false
	}
	cursorRow := visualCursorRowStartForPos(state.text, state.cursor)
	for _, layoutRow := range layout.rows {
		if layoutRow.start == cursorRow {
			return true
		}
	}
	return false
}

func terminalCursorPosition(state *bufferState, overlay *overlayState) (int, int) {
	if overlay != nil && overlay.visible {
		row := termRows - 1 - overlayBottomPadRows(overlay)
		col := overlay.cursor
		if col > termCols-1 {
			col = termCols - 1
		}
		return row, col
	}
	if state == nil {
		return 0, 0
	}
	layout := state.visibleLayout(overlay)
	if layout == nil || len(layout.rows) == 0 {
		return 0, 0
	}
	cursorRow := visualCursorRowStartForPos(state.text, state.cursor)
	for row, layoutRow := range layout.rows {
		if layoutRow.start == cursorRow {
			col := layoutRow.columnForPos(state.cursor)
			if col >= bufferWrapCols() {
				col = bufferWrapCols() - 1
			}
			if col > termCols-1 {
				col = termCols - 1
			}
			return row, col
		}
	}
	return max(len(layout.rows)-1, 0), 0
}

func overlayRunningLineRow(overlay *overlayState, lines []overlayRenderLine) int {
	if overlay == nil || !overlay.visible {
		return -1
	}
	baseRow := overlayTopRow(overlay) + overlayTopPadRows(overlay)
	for idx, line := range lines {
		if line.running {
			return baseRow + idx
		}
	}
	return -1
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

func collapsedInactiveSelection(state *bufferState, inactive bool, rowStart int) (int, int, bool) {
	if state == nil || !inactive {
		return 0, 0, false
	}
	if state.dotStart != state.dotEnd {
		return 0, 0, false
	}
	pos := clampIndex(state.dotStart, len(state.text))
	cursorRow := visualCursorRowStartForPos(state.text, pos)
	if cursorRow != rowStart {
		return 0, 0, false
	}
	return pos, visualColumnForPos(state.text, cursorRow, pos), true
}

func bufferInactive(overlay *overlayState, menu *menuState, focused bool) bool {
	if !focused {
		return true
	}
	if menu != nil && menu.visible {
		return true
	}
	return overlay != nil && overlay.visible
}

func revealOverlaySelection(previous, next *bufferState, overlay *overlayState) *bufferState {
	if next == nil || overlay == nil || !overlay.visible {
		return next
	}
	if previous == nil || previous.fileID != next.fileID {
		return next
	}
	if previous.dotStart == next.dotStart && previous.dotEnd == next.dotEnd {
		return next
	}
	rows := bufferViewRows(overlay)
	next.origin = adjustOriginForCursor(next.text, next.origin, next.dotStart, rows)
	return next
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
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyDown, keyPgDn:
		state.cursor = movePageDown(state.text, state.cursor, rows)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
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
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyAltLeft:
		state.cursor = prevWordStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyAltRight:
		state.cursor = nextWordStart(state.text, state.cursor)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	case keyAltPageUp:
		state.cursor = movePageUp(state.text, state.cursor, rows)
		state.origin = adjustOriginForCursor(state.text, state.origin, state.cursor, rows)
	}
	updateSelection(state)
}

func movePageUp(text []rune, pos, rows int) int {
	for i := 0; i < rows; i++ {
		next := prevVisualRowStart(text, visualCursorRowStartForPos(text, pos))
		if next == pos {
			break
		}
		pos = next
	}
	return pos
}

func movePageDown(text []rune, pos, rows int) int {
	for i := 0; i < rows; i++ {
		next := nextVisualRowStart(text, visualCursorRowStartForPos(text, pos))
		if next == pos {
			break
		}
		pos = next
	}
	return pos
}

func adjustOriginForCursor(text []rune, origin, cursor, rows int) int {
	origin = visualRowStartForPos(text, origin)
	cursorRow := visualCursorRowStartForPos(text, cursor)
	if cursorRow < origin {
		return cursorRow
	}
	visualRows := 0
	for p := origin; p < cursorRow; {
		next := nextVisualRowStart(text, p)
		if next == p {
			break
		}
		visualRows++
		p = next
	}
	if visualRows < rows {
		return origin
	}
	centered := cursorRow
	for i := 0; i < rows/2 && centered > 0; i++ {
		prev := prevVisualRowStart(text, centered)
		if prev == centered {
			break
		}
		centered = prev
	}
	return centered
}

func moveLineUp(text []rune, pos int) int {
	start := visualCursorRowStartForPos(text, pos)
	if start == 0 {
		return pos
	}
	col := visualColumnForPos(text, start, pos)
	prev := prevVisualRowStart(text, start)
	return visualRowPosAtColumn(text, prev, col)
}

func moveLineDown(text []rune, pos int) int {
	start := visualCursorRowStartForPos(text, pos)
	col := visualColumnForPos(text, start, pos)
	next := nextVisualRowStart(text, start)
	if next == start {
		return pos
	}
	return visualRowPosAtColumn(text, next, col)
}

func bufferWrapCols() int {
	if termCols < 1 {
		return 1
	}
	return termCols
}

func visualRowEnd(text []rune, start int) int {
	start = clampIndex(start, len(text))
	col := 0
	end := start
	maxCols := bufferWrapCols()
	for end < len(text) && text[end] != '\n' && col < maxCols {
		advance := bufferRuneAdvance(text[end], col, maxCols)
		if advance <= 0 {
			break
		}
		col += advance
		end++
	}
	return end
}

func nextVisualRowStart(text []rune, start int) int {
	start = clampIndex(start, len(text))
	end := visualRowEnd(text, start)
	if end < len(text) && text[end] == '\n' {
		next := end + 1
		if next >= len(text) {
			return start
		}
		return next
	}
	if end > start && end < len(text) {
		return end
	}
	return start
}

func prevVisualRowStart(text []rune, start int) int {
	start = clampIndex(start, len(text))
	currentLineStart := lineStart(text, start)
	if start > currentLineStart {
		prev := currentLineStart
		for {
			next := nextVisualRowStart(text, prev)
			if next == prev || next >= start {
				return prev
			}
			prev = next
		}
	}
	if currentLineStart == 0 {
		return 0
	}
	prevLine := prevLineStart(text, currentLineStart)
	return lastVisualRowStart(text, prevLine)
}

func lastVisualRowStart(text []rune, start int) int {
	start = clampIndex(start, len(text))
	limit := nextLineStart(text, start)
	last := start
	for {
		next := nextVisualRowStart(text, last)
		if next == last || next >= limit {
			return last
		}
		last = next
	}
}

func visualRowStartForPos(text []rune, pos int) int {
	pos = clampIndex(pos, len(text))
	if pos == len(text) && pos > 0 && text[pos-1] == '\n' {
		pos--
	}
	start := lineStart(text, pos)
	for {
		end := visualRowEnd(text, start)
		if pos <= end {
			return start
		}
		next := nextVisualRowStart(text, start)
		if next == start {
			return start
		}
		start = next
	}
}

func visualCursorRowStartForPos(text []rune, pos int) int {
	pos = clampIndex(pos, len(text))
	start := visualRowStartForPos(text, pos)
	if pos >= len(text) {
		return start
	}
	end := visualRowEnd(text, start)
	if pos == end && end > start && end < len(text) && text[end] != '\n' {
		return end
	}
	return start
}

func visualRowPosAtColumn(text []rune, start, col int) int {
	if col < 0 {
		col = 0
	}
	end := visualRowEnd(text, start)
	if col == 0 {
		return start
	}
	visualCol := 0
	maxCols := bufferWrapCols()
	for pos := start; pos < end; pos++ {
		if col == visualCol {
			return pos
		}
		advance := bufferRuneAdvance(text[pos], visualCol, maxCols)
		if advance <= 0 {
			break
		}
		visualCol += advance
		if col <= visualCol {
			return pos + 1
		}
	}
	return end
}

func hasDirtyFiles(svc wire.TermService) (bool, error) {
	files, err := svc.MenuFiles()
	if err != nil {
		return false, err
	}
	for _, file := range files {
		if file.Dirty {
			return true, nil
		}
	}
	return false, nil
}

func refreshCurrentBufferDirty(svc wire.TermService, state *bufferState) {
	if svc == nil || state == nil {
		return
	}
	files, err := svc.MenuFiles()
	if err != nil {
		return
	}
	for _, file := range files {
		if !file.Current {
			continue
		}
		state.dirty = file.Dirty
		return
	}
	state.dirty = false
}

func bufferWindowTitleSequence(name string, dirty bool) string {
	title := filepath.Base(strings.TrimSpace(name))
	if title == "." || title == string(filepath.Separator) {
		title = ""
	}
	title = strings.Map(func(r rune) rune {
		switch r {
		case 0x07, 0x1b:
			return -1
		}
		return r
	}, title)
	if title == "" {
		title = "ion"
	}
	if dirty {
		title += "'"
	}
	return "\x1b]2;" + title + "\x07"
}

func linePosAtColumn(text []rune, start, col int) int {
	end := lineEnd(text, start)
	pos := start + col
	if pos > end {
		return end
	}
	return pos
}

func bufferRuneAdvance(r rune, col, maxCols int) int {
	return runeDisplayAdvance(r, col, maxCols, bufferTabWidth)
}

func visualColumnForPos(text []rune, start, pos int) int {
	start = clampIndex(start, len(text))
	pos = clampIndex(pos, len(text))
	if pos < start {
		pos = start
	}
	col := 0
	maxCols := bufferWrapCols()
	for i := start; i < pos && i < len(text) && text[i] != '\n' && col < maxCols; i++ {
		advance := bufferRuneAdvance(text[i], col, maxCols)
		if advance <= 0 {
			break
		}
		col += advance
	}
	return col
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

func copyBufferSelection(stdout io.Writer, state *bufferState) ([]rune, string, error) {
	snarf := snarfSelection(state)
	if len(snarf) == 0 {
		return nil, "", nil
	}
	if err := copyToClipboard(stdout, snarf); err != nil {
		return nil, "", err
	}
	return snarf, "snarfed", nil
}

func cutBufferSelection(stdout io.Writer, svc wire.TermService, state *bufferState) (*bufferState, []rune, string, error) {
	snarf, _, err := copyBufferSelection(stdout, state)
	if err != nil {
		return state, nil, "", err
	}
	if len(snarf) == 0 {
		return state, nil, "", nil
	}
	next, err := replaceBufferRange(svc, state, state.dotStart, state.dotEnd, "")
	if err != nil {
		return state, nil, "", err
	}
	return next, snarf, "cut", nil
}

func pasteBufferSnarf(svc wire.TermService, state *bufferState, snarf []rune) (*bufferState, string, error) {
	if len(snarf) == 0 {
		return state, "", nil
	}
	next, err := replaceBufferRange(svc, state, state.dotStart, state.dotEnd, string(snarf))
	if err != nil {
		return state, "", err
	}
	return next, "", nil
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

func doubleClickSpanAt(text []rune, pos int) (start, end int) {
	pos = clampIndex(pos, len(text))
	if start, end, ok := delimitedSpanAt(text, pos); ok {
		return start, end
	}
	if start, end, ok := lineSpanAtDoubleClick(text, pos); ok {
		return start, end
	}
	return wordSpanAt(text, pos)
}

func lineSpanAtDoubleClick(text []rune, pos int) (start, end int, ok bool) {
	pos = clampIndex(pos, len(text))
	start = lineStart(text, pos)
	end = lineEnd(text, pos)
	if pos == end {
		return start, end, true
	}
	return 0, 0, false
}

func delimitedSpanAt(text []rune, pos int) (start, end int, ok bool) {
	type boundary struct {
		index     int
		opening   bool
		delimiter rune
	}
	var checks []boundary
	if pos > 0 {
		if isOpeningDelimiter(text[pos-1]) {
			checks = append(checks, boundary{index: pos - 1, opening: true, delimiter: text[pos-1]})
		}
		if isClosingDelimiter(text[pos-1]) {
			checks = append(checks, boundary{index: pos - 1, opening: false, delimiter: text[pos-1]})
		}
	}
	if pos < len(text) {
		if isOpeningDelimiter(text[pos]) {
			checks = append(checks, boundary{index: pos, opening: true, delimiter: text[pos]})
		}
		if isClosingDelimiter(text[pos]) {
			checks = append(checks, boundary{index: pos, opening: false, delimiter: text[pos]})
		}
	}
	for _, check := range checks {
		var openIdx, closeIdx int
		if check.opening {
			closeIdx, ok = findDelimitedClose(text, check.index, check.delimiter)
			openIdx = check.index
		} else {
			openIdx, ok = findDelimitedOpen(text, check.index, check.delimiter)
			closeIdx = check.index
		}
		if !ok || openIdx+1 > closeIdx {
			continue
		}
		return openIdx + 1, closeIdx, true
	}
	return 0, 0, false
}

func isOpeningDelimiter(r rune) bool {
	switch r {
	case '(', '{', '[', '<', '"', '\'':
		return true
	}
	return false
}

func isClosingDelimiter(r rune) bool {
	switch r {
	case ')', '}', ']', '>', '"', '\'':
		return true
	}
	return false
}

func matchingDelimiter(r rune) rune {
	switch r {
	case '(':
		return ')'
	case '{':
		return '}'
	case '[':
		return ']'
	case '<':
		return '>'
	case ')':
		return '('
	case '}':
		return '{'
	case ']':
		return '['
	case '>':
		return '<'
	case '"', '\'':
		return r
	}
	return 0
}

func findDelimitedClose(text []rune, openIdx int, open rune) (int, bool) {
	close := matchingDelimiter(open)
	if close == 0 {
		return 0, false
	}
	if open == close {
		for i := openIdx + 1; i < len(text); i++ {
			if text[i] == close {
				return i, true
			}
		}
		return 0, false
	}
	depth := 1
	for i := openIdx + 1; i < len(text); i++ {
		switch text[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func findDelimitedOpen(text []rune, closeIdx int, close rune) (int, bool) {
	open := matchingDelimiter(close)
	if open == 0 {
		return 0, false
	}
	if open == close {
		for i := closeIdx - 1; i >= 0; i-- {
			if text[i] == open {
				return i, true
			}
		}
		return 0, false
	}
	depth := 1
	for i := closeIdx - 1; i >= 0; i-- {
		switch text[i] {
		case close:
			depth++
		case open:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
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
