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
