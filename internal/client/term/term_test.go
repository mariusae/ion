package term

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"ion/internal/proto/wire"
)

type fakeTermService struct {
	view         wire.BufferView
	menuFiles    []wire.MenuFile
	navStack     wire.NavigationStack
	focusID      int
	setDotCalls  int
	lastDotStart int
	lastDotEnd   int
}

func (f *fakeTermService) Bootstrap(files []string) error {
	_ = files
	return nil
}

func (f *fakeTermService) Execute(script string) (bool, error) {
	_ = script
	return true, nil
}

func (f *fakeTermService) CurrentView() (wire.BufferView, error) {
	return f.view, nil
}

func (f *fakeTermService) OpenFiles(files []string) (wire.BufferView, error) {
	if len(files) > 0 {
		f.view.Name = files[len(files)-1]
	}
	return f.view, nil
}

func (f *fakeTermService) OpenTarget(path, address string) (wire.BufferView, error) {
	f.view.Name = path
	_ = address
	return f.view, nil
}

func (f *fakeTermService) MenuFiles() ([]wire.MenuFile, error) {
	return append([]wire.MenuFile(nil), f.menuFiles...), nil
}

func (f *fakeTermService) NavigationStack() (wire.NavigationStack, error) {
	return f.navStack, nil
}

func (f *fakeTermService) FocusFile(id int) (wire.BufferView, error) {
	f.focusID = id
	for _, file := range f.menuFiles {
		if file.ID != id {
			continue
		}
		f.view.ID = file.ID
		f.view.Name = file.Name
		break
	}
	return f.view, nil
}

func (f *fakeTermService) SetAddress(expr string) (wire.BufferView, error) {
	_ = expr
	return f.view, nil
}

func (f *fakeTermService) SetDot(start, end int) (wire.BufferView, error) {
	f.setDotCalls++
	f.lastDotStart = start
	f.lastDotEnd = end
	f.view.DotStart = start
	f.view.DotEnd = end
	return f.view, nil
}

func (f *fakeTermService) Replace(start, end int, text string) (wire.BufferView, error) {
	runes := []rune(f.view.Text)
	next := append([]rune{}, runes[:start]...)
	next = append(next, []rune(text)...)
	next = append(next, runes[end:]...)
	f.view.Text = string(next)
	f.view.DotStart = start
	f.view.DotEnd = start + len([]rune(text))
	return f.view, nil
}

func (f *fakeTermService) Undo() (wire.BufferView, error) {
	return f.view, nil
}

func (f *fakeTermService) Save() (string, error) {
	return "saved", nil
}

func TestNewBufferStateStartsAtDot(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\nbeta\ngamma\n",
		DotStart: 6,
		DotEnd:   10,
	})

	if got, want := state.cursor, 6; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
	if got, want := state.origin, 6; got != want {
		t.Fatalf("origin = %d, want %d", got, want)
	}
}

func TestRestoreBufferOriginKeepsPerFileScrollPosition(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		ID:       7,
		Text:     "line1\nline2\nline3\nline4\n",
		DotStart: 18,
		DotEnd:   18,
	})
	state.cursor = 18

	if got, want := restoreBufferOrigin(state, 6), 6; got != want {
		t.Fatalf("restoreBufferOrigin() = %d, want %d", got, want)
	}
}

func TestBufferStateFromViewRestoresSavedOriginForRevisitedFile(t *testing.T) {
	t.Parallel()

	origins := make(map[int]int)
	current := newBufferState(wire.BufferView{
		ID:       1,
		Text:     "one1\none2\none3\none4\n",
		DotStart: 0,
		DotEnd:   0,
	})
	current.origin = 10

	next := bufferStateFromView(wire.BufferView{
		ID:       2,
		Text:     "two1\ntwo2\n",
		DotStart: 0,
		DotEnd:   0,
	}, current, origins)

	if got, want := origins[1], 10; got != want {
		t.Fatalf("saved origin = %d, want %d", got, want)
	}

	next.origin = 5
	restored := bufferStateFromView(wire.BufferView{
		ID:       1,
		Text:     "one1\none2\none3\none4\n",
		DotStart: 10,
		DotEnd:   10,
	}, next, origins)

	if got, want := restored.origin, 10; got != want {
		t.Fatalf("restored origin = %d, want %d", got, want)
	}
}

func TestMovePageDownByLines(t *testing.T) {
	t.Parallel()

	text := []rune("l1\nl2\nl3\nl4\nl5\n")
	if got, want := movePageDown(text, 0, 2), 6; got != want {
		t.Fatalf("movePageDown() = %d, want %d", got, want)
	}
}

func TestMovePageDownContinuesFromWrappedBoundary(t *testing.T) {
	prevCols := termCols
	termCols = 3
	t.Cleanup(func() {
		termCols = prevCols
	})

	text := []rune("abcdef\nghij\n")
	if got, want := movePageDown(text, 3, 1), 7; got != want {
		t.Fatalf("movePageDown(wrap boundary) = %d, want %d", got, want)
	}
}

func TestBufferViewRowsUsesLiveTerminalHeight(t *testing.T) {
	prev := termRows
	termRows = 40
	t.Cleanup(func() {
		termRows = prev
	})

	if got, want := bufferViewRows(nil), 40; got != want {
		t.Fatalf("bufferViewRows(nil) = %d, want %d", got, want)
	}

	overlay := newOverlayState()
	overlay.visible = true
	if got, want := bufferViewRows(overlay), 40-overlayHeight(overlay); got != want {
		t.Fatalf("bufferViewRows(overlay) = %d, want %d", got, want)
	}
}

