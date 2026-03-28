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
	if err := renderer.Draw(&out, redrawInitial, state, nil, newMenuState(), nil, true, true, nil); err != nil {
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

	if err := renderer.Draw(&out, redrawBufferViewport, next, nil, newMenuState(), nil, true, false, nil); err != nil {
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
	if err := renderer.Draw(&out, redrawInitial, state, overlay, newMenuState(), nil, true, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	out.Reset()
	overlay.insert([]rune("p"))
	if err := renderer.Draw(&out, redrawOverlayInput, state, overlay, newMenuState(), nil, true, false, nil); err != nil {
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
	if err := renderer.Draw(&out, redrawInitial, state, nil, menu, nil, true, true, nil); err != nil {
		t.Fatalf("Draw(initial) error = %v", err)
	}

	out.Reset()
	menu.hover = 1
	if err := renderer.Draw(&out, redrawMenuHover, state, nil, menu, nil, true, false, nil); err != nil {
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
	if err := drawBufferMode(&out, renderer, nil, redrawInitial, state, nil, menu, nil, true, true); err != nil {
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
	if err := drawBufferMode(&out, renderer, nil, redrawMenuOpen, state, nil, menu, nil, true, false); err != nil {
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
	if err := drawBufferMode(&out, renderer, nil, redrawMenuClose, state, nil, menu, nil, true, false); err != nil {
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
	if err := drawBufferMode(&out, renderer, nil, redrawInitial, state, overlay, newMenuState(), nil, true, true); err != nil {
		t.Fatalf("drawBufferMode(initial) error = %v", err)
	}

	overlay.open(",")
	overlay.addOutput("hello")
	out.Reset()
	if err := drawBufferMode(&out, renderer, nil, redrawOverlayOpen, state, overlay, newMenuState(), nil, true, false); err != nil {
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
	if err := drawBufferMode(&out, renderer, nil, redrawOverlayClose, state, overlay, newMenuState(), nil, true, false); err != nil {
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
