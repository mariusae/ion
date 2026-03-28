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

func TestExplicitRenderRequestsMapToIncrementalInvalidation(t *testing.T) {
	t.Parallel()

	if got, want := bufferRenderRequest(redrawBufferViewport, &bufferState{}, nil, newMenuState(), true).invalidation, renderInvalidateBuffer; got != want {
		t.Fatalf("buffer viewport invalidation = %v, want %v", got, want)
	}
	if got, want := renderRequestForLayers(redrawOverlayInput, renderInvalidateOverlayInput).invalidation, renderInvalidateOverlayInput; got != want {
		t.Fatalf("overlay input invalidation = %v, want %v", got, want)
	}
	if got, want := renderRequestForLayers(redrawOverlayOpen, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput).invalidation, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput; got != want {
		t.Fatalf("overlay open invalidation = %v, want %v", got, want)
	}
	if got, want := renderRequestForLayers(redrawMenuOpen, renderInvalidateMenu).invalidation, renderInvalidateMenu; got != want {
		t.Fatalf("menu open invalidation = %v, want %v", got, want)
	}
	if got, want := fullRenderRequest(redrawTheme).invalidation, renderInvalidateAllLayers; got != want {
		t.Fatalf("theme invalidation = %v, want %v", got, want)
	}
}

func TestLegacyBuildRenderRequestMatchesOverlayCompatibilityMapping(t *testing.T) {
	t.Parallel()

	if got, want := buildRenderRequest(redrawOverlayOpen, false, &bufferState{}, newOverlayState(), newMenuState(), true).invalidation, renderInvalidateBuffer|renderInvalidateOverlayHistory|renderInvalidateOverlayInput; got != want {
		t.Fatalf("legacy overlay open invalidation = %v, want %v", got, want)
	}
	if got, want := buildRenderRequest(redrawTheme, false, &bufferState{}, nil, newMenuState(), true).invalidation, renderInvalidateAllLayers; got != want {
		t.Fatalf("legacy theme invalidation = %v, want %v", got, want)
	}
}