func TestMoveLineDownPreservesColumn(t *testing.T) {
	t.Parallel()

	text := []rune("alpha\nxy\nomega\n")
	if got, want := moveLineDown(text, 3), 8; got != want {
		t.Fatalf("moveLineDown() = %d, want %d", got, want)
	}
}

func TestWordSpanAtSelectsWholeWord(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta")
	if start, end := wordSpanAt(text, 2); start != 0 || end != 5 {
		t.Fatalf("wordSpanAt(interior) = (%d, %d), want (0, 5)", start, end)
	}
	if start, end := wordSpanAt(text, 5); start != 0 || end != 5 {
		t.Fatalf("wordSpanAt(boundary) = (%d, %d), want (0, 5)", start, end)
	}
	if start, end := wordSpanAt(text, 6); start != 6 || end != 10 {
		t.Fatalf("wordSpanAt(next word) = (%d, %d), want (6, 10)", start, end)
	}
}

func TestDoubleClickSpanAtSelectsWholeLineAtLineEnd(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta\nomega\n")
	start, end := doubleClickSpanAt(text, 10)
	if start != 0 || end != 10 {
		t.Fatalf("doubleClickSpanAt(line end) = (%d, %d), want (0, 10)", start, end)
	}
}

func TestDoubleClickSpanAtPrioritizesDelimitedSelectionAtLineEnd(t *testing.T) {
	t.Parallel()

	text := []rune("type goListPackage struct {\n\tImportPath string\n\tImports    []string\n}")
	start, end := doubleClickSpanAt(text, len(text)-1)
	if got, want := string(text[start:end]), "\n\tImportPath string\n\tImports    []string\n"; got != want {
		t.Fatalf("doubleClickSpanAt(delimited line end) = %q, want %q", got, want)
	}
}

func TestDoubleClickSpanAtSelectsInsideParensFromRightOfOpen(t *testing.T) {
	t.Parallel()

	text := []rune("hello( there, okay)")
	start, end := doubleClickSpanAt(text, 6)
	if got, want := string(text[start:end]), " there, okay"; got != want {
		t.Fatalf("doubleClickSpanAt(right of open) = %q, want %q", got, want)
	}
}

func TestDoubleClickSpanAtSelectsInsideParensFromLeftOfClose(t *testing.T) {
	t.Parallel()

	text := []rune("hello( there, okay)")
	start, end := doubleClickSpanAt(text, len(text)-1)
	if got, want := string(text[start:end]), " there, okay"; got != want {
		t.Fatalf("doubleClickSpanAt(left of close) = %q, want %q", got, want)
	}
}

func TestDoubleClickSpanAtSelectsInsideQuotes(t *testing.T) {
	t.Parallel()

	text := []rune(`say "quoted text" now`)
	start, end := doubleClickSpanAt(text, 5)
	if got, want := string(text[start:end]), "quoted text"; got != want {
		t.Fatalf("doubleClickSpanAt(quotes) = %q, want %q", got, want)
	}
}

func TestDrawBufferLineSuppressesSelectionDuringFlash(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 0,
		DotEnd:   2,
	})
	state.flashSelection = true

	var out bytes.Buffer
	if err := drawBufferLine(&out, state, 0, 5, false, nil); err != nil {
		t.Fatalf("drawBufferLine() error = %v", err)
	}
	if strings.Contains(out.String(), "\x1b[7m") {
		t.Fatalf("drawBufferLine() = %q, want selection highlight suppressed", out.String())
	}
}

func TestDrawBufferLineHighlightsExpandedTabSpaces(t *testing.T) {
	t.Parallel()

	prevCols := termCols
	termCols = 16
	t.Cleanup(func() {
		termCols = prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "\talpha\n",
		DotStart: 0,
		DotEnd:   1,
	})

	var out bytes.Buffer
	if err := drawBufferLine(&out, state, 0, 1, false, nil); err != nil {
		t.Fatalf("drawBufferLine() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b[7m        \x1b[27m") {
		t.Fatalf("drawBufferLine() = %q, want selected tab drawn as highlighted spaces", got)
	}
}

func TestDrawBufferModeUsesTerminalBarCursor(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   1,
	})

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, nil, nil, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[?25l") {
		t.Fatalf("drawBufferMode() = %q, want redraw to hide the terminal cursor before painting", got)
	}
	if !strings.Contains(got, "\x1b[6 q") {
		t.Fatalf("drawBufferMode() = %q, want steady bar cursor shape", got)
	}
	if !strings.Contains(got, "\x1b[?1004h") {
		t.Fatalf("drawBufferMode() = %q, want focus reporting enabled", got)
	}
	if !strings.Contains(got, "\x1b[?1002h") {
		t.Fatalf("drawBufferMode() = %q, want drag mouse tracking enabled by default", got)
	}
	if !strings.Contains(got, "\x1b[?1003h") {
		t.Fatalf("drawBufferMode() = %q, want full-motion mouse tracking enabled", got)
	}
	if !strings.Contains(got, "\x1b[1;2H") {
		t.Fatalf("drawBufferMode() = %q, want terminal cursor positioned at row 1 col 2", got)
	}
	if strings.Contains(got, "\x1b[7m") {
		t.Fatalf("drawBufferMode() = %q, want no painted inverse-video cursor", got)
	}
}

