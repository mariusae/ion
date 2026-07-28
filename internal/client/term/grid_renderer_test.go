package term

import (
	"bytes"
	"strings"
	"testing"

	"ion/internal/proto/wire"
)

func TestGridRendererViewportScrollUsesScrollOperation(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 4, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "one\ntwo\nthree\nfour\nfive\n",
		DotStart: 0,
		DotEnd:   0,
	})

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	out.Reset()
	next := newBufferStateWithPrevious(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "one\ntwo\nthree\nfour\nfive\n",
		DotStart: 0,
		DotEnd:   0,
	}, state)
	next.origin = nextVisualRowStart(next.text, state.origin)

	if err := renderer.Draw(&out, bufferRenderRequest(redrawBufferViewport, nil, newMenuState(), true), next, nil, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(viewport) error = %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "\x1b[1M") {
		t.Fatalf("Draw(viewport) = %q, want line-delete scroll operation", got)
	}
	if strings.Contains(got, "\x1b[2J") {
		t.Fatalf("Draw(viewport) = %q, want incremental redraw without full clear", got)
	}
}

func TestGridRendererPaintsScrollbarInRightColumn(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 4, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 20, g: 20, b: 20}, colorModeTrueColor)
	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "one\ntwo\nthree\nfour\nfive\nsix\nseven\n",
		DotStart: 0,
		DotEnd:   0,
	})
	for i := 0; i < 2; i++ {
		state.origin = nextVisualRowStart(state.text, state.origin)
	}

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, nil, newMenuState(), theme, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	gutterStyle := renderer.palette.ID(scrollbarPrefix(theme, false, false))
	thumbStyle := renderer.palette.ID(scrollbarPrefix(theme, true, false))
	start, end := scrollbarThumbRows(state, nil, termRows)
	for row := 0; row < termRows; row++ {
		cell := renderer.root.rowCells(row)[scrollbarColumn()]
		wantRune := ' '
		wantStyle := gutterStyle
		if row >= start && row < end {
			wantRune = '█'
			wantStyle = thumbStyle
		}
		if cell.r != wantRune {
			t.Fatalf("scrollbar cell row %d rune = %q, want %q", row, cell.r, wantRune)
		}
		if cell.style != wantStyle {
			t.Fatalf("scrollbar cell row %d style = %d, want %d", row, cell.style, wantStyle)
		}
	}
	if start == 0 || end <= start {
		t.Fatalf("scrollbarThumbRows() = %d,%d, want non-empty thumb below top", start, end)
	}
}

func TestGridRendererPaintsFullHeightScrollbarForShortBuffer(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	theme := buildTheme(rgbColor{r: 20, g: 20, b: 20}, colorModeTrueColor)
	for _, text := range []string{"", "one\ntwo\n"} {
		renderer := newGridRenderer()
		state := newBufferState(wire.BufferView{
			Name:     "/tmp/short.txt",
			Text:     text,
			DotStart: 0,
			DotEnd:   0,
		})

		var out bytes.Buffer
		if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, nil, newMenuState(), theme, true, nil); err != nil {
			t.Fatalf("Draw(%q) error = %v", text, err)
		}

		thumbStyle := renderer.palette.ID(scrollbarFullThumbPrefix(theme, false))
		start, end := scrollbarThumbRows(state, nil, termRows)
		if start != 0 || end != termRows {
			t.Fatalf("scrollbarThumbRows(%q) = %d,%d, want 0,%d", text, start, end, termRows)
		}
		for row := 0; row < termRows; row++ {
			cell := renderer.root.rowCells(row)[scrollbarColumn()]
			if cell.r != ' ' {
				t.Fatalf("scrollbar cell row %d for %q rune = %q, want background thumb fill", row, text, cell.r)
			}
			if cell.style != thumbStyle {
				t.Fatalf("scrollbar cell row %d for %q style = %d, want thumb style %d", row, text, cell.style, thumbStyle)
			}
		}
	}
}

