package term

func snapshotBufferState(state *bufferState) *bufferState {
	if state == nil {
		return nil
	}
	clone := *state
	clone.layout = nil
	return &clone
}

func sameRuneSlices(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	if &a[0] == &b[0] {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func classifyBufferRedraw(previous, next *bufferState) redrawClass {
	if previous == nil || next == nil {
		return redrawBufferContent
	}
	if previous.fileID != next.fileID || !sameRuneSlices(previous.text, next.text) {
		return redrawBufferContent
	}
	if previous.origin != next.origin {
		return redrawBufferViewport
	}
	if previous.status != next.status {
		return redrawBufferStatus
	}
	if previous.dotStart != next.dotStart ||
		previous.dotEnd != next.dotEnd ||
		previous.markMode != next.markMode ||
		previous.markPos != next.markPos ||
		previous.flashSelection != next.flashSelection {
		if previous.dotStart == previous.dotEnd &&
			next.dotStart == next.dotEnd &&
			previous.cursor != next.cursor {
			return redrawBufferCursor
		}
		return redrawBufferSelection
	}
	if previous.cursor != next.cursor {
		return redrawBufferCursor
	}
	return redrawBufferContent
}

func classifyBufferRenderRequest(previous, next *bufferState, overlay *overlayState, menu *menuState, focused bool) renderRequest {
	class := classifyBufferRedraw(previous, next)
	return buildRenderRequest(class, false, next, overlay, menu, focused)
}