func TestDrawBufferModeSetsWindowTitleToBasename(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Name:     "/tmp/work/alpha.txt",
		Text:     "alpha\n",
		DotStart: 0,
		DotEnd:   0,
	})

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, nil, nil, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b]2;alpha.txt\x07") {
		t.Fatalf("drawBufferMode() = %q, want basename-only window title", got)
	}
}

func TestDrawBufferModeMarksDirtyWindowTitle(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Name:     "/tmp/work/alpha.txt",
		Text:     "alpha\n",
		DotStart: 0,
		DotEnd:   0,
	})
	state.dirty = true

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, nil, nil, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b]2;alpha.txt'\x07") {
		t.Fatalf("drawBufferMode() = %q, want dirty window title marker", got)
	}
}

func TestExitBufferModeRestoresDefaultCursorShape(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := exitBufferMode(&out); err != nil {
		t.Fatalf("exitBufferMode() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b[?25h\x1b[0 q") || !strings.Contains(got, "\x1b[?1004l") {
		t.Fatalf("exitBufferMode() = %q, want visible default-cursor reset and focus-report disable", got)
	}
}

func TestPositionTerminalCursorHidesCursorWhileOverlayRuns(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.visible = true
	overlay.running = true

	var out bytes.Buffer
	if err := positionTerminalCursor(&out, nil, overlay, newMenuState(), true); err != nil {
		t.Fatalf("positionTerminalCursor() error = %v", err)
	}
	if got, want := out.String(), "\x1b[?25l"; got != want {
		t.Fatalf("positionTerminalCursor() = %q, want %q", got, want)
	}
}

func TestPositionTerminalCursorShowsCursorWhenNotRunning(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   1,
	})

	var out bytes.Buffer
	if err := positionTerminalCursor(&out, state, nil, newMenuState(), true); err != nil {
		t.Fatalf("positionTerminalCursor() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b[?25h\x1b[1;2H") {
		t.Fatalf("positionTerminalCursor() = %q, want visible cursor positioning sequence", got)
	}
}

func TestPositionTerminalCursorHidesCursorWhenMenuVisible(t *testing.T) {
	t.Parallel()

	menu := newMenuState()
	menu.visible = true

	var out bytes.Buffer
	if err := positionTerminalCursor(&out, newBufferState(wire.BufferView{}), nil, menu, true); err != nil {
		t.Fatalf("positionTerminalCursor() error = %v", err)
	}
	if got, want := out.String(), "\x1b[?25l"; got != want {
		t.Fatalf("positionTerminalCursor() = %q, want %q", got, want)
	}
}

func TestPositionTerminalCursorHidesCursorWhenUnfocused(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := positionTerminalCursor(&out, newBufferState(wire.BufferView{}), nil, newMenuState(), false); err != nil {
		t.Fatalf("positionTerminalCursor() error = %v", err)
	}
	if got, want := out.String(), "\x1b[?25l"; got != want {
		t.Fatalf("positionTerminalCursor() = %q, want %q", got, want)
	}
}

func TestTerminalCursorPositionTracksWrappedRows(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 3
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "abcdef\n",
		DotStart: 4,
		DotEnd:   4,
	})
	state.origin = 0

	row, col := terminalCursorPosition(state, nil)
	if row != 1 || col != 1 {
		t.Fatalf("terminalCursorPosition() = (%d, %d), want (1, 1)", row, col)
	}
}

func TestTerminalCursorPositionTreatsWrapBoundaryAsNextRow(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 3
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "abcdef\n",
		DotStart: 3,
		DotEnd:   3,
	})
	state.origin = 0

	row, col := terminalCursorPosition(state, nil)
	if row != 1 || col != 0 {
		t.Fatalf("terminalCursorPosition(wrap boundary) = (%d, %d), want (1, 0)", row, col)
	}
}

func TestTerminalCursorPositionAccountsForTabWidth(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 16
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "\talpha\n",
		DotStart: 1,
		DotEnd:   1,
	})
	state.origin = 0

	row, col := terminalCursorPosition(state, nil)
	if row != 0 || col != 8 {
		t.Fatalf("terminalCursorPosition() = (%d, %d), want (0, 8)", row, col)
	}
}

func TestVisualRowStartForPosUsesLastDrawableRowAtTrailingEOF(t *testing.T) {
	t.Parallel()

	text := []rune("line1\nline2\n")
	if got, want := visualRowStartForPos(text, len(text)), 6; got != want {
		t.Fatalf("visualRowStartForPos(EOF) = %d, want %d", got, want)
	}
}

func TestNextVisualRowStartStopsAtTrailingEOFBoundary(t *testing.T) {
	t.Parallel()

	text := []rune("line1\nline2\n")
	if got, want := nextVisualRowStart(text, 6), 6; got != want {
		t.Fatalf("nextVisualRowStart(last trailing-newline row) = %d, want %d", got, want)
	}
}

func TestNextVisualRowStartStopsAtWrappedEOFBoundary(t *testing.T) {
	prevCols := termCols
	termCols = 80
	t.Cleanup(func() {
		termCols = prevCols
	})

	text := []rune(strings.Repeat("a", 160))
	if got, want := nextVisualRowStart(text, 80), 80; got != want {
		t.Fatalf("nextVisualRowStart(last wrapped row) = %d, want %d", got, want)
	}
}

func TestTerminalCursorPositionUsesLastDrawableRowAtTrailingEOF(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "line1\nline2\n",
		DotStart: 12,
		DotEnd:   12,
	})
	state.cursor = len(state.text)
	state.origin = 0

	row, col := terminalCursorPosition(state, nil)
	if row != 1 || col != 5 {
		t.Fatalf("terminalCursorPosition(EOF) = (%d, %d), want (1, 5)", row, col)
	}
}

