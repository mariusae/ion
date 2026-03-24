package term

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
	"time"

	"ion/internal/proto/wire"
)

type fakeTermService struct {
	view      wire.BufferView
	menuFiles []wire.MenuFile
	focusID   int
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

func (f *fakeTermService) MenuFiles() ([]wire.MenuFile, error) {
	return append([]wire.MenuFile(nil), f.menuFiles...), nil
}

func (f *fakeTermService) FocusFile(id int) (wire.BufferView, error) {
	f.focusID = id
	for _, file := range f.menuFiles {
		if file.ID != id {
			continue
		}
		f.view.Name = file.Name
		break
	}
	return f.view, nil
}

func (f *fakeTermService) SetDot(start, end int) (wire.BufferView, error) {
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

func TestMovePageDownByLines(t *testing.T) {
	t.Parallel()

	text := []rune("l1\nl2\nl3\nl4\nl5\n")
	if got, want := movePageDown(text, 0, 2), 6; got != want {
		t.Fatalf("movePageDown() = %d, want %d", got, want)
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

func TestDrawBufferLineSuppressesSelectionDuringFlash(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 0,
		DotEnd:   2,
	})
	state.flashSelection = true

	var out bytes.Buffer
	if err := drawBufferLine(&out, state, 0, 5, nil); err != nil {
		t.Fatalf("drawBufferLine() error = %v", err)
	}
	if strings.Contains(out.String(), "\x1b[7m") {
		t.Fatalf("drawBufferLine() = %q, want selection highlight suppressed", out.String())
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
	if err := drawBufferMode(&out, state, nil, newMenuState(), nil); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[?25h\x1b[6 q") {
		t.Fatalf("drawBufferMode() = %q, want steady bar cursor sequence", got)
	}
	if !strings.Contains(got, "\x1b[?1004h") {
		t.Fatalf("drawBufferMode() = %q, want focus reporting enabled", got)
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
	if err := drawBufferMode(&out, state, nil, newMenuState(), nil); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b]2;alpha.txt\x07") {
		t.Fatalf("drawBufferMode() = %q, want basename-only window title", got)
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
	if err := positionTerminalCursor(&out, nil, overlay); err != nil {
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
	if err := positionTerminalCursor(&out, state, nil); err != nil {
		t.Fatalf("positionTerminalCursor() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b[?25h\x1b[1;2H") {
		t.Fatalf("positionTerminalCursor() = %q, want visible cursor positioning sequence", got)
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

func TestDrawOverlayPromptUsesChevronAndOverlayTint(t *testing.T) {
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
	if !strings.Contains(got, theme.hudPrefix()+"› ,p") {
		t.Fatalf("drawOverlayPrompt() = %q, want chevron prompt with overlay tint", got)
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
	if err := drawBufferMode(&out, state, overlay, newMenuState(), theme); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[4;1H\x1b[2K"+theme.hudPrefix()) {
		t.Fatalf("drawBufferMode() = %q, want tinted top HUD padding row", got)
	}
	if !strings.Contains(got, "\x1b[6;1H\x1b[2K"+theme.hudPrefix()) {
		t.Fatalf("drawBufferMode() = %q, want tinted bottom HUD padding row", got)
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
	if err := drawBufferMode(&out, state, nil, newMenuState(), nil); err != nil {
		t.Fatalf("drawBufferMode() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[1;1H\x1b[2Kabc") {
		t.Fatalf("drawBufferMode() = %q, want first wrapped row", got)
	}
	if !strings.Contains(got, "\x1b[2;1H\x1b[2Kdef") {
		t.Fatalf("drawBufferMode() = %q, want second wrapped row", got)
	}
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
