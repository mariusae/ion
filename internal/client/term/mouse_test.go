package term

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

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

func TestReadBufferEscapeFocusEvents(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[I\x1b[O"))

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with first ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader)
	if err != nil {
		t.Fatalf("readBufferEscape(focus-in) error = %v", err)
	}
	if key != keyFocusIn {
		t.Fatalf("focus-in key = %d, want keyFocusIn", key)
	}
	if mouse != nil {
		t.Fatalf("focus-in mouse = %#v, want nil", mouse)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with second ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader)
	if err != nil {
		t.Fatalf("readBufferEscape(focus-out) error = %v", err)
	}
	if key != keyFocusOut {
		t.Fatalf("focus-out key = %d, want keyFocusOut", key)
	}
	if mouse != nil {
		t.Fatalf("focus-out mouse = %#v, want nil", mouse)
	}
}

func TestReadBufferEscapeTTYWaitsForFragmentedArrowSequence(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer r.Close()
	defer w.Close()

	reader := bufio.NewReader(r)
	go func() {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte{'['})
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte{'A'})
	}()

	key, mouse, err := readBufferEscapeTTY(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscapeTTY() error = %v", err)
	}
	if key != keyUp {
		t.Fatalf("readBufferEscapeTTY() key = %d, want keyUp", key)
	}
	if mouse != nil {
		t.Fatalf("readBufferEscapeTTY() mouse = %#v, want nil", mouse)
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

func TestScreenToPosUsesWrappedRows(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 3
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "abcdef\n",
		DotStart: 0,
		DotEnd:   0,
	})

	pos, ok := screenToPos(state, nil, 1, 1)
	if !ok {
		t.Fatalf("screenToPos() ok = false, want true")
	}
	if got, want := pos, 4; got != want {
		t.Fatalf("screenToPos() = %d, want %d", got, want)
	}
}

func TestScreenToPosUsesExpandedTabColumns(t *testing.T) {
	prevRows, prevCols := termRows, termCols
	termRows, termCols = 6, 16
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text:     "\talpha\n",
		DotStart: 0,
		DotEnd:   0,
	})

	pos, ok := screenToPos(state, nil, 0, 0)
	if !ok {
		t.Fatalf("screenToPos() ok = false, want true")
	}
	if got, want := pos, 0; got != want {
		t.Fatalf("screenToPos(start of tab) = %d, want %d", got, want)
	}

	pos, ok = screenToPos(state, nil, 0, 3)
	if !ok {
		t.Fatalf("screenToPos() ok = false, want true")
	}
	if got, want := pos, 1; got != want {
		t.Fatalf("screenToPos(inside tab) = %d, want %d", got, want)
	}
}