func TestHandleBufferKeyCanPageBackUpFromEOFWithoutBlankOrigin(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 24, 80
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	var text strings.Builder
	for i := 1; i <= 60; i++ {
		text.WriteString("line\n")
	}
	state := newBufferState(wire.BufferView{
		Text:     text.String(),
		DotStart: 0,
		DotEnd:   0,
	})

	handleBufferKey(state, keyDown)
	handleBufferKey(state, keyDown)
	handleBufferKey(state, keyDown)

	if got := state.origin; got >= len(state.text) {
		t.Fatalf("origin after paging down = %d, want visible content row", got)
	}

	handleBufferKey(state, keyUp)
	if got := state.origin; got >= len(state.text) {
		t.Fatalf("origin after paging back up = %d, want visible content row", got)
	}
}

func TestAdjustOriginForCursorCentersPageNavigationLikeTermC(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 24, 80
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	var text strings.Builder
	for i := 1; i <= 60; i++ {
		text.WriteString(fmt.Sprintf("line%03d\n", i))
	}
	state := newBufferState(wire.BufferView{
		Text:     text.String(),
		DotStart: 0,
		DotEnd:   0,
	})

	if got, want := movePageDown(state.text, state.cursor, termRows), 192; got != want {
		t.Fatalf("movePageDown() = %d, want %d", got, want)
	}
	handleBufferKey(state, keyDown)
	if got, want := state.origin, 96; got != want {
		t.Fatalf("after one page down cursor=%d origin=%d, want origin %d", state.cursor, got, want)
	}
}

func TestOverlayRunningLineRowAccountsForTopPadding(t *testing.T) {
	prevRows := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addCommand("!sleep 4")
	overlay.setRunning(true)

	lines := overlay.renderLines(overlayHistoryRows(overlay))
	if got, want := overlayRunningLineRow(overlay, lines), overlayTopRow(overlay)+overlayTopPadRows(overlay); got != want {
		t.Fatalf("overlayRunningLineRow() = %d, want %d", got, want)
	}
}

func TestBuildThemeUsesOverlayAndOutputTintsInLightMode(t *testing.T) {
	t.Parallel()

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	if got, want := theme.hudBG, (rgbColor{r: 244, g: 244, b: 244}); got != want {
		t.Fatalf("hudBG = %#v, want %#v", got, want)
	}
	if got, want := theme.outputBG, (rgbColor{r: 224, g: 224, b: 224}); got != want {
		t.Fatalf("outputBG = %#v, want %#v", got, want)
	}
}

func TestDrawInlineHUDLabelDoesNotPadTintAcrossLine(t *testing.T) {
	prevCols := termCols
	termCols = 20
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	var out bytes.Buffer
	if err := drawInlineHUDLabel(&out, 0, "saved", theme.subtlePrefix(), theme); err != nil {
		t.Fatalf("drawInlineHUDLabel() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.subtlePrefix()+"saved"+styleReset()) {
		t.Fatalf("drawInlineHUDLabel() = %q, want tinted status text", got)
	}
	if strings.Contains(got, theme.subtlePrefix()+"saved "+styleReset()) {
		t.Fatalf("drawInlineHUDLabel() = %q, want no full-line padded background", got)
	}
}

func TestDrawHUDLineExpandsTabsUnderTint(t *testing.T) {
	prevCols := termCols
	termCols = 12
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	var out bytes.Buffer
	if err := drawHUDLine(&out, 0, "a\tb", theme.hudPrefix(), theme); err != nil {
		t.Fatalf("drawHUDLine() error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, "\t") {
		t.Fatalf("drawHUDLine() = %q, want tabs expanded before rendering", got)
	}
	if !strings.Contains(got, theme.hudPrefix()+"a       b") {
		t.Fatalf("drawHUDLine() = %q, want tab expanded to tinted spaces", got)
	}
}

func TestNormalizeStatusResultKeepsSimpleSaveInline(t *testing.T) {
	t.Parallel()

	inline, hud := normalizeStatusResult("in.txt: #11", nil)
	if got, want := inline, "in.txt: #11"; got != want {
		t.Fatalf("inline status = %q, want %q", got, want)
	}
	if len(hud) != 0 {
		t.Fatalf("hud lines = %q, want none", hud)
	}
}

func TestNormalizeStatusResultRoutesWarningsToHUD(t *testing.T) {
	t.Parallel()

	inline, hud := normalizeStatusResult("todo.txt: ?warning: last char not newline\n#734", nil)
	if inline != "" {
		t.Fatalf("inline status = %q, want empty", inline)
	}
	if got, want := hud, []string{"?warning: last char not newline", "todo.txt: #734"}; !equalStrings(got, want) {
		t.Fatalf("hud lines = %q, want %q", got, want)
	}
}

func TestDrawOverlayHistoryLineTintsOutputGutter(t *testing.T) {
	prevCols := termCols
	termCols = 20
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	line := overlayRenderLine{text: "█ alpha", history: 0}

	var out bytes.Buffer
	if err := drawOverlayHistoryLine(&out, 0, line, overlay, theme); err != nil {
		t.Fatalf("drawOverlayHistoryLine() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.outputPrefix()+" ") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want tinted output gutter cell", got)
	}
	if strings.Contains(got, "█ alpha") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want background-backed gutter instead of literal block glyph", got)
	}
	if !strings.Contains(got, theme.hudPrefix()+" alpha") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want overlay tint restored for output text", got)
	}
}

