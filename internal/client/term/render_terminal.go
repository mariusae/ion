package term

import (
	"fmt"
	"io"
)

func buildFrameCursor(state *bufferState, overlay *overlayState, menu *menuState, focused bool) frameCursor {
	if overlay != nil && overlay.visible && overlay.running {
		return frameCursor{}
	}
	if !focused || (menu != nil && menu.visible) {
		return frameCursor{}
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
	return frameCursor{
		visible: true,
		row:     row,
		col:     col,
	}
}

func writeFrameTerminalState(stdout io.Writer, prev, next frameTerminalState) error {
	if prev.altScreen != next.altScreen {
		seq := "\x1b[?1049l"
		if next.altScreen {
			seq = "\x1b[?1049h"
		}
		if _, err := io.WriteString(stdout, seq); err != nil {
			return err
		}
	}
	if prev.cursorShape != next.cursorShape {
		shape := "2"
		if next.cursorShape == frameCursorShapeBar {
			shape = "6"
		}
		if _, err := fmt.Fprintf(stdout, "\x1b[%s q", shape); err != nil {
			return err
		}
	}
	for _, mode := range []struct {
		prev bool
		next bool
		on   string
		off  string
	}{
		{prev.mousePress, next.mousePress, "\x1b[?1002h", "\x1b[?1002l"},
		{prev.mouseMotion, next.mouseMotion, "\x1b[?1003h", "\x1b[?1003l"},
		{prev.focusReporting, next.focusReporting, "\x1b[?1004h", "\x1b[?1004l"},
		{prev.mouseSGR, next.mouseSGR, "\x1b[?1006h", "\x1b[?1006l"},
		{prev.bracketedPaste, next.bracketedPaste, "\x1b[?2004h", "\x1b[?2004l"},
	} {
		if mode.prev == mode.next {
			continue
		}
		seq := mode.off
		if mode.next {
			seq = mode.on
		}
		if _, err := io.WriteString(stdout, seq); err != nil {
			return err
		}
	}
	return nil
}
