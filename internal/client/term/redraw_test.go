package term

import "testing"

func TestClassifyBufferRedrawCursorMove(t *testing.T) {
	t.Parallel()

	prev := &bufferState{text: []rune("alpha"), cursor: 1, dotStart: 1, dotEnd: 1}
	next := &bufferState{text: prev.text, cursor: 2, dotStart: 2, dotEnd: 2}
	if got, want := classifyBufferRedraw(prev, next), redrawBufferCursor; got != want {
		t.Fatalf("classifyBufferRedraw() = %q, want %q", got, want)
	}
}

func TestClassifyBufferRedrawSelectionChange(t *testing.T) {
	t.Parallel()

	prev := &bufferState{text: []rune("alpha"), cursor: 1, dotStart: 1, dotEnd: 1}
	next := &bufferState{text: prev.text, cursor: 1, dotStart: 1, dotEnd: 3}
	if got, want := classifyBufferRedraw(prev, next), redrawBufferSelection; got != want {
		t.Fatalf("classifyBufferRedraw() = %q, want %q", got, want)
	}
}

func TestClassifyBufferRedrawViewportAndContent(t *testing.T) {
	t.Parallel()

	prev := &bufferState{text: []rune("alpha"), origin: 0}
	viewport := &bufferState{text: prev.text, origin: 2}
	if got, want := classifyBufferRedraw(prev, viewport), redrawBufferViewport; got != want {
		t.Fatalf("viewport classifyBufferRedraw() = %q, want %q", got, want)
	}

	content := &bufferState{text: []rune("beta"), origin: 0}
	if got, want := classifyBufferRedraw(prev, content), redrawBufferContent; got != want {
		t.Fatalf("content classifyBufferRedraw() = %q, want %q", got, want)
	}
}

func TestRedrawNeedsFullFrameAllowsOverlayInputDiffOnly(t *testing.T) {
	t.Parallel()

	for _, class := range []redrawClass{
		redrawBufferCursor,
		redrawBufferContent,
		redrawBufferStatus,
		redrawOverlayInput,
	} {
		if redrawNeedsFullFrame(class) {
			t.Fatalf("redrawNeedsFullFrame(%q) = true, want false", class)
		}
	}

	for _, class := range []redrawClass{
		redrawOverlayHistory,
		redrawOverlayOpen,
		redrawOverlayClose,
		redrawMenuOpen,
		redrawMenuClose,
		redrawTheme,
	} {
		if !redrawNeedsFullFrame(class) {
			t.Fatalf("redrawNeedsFullFrame(%q) = false, want true", class)
		}
	}
}