func TestDrawOverlayHistoryLineExpandsTabsUnderOutputTint(t *testing.T) {
	prevCols := termCols
	termCols = 20
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	line := overlayRenderLine{text: "█ a\tb", history: 0}

	var out bytes.Buffer
	if err := drawOverlayHistoryLine(&out, 0, line, overlay, theme); err != nil {
		t.Fatalf("drawOverlayHistoryLine() error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, "\t") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want tabs expanded before rendering", got)
	}
	if !strings.Contains(got, theme.outputPrefix()+" ") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want tinted gutter cell", got)
	}
	if !strings.Contains(got, theme.hudPrefix()+" a     b") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want tab expanded under HUD tint", got)
	}
}

func TestDrawOverlayHistoryLineSelectionStartsAfterOutputGutter(t *testing.T) {
	prevCols := termCols
	termCols = 20
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.selectStart = overlaySelectionPos{line: 0, col: 0}
	overlay.selectEnd = overlaySelectionPos{line: 0, col: 2}
	line := overlayRenderLine{text: "█ alpha", history: 0, offset: 2}

	var out bytes.Buffer
	if err := drawOverlayHistoryLine(&out, 0, line, overlay, theme); err != nil {
		t.Fatalf("drawOverlayHistoryLine() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.outputPrefix()+" ") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want tinted gutter preserved", got)
	}
	if !strings.Contains(got, highlightPrefix(theme, false)+"al") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want selection highlight on content only", got)
	}
	if strings.Contains(got, highlightPrefix(theme, false)+" ") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want no selection highlight on gutter", got)
	}
}

func TestDrawOverlayHistoryLineBoldsCommittedCommand(t *testing.T) {
	prevCols := termCols
	termCols = 20
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	line := overlayRenderLine{text: ",p", history: 0, command: true}

	var out bytes.Buffer
	if err := drawOverlayHistoryLine(&out, 0, line, overlay, theme); err != nil {
		t.Fatalf("drawOverlayHistoryLine() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, theme.commandPrefix()+",p") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want bold committed command with overlay tint", got)
	}
}

func TestDrawOverlayPromptUsesOverlayTintWithoutPromptGlyph(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.visible = true
	overlay.input = []rune(",p")

	var out bytes.Buffer
	if err := drawOverlayPrompt(&out, overlay, theme); err != nil {
		t.Fatalf("drawOverlayPrompt() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.hudPrefix()+",p") {
		t.Fatalf("drawOverlayPrompt() = %q, want command input with overlay tint", got)
	}
	if strings.Contains(got, "› ") {
		t.Fatalf("drawOverlayPrompt() = %q, want no prompt glyph", got)
	}
	if strings.Contains(got, "\x1b[1;") {
		t.Fatalf("drawOverlayPrompt() = %q, want non-bold live prompt", got)
	}
}

func TestDrawBufferModeAddsTintedOverlayPaddingRows(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.visible = true
	overlay.input = []rune(",p")
	overlay.cursor = len(overlay.input)
	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   1,
	})

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, nil, nil, fullRenderRequest(redrawInitial), state, overlay, newMenuState(), theme, true); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[4;1H") || !strings.Contains(got, theme.hudPrefix()) {
		t.Fatalf("drawBufferMode() = %q, want tinted top HUD padding row", got)
	}
	if !strings.Contains(got, "\x1b[6;1H") || !strings.Contains(got, theme.hudPrefix()) {
		t.Fatalf("drawBufferMode() = %q, want tinted bottom HUD padding row", got)
	}
}

func TestDrawBufferModeShowsPaintedCursorWhenMenuVisible(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 12, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	state := newBufferState(wire.BufferView{
		Name:     "alpha.txt",
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   1,
	})
	menu := buildContextMenu(state, nil, wire.NavigationStack{}, 5, 8, menuStickyState{})

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, nil, nil, fullRenderRequest(redrawInitial), state, nil, menu, theme, true); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.cursorPrefix()+"l") {
		t.Fatalf("drawBufferMode() = %q, want painted collapsed cursor in buffer", got)
	}
	if !strings.Contains(got, "\x1b[?25l") {
		t.Fatalf("drawBufferMode() = %q, want hidden terminal cursor while menu is visible", got)
	}
	if !strings.Contains(got, "\x1b[?1003h") {
		t.Fatalf("drawBufferMode() = %q, want full-motion mouse tracking while menu is visible", got)
	}
}

func TestDrawBufferModeShowsSelectionWhenUnfocused(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   3,
	})

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, nil, nil, fullRenderRequest(redrawInitial), state, nil, newMenuState(), theme, false); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.selectionPrefix()+"lp") {
		t.Fatalf("drawBufferMode() = %q, want painted selection while buffer is unfocused", got)
	}
	if !strings.Contains(got, "\x1b[?25l") {
		t.Fatalf("drawBufferMode() = %q, want hidden terminal cursor while unfocused", got)
	}
}

func TestDrawBufferLineShowsDarkerCollapsedSelectionInOverlay(t *testing.T) {
	t.Parallel()

	prevCols := termCols
	termCols = 16
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.visible = true
	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   1,
	})

	var out bytes.Buffer
	if err := drawBufferLine(&out, state, 0, 5, true, theme); err != nil {
		t.Fatalf("drawBufferLine() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.cursorPrefix()+"l") {
		t.Fatalf("drawBufferLine() = %q, want darker collapsed-selection cursor tint", got)
	}
	if strings.Contains(got, theme.selectionPrefix()+"l") {
		t.Fatalf("drawBufferLine() = %q, want collapsed selection to avoid normal selection tint", got)
	}
}

