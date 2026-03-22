package term

import (
	"bufio"
	"strings"
	"testing"

	"ion/internal/proto/wire"
)

func TestReadBufferEscapeMouse(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[<0;3;1M"))
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader)
	if err != nil {
		t.Fatalf("readBufferEscape() error = %v", err)
	}
	if key != keyMouse {
		t.Fatalf("readBufferEscape() key = %d, want keyMouse", key)
	}
	if mouse == nil {
		t.Fatalf("mouse event = nil, want value")
	}
	if got, want := mouse.x, 2; got != want {
		t.Fatalf("mouse.x = %d, want %d", got, want)
	}
	if got, want := mouse.y, 0; got != want {
		t.Fatalf("mouse.y = %d, want %d", got, want)
	}
	if !mouse.pressed {
		t.Fatalf("mouse.pressed = false, want true")
	}
}

func TestHandleMouseEventDragSelectsRange(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\nbeta\n",
		DotStart: 0,
		DotEnd:   0,
	})
	overlay := newOverlayState()
	selecting := false
	selectStart := 0

	if ok := handleMouseEvent(state, overlay, mouseEvent{button: 0, x: 0, y: 0, pressed: true}, &selecting, &selectStart); !ok {
		t.Fatalf("press not handled")
	}
	if ok := handleMouseEvent(state, overlay, mouseEvent{button: 32, x: 2, y: 0, pressed: true}, &selecting, &selectStart); !ok {
		t.Fatalf("drag not handled")
	}
	if ok := handleMouseEvent(state, overlay, mouseEvent{button: 0, x: 2, y: 0, pressed: false}, &selecting, &selectStart); !ok {
		t.Fatalf("release not handled")
	}
	if got, want := state.dotStart, 0; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := state.dotEnd, 2; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
}