func TestScrollbarPrefixMatchesHUDTints(t *testing.T) {
	t.Parallel()

	theme := buildTheme(rgbColor{r: 20, g: 20, b: 20}, colorModeTrueColor)
	if got, want := scrollbarPrefix(theme, false, false), theme.hudPrefix(); got != want {
		t.Fatalf("scrollbar gutter prefix = %q, want HUD prefix %q", got, want)
	}
	thumb := scrollbarPrefix(theme, true, false)
	for _, want := range []string{theme.bgCode(theme.hudBG), theme.fgCode(theme.outputBG)} {
		if !strings.Contains(thumb, want) {
			t.Fatalf("scrollbar thumb prefix = %q, want component %q", thumb, want)
		}
	}
}

func TestScrollbarFullThumbPrefixUsesThumbBackground(t *testing.T) {
	t.Parallel()

	theme := buildTheme(rgbColor{r: 20, g: 20, b: 20}, colorModeTrueColor)
	full := scrollbarFullThumbPrefix(theme, false)
	if !strings.Contains(full, theme.bgCode(theme.outputBG)) {
		t.Fatalf("scrollbar full thumb prefix = %q, want output background %q", full, theme.bgCode(theme.outputBG))
	}
	if strings.Contains(full, theme.bgCode(theme.hudBG)) {
		t.Fatalf("scrollbar full thumb prefix = %q, want thumb background instead of gutter background", full)
	}
}

func TestScrollbarPrefixScalesInactiveTints(t *testing.T) {
	t.Parallel()

	theme := buildTheme(rgbColor{r: 20, g: 20, b: 20}, colorModeTrueColor)
	active := scrollbarPrefix(theme, true, false)
	inactive := scrollbarPrefix(theme, true, true)
	if active == inactive {
		t.Fatalf("inactive scrollbar thumb prefix = active prefix %q, want scaled style", active)
	}
	inactiveBG := scaleScrollbarTint(theme, theme.hudBG)
	inactiveFG := scaleScrollbarTint(theme, theme.outputBG)
	for _, want := range []string{theme.bgCode(inactiveBG), theme.fgCode(inactiveFG)} {
		if !strings.Contains(inactive, want) {
			t.Fatalf("inactive scrollbar thumb prefix = %q, want component %q", inactive, want)
		}
	}
	if strings.Contains(inactive, theme.bgCode(theme.hudBG)) || strings.Contains(inactive, theme.fgCode(theme.outputBG)) {
		t.Fatalf("inactive scrollbar thumb prefix = %q, want scaled colors instead of active colors", inactive)
	}

	activeGutter := scrollbarPrefix(theme, false, false)
	inactiveGutter := scrollbarPrefix(theme, false, true)
	if activeGutter == inactiveGutter {
		t.Fatalf("inactive scrollbar gutter prefix = active prefix %q, want scaled style", activeGutter)
	}
	if !strings.Contains(inactiveGutter, theme.bgCode(inactiveBG)) {
		t.Fatalf("inactive scrollbar gutter prefix = %q, want scaled HUD background", inactiveGutter)
	}
	if strings.Contains(inactiveGutter, theme.bgCode(theme.hudBG)) {
		t.Fatalf("inactive scrollbar gutter prefix = %q, want scaled color instead of active color", inactiveGutter)
	}
}

func TestGridRendererOverlayInputRedrawTouchesPromptRowOnly(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	overlay := newOverlayState()
	overlay.open(",")

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, overlay, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	out.Reset()
	overlay.insert([]rune("p"))
	if err := renderer.Draw(&out, renderRequestForLayers(redrawOverlayInput, renderInvalidateOverlayInput), state, overlay, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(overlay input) error = %v", err)
	}

	got := out.String()
	if strings.Contains(got, "\x1b[2J") {
		t.Fatalf("Draw(overlay input) = %q, want no full clear", got)
	}
	if !strings.Contains(got, "\x1b[5;") {
		t.Fatalf("Draw(overlay input) = %q, want prompt row repaint", got)
	}
	for _, unwanted := range []string{"\x1b[1;1H", "\x1b[4;1H", "\x1b[6;1H"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("Draw(overlay input) = %q, want no repaint for unaffected row %q", got, unwanted)
		}
	}
}