func TestDrawBufferLineKeepsOneRuneSelectionDistinctFromCollapsedSelection(t *testing.T) {
	t.Parallel()

	prevCols := termCols
	termCols = 16
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.visible = true
	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   2,
	})

	var out bytes.Buffer
	if err := drawBufferLine(&out, state, 0, 5, true, theme); err != nil {
		t.Fatalf("drawBufferLine() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.selectionPrefix()+"l") {
		t.Fatalf("drawBufferLine() = %q, want one-rune selection to keep normal selection tint", got)
	}
	if strings.Contains(got, theme.cursorPrefix()+"l") {
		t.Fatalf("drawBufferLine() = %q, want one-rune selection to avoid collapsed-selection tint", got)
	}
}

func TestDrawBufferLineShowsCollapsedSelectionAtEndOfLine(t *testing.T) {
	t.Parallel()

	prevCols := termCols
	termCols = 16
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	overlay.visible = true
	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 5,
		DotEnd:   5,
	})

	var out bytes.Buffer
	if err := drawBufferLine(&out, state, 0, 5, true, theme); err != nil {
		t.Fatalf("drawBufferLine() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "alpha"+theme.cursorPrefix()+" ") {
		t.Fatalf("drawBufferLine() = %q, want tinted block at end-of-line cursor position", got)
	}
}

func TestDrawShimmerHUDLineRendersTextWithoutSpinnerGlyphs(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	var out bytes.Buffer
	if err := drawShimmerHUDLine(&out, 0, ",p", theme.commandPrefix(), theme); err != nil {
		t.Fatalf("drawShimmerHUDLine() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, ",p") {
		t.Fatalf("drawShimmerHUDLine() = %q, want command text", got)
	}
	if strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("drawShimmerHUDLine() = %q, want shimmer text instead of braille spinner", got)
	}
}

func TestShimmerIntensityKeepsCommandVisibleOffBand(t *testing.T) {
	t.Parallel()

	if got := shimmerIntensity(0, 8, 1500*time.Millisecond); got < 0.28 {
		t.Fatalf("shimmerIntensity() = %f, want >= 0.28", got)
	}
}

func TestShimmerBandDimnessIsZeroOffBand(t *testing.T) {
	t.Parallel()

	if got := shimmerBandDimness(0, 8, 1500*time.Millisecond); got != 0 {
		t.Fatalf("shimmerBandDimness() = %f, want 0 off band", got)
	}
}

func TestStyleResetClearsForegroundToo(t *testing.T) {
	t.Parallel()

	if got, want := styleReset(), "\x1b[39;49;22;24;27m"; got != want {
		t.Fatalf("styleReset() = %q, want %q", got, want)
	}
}

func TestDrawOverlayHistoryLineShimmersRunningCommand(t *testing.T) {
	prevCols := termCols
	termCols = 20
	t.Cleanup(func() {
		termCols = prevCols
	})

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	overlay := newOverlayState()
	line := overlayRenderLine{text: ",p", history: 0, command: true, running: true}

	var out bytes.Buffer
	if err := drawOverlayHistoryLine(&out, 0, line, overlay, theme); err != nil {
		t.Fatalf("drawOverlayHistoryLine() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, ",p") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want running command text", got)
	}
	if strings.Contains(got, "running") {
		t.Fatalf("drawOverlayHistoryLine() = %q, want no separate running line text", got)
	}
}

func TestOverlayRenderLinesRespectsUpdatedTerminalHeight(t *testing.T) {
	prev := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prev
	})

	overlay := newOverlayState()
	overlay.visible = true
	for i := 0; i < 10; i++ {
		overlay.addOutput("line")
	}

	if got, want := overlayHeight(overlay), 4; got != want {
		t.Fatalf("overlayHeight(initial) = %d, want %d", got, want)
	}

	termRows = 20
	if got, want := overlayHeight(overlay), 10; got != want {
		t.Fatalf("overlayHeight(after resize) = %d, want %d", got, want)
	}
}

func TestHandleBufferKeyCtrlAAndCtrlE(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\nbeta\n",
		DotStart: 8,
		DotEnd:   8,
	})
	handleBufferKey(state, 1)
	if got, want := state.cursor, 6; got != want {
		t.Fatalf("Ctrl-A cursor = %d, want %d", got, want)
	}
	handleBufferKey(state, 5)
	if got, want := state.cursor, 10; got != want {
		t.Fatalf("Ctrl-E cursor = %d, want %d", got, want)
	}
}

func TestDrawBufferModeWrapsLongLines(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 4, 3
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "abcdef\n",
		DotStart: 0,
		DotEnd:   0,
	})

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, nil, nil, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[1;1H") || !strings.Contains(got, "abc") {
		t.Fatalf("drawBufferMode() = %q, want first wrapped row", got)
	}
	if !strings.Contains(got, "\x1b[2;1H") || !strings.Contains(got, "def") {
		t.Fatalf("drawBufferMode() = %q, want second wrapped row", got)
	}
}

func TestRevealOverlaySelectionAdjustsOriginWhenDotMovesInSameFile(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addCommand("/unicode")
	previous := newBufferState(wire.BufferView{
		ID:       7,
		Text:     "line1\nline2\nline3\nline4\nline5\nline6\n",
		DotStart: 0,
		DotEnd:   0,
	})
	previous.origin = visualRowStartForPos(previous.text, 0)
	next := newBufferState(wire.BufferView{
		ID:       7,
		Text:     "line1\nline2\nline3\nline4\nline5\nline6\n",
		DotStart: 24,
		DotEnd:   29,
	})
	next.origin = previous.origin

	revealed := revealOverlaySelection(previous, next, overlay)
	if !bufferPosVisible(revealed, overlay, revealed.dotStart) {
		t.Fatalf("dotStart %d not visible after reveal; origin=%d", revealed.dotStart, revealed.origin)
	}
}

func TestRevealOverlaySelectionPreservesOriginAcrossFileSwitches(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.visible = true
	previous := &bufferState{fileID: 7, origin: 12, dotStart: 0, dotEnd: 0}
	next := &bufferState{fileID: 8, origin: 12, dotStart: 24, dotEnd: 29}

	revealed := revealOverlaySelection(previous, next, overlay)
	if got, want := revealed.origin, 12; got != want {
		t.Fatalf("origin = %d, want preserved origin %d on file switch", got, want)
	}
}

func bufferPosVisible(state *bufferState, overlay *overlayState, pos int) bool {
	if state == nil {
		return false
	}
	pos = clampIndex(pos, len(state.text))
	target := visualRowStartForPos(state.text, pos)
	row := visualRowStartForPos(state.text, state.origin)
	limit := bufferViewRows(overlay)
	for i := 0; i < limit; i++ {
		if row == target {
			return true
		}
		next := nextVisualRowStart(state.text, row)
		if next == row {
			break
		}
		row = next
	}
	return false
}

func TestMoveLineDownUsesWrappedRows(t *testing.T) {
	prevCols := termCols
	termCols = 3
	t.Cleanup(func() {
		termCols = prevCols
	})

	text := []rune("abcdef\ngh\n")
	if got, want := moveLineDown(text, 1), 4; got != want {
		t.Fatalf("moveLineDown(wrapped) = %d, want %d", got, want)
	}
	if got, want := moveLineDown(text, 4), 8; got != want {
		t.Fatalf("moveLineDown(next line) = %d, want %d", got, want)
	}
}

func TestMoveLineDownFromWrappedBoundaryTreatsCursorAsNextRow(t *testing.T) {
	prevCols := termCols
	termCols = 3
	t.Cleanup(func() {
		termCols = prevCols
	})

	text := []rune("abcdef\ngh\n")
	if got, want := moveLineDown(text, 3), 7; got != want {
		t.Fatalf("moveLineDown(wrap boundary) = %d, want %d", got, want)
	}
}

func TestHasDirtyFiles(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		menuFiles: []wire.MenuFile{
			{Name: "clean.txt"},
			{Name: "dirty.txt", Dirty: true},
		},
	}

	dirty, err := hasDirtyFiles(svc)
	if err != nil {
		t.Fatalf("hasDirtyFiles() error = %v", err)
	}
	if !dirty {
		t.Fatalf("hasDirtyFiles() = false, want true")
	}
}

func TestHandleBufferKeyCtrlSpaceExtendsSelection(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 0,
		DotEnd:   0,
	})
	handleBufferKey(state, 0)
	handleBufferKey(state, keyRight)
	handleBufferKey(state, keyRight)

	if got, want := state.dotStart, 0; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := state.dotEnd, 2; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
	if !state.markMode {
		t.Fatalf("markMode = false, want true")
	}
}

func TestApplyBufferKeyPrintableReplacesSelection(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 0,
			DotEnd:   0,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, int('Z'))
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "Zalpha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 1; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
	if got, want := next.dotStart, 1; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := next.dotEnd, 1; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyPrintableRefreshesDirtyState(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			ID:       7,
			Name:     "alpha.txt",
			Text:     "alpha\n",
			DotStart: 0,
			DotEnd:   0,
		},
		menuFiles: []wire.MenuFile{
			{ID: 7, Name: "alpha.txt", Dirty: true, Current: true},
		},
	}
	state := newBufferState(svc.view)
	if state.dirty {
		t.Fatalf("initial state.dirty = true, want false before refresh")
	}

	next, err := applyBufferKey(svc, state, int('Z'))
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if !next.dirty {
		t.Fatalf("next.dirty = false, want true after replace")
	}
}

func TestApplyBufferKeyMovementSyncsDotToService(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 0,
			DotEnd:   0,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, keyRight)
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := svc.setDotCalls, 1; got != want {
		t.Fatalf("SetDot calls = %d, want %d", got, want)
	}
	if got, want := svc.lastDotStart, 1; got != want {
		t.Fatalf("last dot start = %d, want %d", got, want)
	}
	if got, want := svc.lastDotEnd, 1; got != want {
		t.Fatalf("last dot end = %d, want %d", got, want)
	}
	if got, want := next.cursor, 1; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
	if got, want := next.dotStart, 1; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := next.dotEnd, 1; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyCtrlSpaceSyncsSelectionToService(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 0,
			DotEnd:   0,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, 0)
	if err != nil {
		t.Fatalf("toggle mark applyBufferKey() error = %v", err)
	}
	next, err = applyBufferKey(svc, next, keyRight)
	if err != nil {
		t.Fatalf("move with mark applyBufferKey() error = %v", err)
	}
	next, err = applyBufferKey(svc, next, keyRight)
	if err != nil {
		t.Fatalf("second move with mark applyBufferKey() error = %v", err)
	}

	if got, want := svc.setDotCalls, 3; got != want {
		t.Fatalf("SetDot calls = %d, want %d", got, want)
	}
	if got, want := svc.lastDotStart, 0; got != want {
		t.Fatalf("last dot start = %d, want %d", got, want)
	}
	if got, want := svc.lastDotEnd, 2; got != want {
		t.Fatalf("last dot end = %d, want %d", got, want)
	}
	if got, want := next.dotStart, 0; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := next.dotEnd, 2; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
	if !next.markMode {
		t.Fatalf("markMode = false, want true")
	}
}