func TestGridRendererOverlayPasteRedrawStaysIncremental(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	overlay := newOverlayState()
	overlay.open(",")

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, overlay, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	out.Reset()
	overlay.insert([]rune("paste"))
	if err := renderer.Draw(&out, renderRequestForLayers(redrawOverlayInput, renderInvalidateOverlayInput), state, overlay, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(overlay paste) error = %v", err)
	}

	got := out.String()
	if strings.Contains(got, "\x1b[2J") {
		t.Fatalf("Draw(overlay paste) = %q, want no full clear", got)
	}
	if !strings.Contains(got, "\x1b[5;") {
		t.Fatalf("Draw(overlay paste) = %q, want prompt row repaint", got)
	}
}

func TestGridRendererRendersCarriageReturnVisibly(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 4, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/cr.txt",
		Text:     "a\rb\n",
		DotStart: 0,
		DotEnd:   0,
	})

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "a␍b") {
		t.Fatalf("Draw(initial) = %q, want visible carriage return glyph", got)
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("Draw(initial) = %q, want no raw carriage return", got)
	}
}

func TestGridRendererResetReplaysTerminalSetup(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 4, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 1,
		DotEnd:   1,
	})

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	renderer.Reset()
	out.Reset()
	if err := renderer.Draw(&out, fullRenderRequest(redrawResize), state, nil, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(resize) error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"\x1b[?1049h", "\x1b[6 q", "\x1b[?1003h", "\x1b[r\x1b[H\x1b[2J"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Draw(resize) = %q, want %q after reset", got, want)
		}
	}
}

func TestGridRendererMenuHoverRedrawIsIncremental(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 8, 30
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	menu := &menuState{
		visible: true,
		x:       2,
		y:       1,
		width:   12,
		height:  4,
		hover:   0,
		title:   " menu ",
		items: []menuItem{
			{label: " one", kind: menuCut},
			{label: " two", kind: menuCut},
		},
	}

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, nil, menu, nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	out.Reset()
	menu.hover = 1
	if err := renderer.Draw(&out, renderRequestForLayers(redrawMenuHover, renderInvalidateMenu), state, nil, menu, nil, true, nil); err != nil {
		t.Fatalf("Draw(menu hover) error = %v", err)
	}

	got := out.String()
	if strings.Contains(got, "\x1b[2J") {
		t.Fatalf("Draw(menu hover) = %q, want no full clear", got)
	}
	for _, wanted := range []string{"\x1b[3;", "\x1b[4;"} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("Draw(menu hover) = %q, want repaint for menu row %q", got, wanted)
		}
	}
	for _, unwanted := range []string{"\x1b[1;", "\x1b[2;", "\x1b[5;"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("Draw(menu hover) = %q, want no repaint for unaffected row prefix %q", got, unwanted)
		}
	}
}

func TestDrawBufferModeMenuOpenAndCloseStayIncremental(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 8, 30
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	menu := newMenuState()

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, renderer, nil, fullRenderRequest(redrawInitial), state, nil, menu, nil, true); err != nil {
		t.Fatalf("drawBufferMode(initial) error = %v", err)
	}

	menu.visible = true
	menu.x = 2
	menu.y = 1
	menu.width = 12
	menu.height = 4
	menu.title = " menu "
	menu.items = []menuItem{
		{label: " one", kind: menuCut},
		{label: " two", kind: menuCut},
	}

	out.Reset()
	if err := drawBufferModeRequest(&out, renderer, nil, renderRequestForLayers(redrawMenuOpen, renderInvalidateMenu), state, nil, menu, nil, true); err != nil {
		t.Fatalf("drawBufferMode(menu open) error = %v", err)
	}
	gotOpen := out.String()
	if strings.Contains(gotOpen, "\x1b[2J") {
		t.Fatalf("drawBufferMode(menu open) = %q, want no full clear", gotOpen)
	}
	if !strings.Contains(gotOpen, "\x1b[2;") {
		t.Fatalf("drawBufferMode(menu open) = %q, want menu-area repaint", gotOpen)
	}

	menu.dismiss()
	out.Reset()
	if err := drawBufferModeRequest(&out, renderer, nil, renderRequestForLayers(redrawMenuClose, renderInvalidateMenu), state, nil, menu, nil, true); err != nil {
		t.Fatalf("drawBufferMode(menu close) error = %v", err)
	}
	gotClose := out.String()
	if strings.Contains(gotClose, "\x1b[2J") {
		t.Fatalf("drawBufferMode(menu close) = %q, want no full clear", gotClose)
	}
	if !strings.Contains(gotClose, "\x1b[2;") {
		t.Fatalf("drawBufferMode(menu close) = %q, want menu-area recomposition", gotClose)
	}
}