func TestCopyBufferSelectionCopiesToClipboard(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 1,
		DotEnd:   4,
	})

	var out bytes.Buffer
	snarf, status, err := copyBufferSelection(&out, state)
	if err != nil {
		t.Fatalf("copyBufferSelection() error = %v", err)
	}
	if got, want := string(snarf), "lph"; got != want {
		t.Fatalf("snarf = %q, want %q", got, want)
	}
	if got, want := status, "snarfed"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	wantOSC52 := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("lph")) + "\x07"
	if got := out.String(); got != wantOSC52 {
		t.Fatalf("clipboard output = %q, want %q", got, wantOSC52)
	}
}

func TestCutBufferSelectionDeletesSelection(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 1,
			DotEnd:   4,
		},
	}
	state := newBufferState(svc.view)

	var out bytes.Buffer
	next, snarf, status, err := cutBufferSelection(&out, svc, state)
	if err != nil {
		t.Fatalf("cutBufferSelection() error = %v", err)
	}
	if got, want := string(snarf), "lph"; got != want {
		t.Fatalf("snarf = %q, want %q", got, want)
	}
	if got, want := status, "cut"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := string(next.text), "aa\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	wantOSC52 := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("lph")) + "\x07"
	if got := out.String(); got != wantOSC52 {
		t.Fatalf("clipboard output = %q, want %q", got, wantOSC52)
	}
}

func TestPasteBufferSnarfReplacesSelection(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 1,
			DotEnd:   4,
		},
	}
	state := newBufferState(svc.view)

	next, status, err := pasteBufferSnarf(svc, state, []rune("XYZ"))
	if err != nil {
		t.Fatalf("pasteBufferSnarf() error = %v", err)
	}
	if got, want := status, ""; got != want {
		t.Fatalf("status = %q, want empty", got)
	}
	if got, want := string(next.text), "aXYZa\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 4; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyBackspaceDeletesPreviousRune(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 2,
			DotEnd:   2,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, 127)
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "apha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 1; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyPrintableAdvancesAcrossRepeatedTyping(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 0,
			DotEnd:   0,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, int('X'))
	if err != nil {
		t.Fatalf("first applyBufferKey() error = %v", err)
	}
	next, err = applyBufferKey(svc, next, int('Y'))
	if err != nil {
		t.Fatalf("second applyBufferKey() error = %v", err)
	}

	if got, want := string(next.text), "XYalpha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 2; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyNewlineCopiesLeadingWhitespace(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "  alpha\n",
			DotStart: 4,
			DotEnd:   4,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKeyWithOptions(svc, state, '\n', Options{AutoIndent: true})
	if err != nil {
		t.Fatalf("applyBufferKeyWithOptions() error = %v", err)
	}
	if got, want := string(next.text), "  al\n  pha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 7; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyNewlineWithoutAutoIndent(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "  alpha\n",
			DotStart: 4,
			DotEnd:   4,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKeyWithOptions(svc, state, '\n', Options{AutoIndent: false})
	if err != nil {
		t.Fatalf("applyBufferKeyWithOptions() error = %v", err)
	}
	if got, want := string(next.text), "  al\npha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 5; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyCtrlKDeletesToLineEnd(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\nbeta\n",
			DotStart: 2,
			DotEnd:   2,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, 11)
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "al\nbeta\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 2; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestFindSelectionForwardWraps(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta alpha")
	if got, ok := findSelection(text, 11, 16, []rune("alpha"), true); !ok || got != 0 {
		t.Fatalf("findSelection(forward wrap) = (%d,%v), want (0,true)", got, ok)
	}
}

func TestFindSelectionBackwardFindsPrevious(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta alpha")
	if got, ok := findSelection(text, 11, 16, []rune("alpha"), false); !ok || got != 0 {
		t.Fatalf("findSelection(backward) = (%d,%v), want (0,true)", got, ok)
	}
}

func TestPrevAndNextWordStart(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta_gamma delta")
	if got, want := nextWordStart(text, 0), 6; got != want {
		t.Fatalf("nextWordStart() = %d, want %d", got, want)
	}
	if got, want := prevWordStart(text, len(text)), 17; got != want {
		t.Fatalf("prevWordStart() = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyAltBackspaceDeletesWord(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha beta\n",
			DotStart: 10,
			DotEnd:   10,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, keyAltBackspace)
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "alpha \n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 6; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestReadBufferKeyPaste(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[200~hello"))
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC: %v", err)
	}
	got, err := readBufferKey(reader)
	if err != nil {
		t.Fatalf("readBufferKey() error = %v", err)
	}
	if got != keyPaste {
		t.Fatalf("readBufferKey() = %d, want keyPaste", got)
	}
}

func TestReadBracketedPaste(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("hello\x1b[201~tail"))
	got, err := readBracketedPaste(reader)
	if err != nil {
		t.Fatalf("readBracketedPaste() error = %v", err)
	}
	if want := "hello"; string(got) != want {
		t.Fatalf("readBracketedPaste() = %q, want %q", string(got), want)
	}
}