func TestDrawBufferModeOverlayOpenAndCloseStayIncremental(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 8, 30
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\ngamma\n",
		DotStart: 0,
		DotEnd:   0,
	})
	overlay := newOverlayState()

	var out bytes.Buffer
	if err := drawBufferModeRequest(&out, renderer, nil, fullRenderRequest(redrawInitial), state, overlay, newMenuState(), nil, true); err != nil {
		t.Fatalf("drawBufferMode(initial) error = %v", err)
	}

	overlay.open(",")
	overlay.addOutput("hello")
	out.Reset()
	if err := drawBufferModeRequest(&out, renderer, nil, renderRequestForLayers(redrawOverlayOpen, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput), state, overlay, newMenuState(), nil, true); err != nil {
		t.Fatalf("drawBufferMode(overlay open) error = %v", err)
	}
	gotOpen := out.String()
	if strings.Contains(gotOpen, "\x1b[2J") {
		t.Fatalf("drawBufferMode(overlay open) = %q, want no full clear", gotOpen)
	}
	if !strings.Contains(gotOpen, "\x1b[6;") {
		t.Fatalf("drawBufferMode(overlay open) = %q, want lower-screen recomposition", gotOpen)
	}

	overlay.close()
	out.Reset()
	if err := drawBufferModeRequest(&out, renderer, nil, renderRequestForLayers(redrawOverlayClose, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput), state, overlay, newMenuState(), nil, true); err != nil {
		t.Fatalf("drawBufferMode(overlay close) error = %v", err)
	}
	gotClose := out.String()
	if strings.Contains(gotClose, "\x1b[2J") {
		t.Fatalf("drawBufferMode(overlay close) = %q, want no full clear", gotClose)
	}
	if !strings.Contains(gotClose, "\x1b[6;") {
		t.Fatalf("drawBufferMode(overlay close) = %q, want lower-screen recomposition", gotClose)
	}
}

func TestGridRendererBufferCursorRedrawMovesCursorOnly(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 4, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, nil, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	next := newBufferStateWithPrevious(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 1,
		DotEnd:   1,
	}, state)
	out.Reset()
	if err := renderer.Draw(&out, bufferRenderRequest(redrawBufferCursor, nil, newMenuState(), true), next, nil, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(cursor) error = %v", err)
	}
	if got, want := out.String(), "\x1b[?25h\x1b[1;2H"; got != want {
		t.Fatalf("Draw(cursor) = %q, want %q", got, want)
	}
}

func TestGridRendererOverlayHistoryRedrawRestoresUnchangedVisibleCursor(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 20
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	renderer := newGridRenderer()
	state := newBufferState(wire.BufferView{
		Name:     "/tmp/alpha.txt",
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	overlay := newOverlayState()
	overlay.open(",")

	var out bytes.Buffer
	if err := renderer.Draw(&out, fullRenderRequest(redrawInitial), state, overlay, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	overlay.addOutput("done")
	out.Reset()
	if err := renderer.Draw(&out, renderRequestForLayers(redrawOverlayHistory, renderInvalidateOverlayHistory), state, overlay, newMenuState(), nil, true, nil); err != nil {
		t.Fatalf("Draw(overlay history) error = %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "\x1b[?25h") {
		t.Fatalf("Draw(overlay history) = %q, want visible terminal cursor restored after repaint", got)
	}
	if !strings.Contains(got, "\x1b[5;2H") {
		t.Fatalf("Draw(overlay history) = %q, want cursor restored to overlay prompt row", got)
	}
}
