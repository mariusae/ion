package term

import (
	"bufio"
	"io"
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
	key, mouse, err := readBufferEscape(reader, nil)
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
	key, mouse, err := readBufferEscape(reader, nil)
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
	key, mouse, err = readBufferEscape(reader, nil)
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

func TestReadBufferEscapeApplicationCursorArrows(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1bOA\x1bOB\x1bOC\x1bOD"))
	tests := []struct {
		name string
		want int
	}{
		{name: "up", want: keyUp},
		{name: "down", want: keyDown},
		{name: "right", want: keyRight},
		{name: "left", want: keyLeft},
	}

	for _, tt := range tests {
		if _, _, err := reader.ReadRune(); err != nil {
			t.Fatalf("%s prime reader with ESC: %v", tt.name, err)
		}
		key, mouse, err := readBufferEscape(reader, nil)
		if err != nil {
			t.Fatalf("%s readBufferEscape() error = %v", tt.name, err)
		}
		if key != tt.want {
			t.Fatalf("%s key = %d, want %d", tt.name, key, tt.want)
		}
		if mouse != nil {
			t.Fatalf("%s mouse = %#v, want nil", tt.name, mouse)
		}
	}
}

func TestReadBufferEscapeCSIArrowWithModifier(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[1;2B"))
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape() error = %v", err)
	}
	if key != keyDown {
		t.Fatalf("key = %d, want keyDown", key)
	}
	if mouse != nil {
		t.Fatalf("mouse = %#v, want nil", mouse)
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

func TestHandleMouseEventNoButtonMotionEndsSelection(t *testing.T) {
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
	if !selecting {
		t.Fatalf("selecting = false, want true after press")
	}
	if ok := handleMouseEvent(state, overlay, mouseEvent{button: 35, x: 2, y: 0, pressed: true}, &selecting, &selectStart); !ok {
		t.Fatalf("no-button motion not handled")
	}
	if selecting {
		t.Fatalf("selecting = true, want false after no-button motion")
	}
	if got, want := state.dotStart, 0; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := state.dotEnd, 2; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
}

func TestMouseEventDismissesOverlayOutside(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event mouseEvent
		want  bool
	}{
		{name: "left press", event: mouseEvent{button: 0, pressed: true}, want: true},
		{name: "right press", event: mouseEvent{button: 2, pressed: true}, want: true},
		{name: "scroll up", event: mouseEvent{button: 64}, want: true},
		{name: "scroll down", event: mouseEvent{button: 65}, want: true},
		{name: "horizontal wheel", event: mouseEvent{button: 66}, want: false},
		{name: "left release", event: mouseEvent{button: 0, pressed: false}, want: false},
		{name: "motion with button down", event: mouseEvent{button: 32, pressed: true}, want: false},
		{name: "motion with no buttons", event: mouseEvent{button: 35, pressed: true}, want: false},
	}

	for _, tt := range tests {
		if got := tt.event.dismissesOverlayOutside(); got != tt.want {
			t.Fatalf("%s dismissesOverlayOutside() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHandleOverlayMouseEventIgnoresPassiveMotionWithoutSelection(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addOutput("alpha")

	handled, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  35,
		x:       2,
		y:       overlayTopRow(overlay) + 1,
		pressed: true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent() error = %v", err)
	}
	if handled {
		t.Fatal("handleOverlayMouseEvent() handled = true, want false for passive motion with no selection")
	}
	if overlay.selecting {
		t.Fatal("overlay.selecting = true, want false")
	}
}

func TestHandleOverlayMouseEventIgnoresUnknownWheelButton(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addOutput("alpha")

	handled, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  66,
		x:       2,
		y:       overlayTopRow(overlay) + 1,
		pressed: true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent() error = %v", err)
	}
	if handled {
		t.Fatal("handleOverlayMouseEvent() handled = true, want false for unknown wheel event")
	}
}

func TestHandleOverlayMouseEventIgnoresNoOpScrollAtBoundary(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addOutput("alpha")

	handled, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  65,
		x:       2,
		y:       overlayTopRow(overlay) + 1,
		pressed: true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent() error = %v", err)
	}
	if handled {
		t.Fatal("handleOverlayMouseEvent() handled = true, want false for no-op scroll")
	}
}

func TestHandleOverlayMouseEventCoalescedWheelScrollsMultipleSteps(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.visible = true
	for i := 0; i < 10; i++ {
		overlay.addOutput("alpha")
	}

	handled, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  64,
		x:       2,
		y:       overlayTopRow(overlay) + 1,
		pressed: true,
		repeat:  2,
	}, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent() error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent() handled = false, want true for coalesced scroll")
	}
	if got, want := overlay.scroll, 6; got != want {
		t.Fatalf("overlay.scroll = %d, want %d", got, want)
	}
}

func TestReadBufferEscapeMouseWithFragmentedSequence(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer r.Close()
	defer w.Close()

	reader := bufio.NewReader(r)
	go func() {
		_, _ = w.Write([]byte("["))
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("<0;3;1M"))
		_ = w.Close()
	}()

	key, mouse, err := readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(fragmented) error = %v", err)
	}
	if key != keyMouse {
		t.Fatalf("readBufferEscape(fragmented) key = %d, want keyMouse", key)
	}
	if mouse == nil {
		t.Fatalf("fragmented mouse event = nil, want value")
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

func TestReadBufferEscapeCoalescesBufferedMouseMotion(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[<35;2;2M\x1b[<35;7;4M\x1b[<0;3;3M"))
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with first ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(coalesced motion) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(coalesced motion) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 35; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.x, 6; got != want {
		t.Fatalf("mouse.x = %d, want %d", got, want)
	}
	if got, want := mouse.y, 3; got != want {
		t.Fatalf("mouse.y = %d, want %d", got, want)
	}
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with next ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(next event) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(next event) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 0; got != want {
		t.Fatalf("next mouse.button = %d, want %d", got, want)
	}
}

func TestReadBufferEscapeCoalescesBufferedMouseWheel(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[<64;3;3M\x1b[<64;3;3M\x1b[<0;3;3M"))
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with first ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(coalesced wheel) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(coalesced wheel) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 64; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.repeat, 2; got != want {
		t.Fatalf("mouse.repeat = %d, want %d", got, want)
	}
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with next ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(next event after wheel coalescing) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(next event after wheel coalescing) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 0; got != want {
		t.Fatalf("next mouse.button = %d, want %d", got, want)
	}
}

func TestReadBufferEscapeCoalescesTimedPassiveMouseMotion(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer r.Close()
	defer w.Close()

	reader := bufio.NewReader(r)
	go func() {
		_, _ = w.Write([]byte("\x1b[<35;2;2M"))
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("\x1b[<35;7;4M"))
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("\x1b[<0;3;3M"))
		_ = w.Close()
	}()

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with first ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(timed coalesced motion) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(timed coalesced motion) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 35; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.x, 6; got != want {
		t.Fatalf("mouse.x = %d, want %d", got, want)
	}
	if got, want := mouse.y, 3; got != want {
		t.Fatalf("mouse.y = %d, want %d", got, want)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with next ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(next event after timed coalescing) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(next event after timed coalescing) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 0; got != want {
		t.Fatalf("next mouse.button = %d, want %d", got, want)
	}
}

func TestReadBufferEscapeCoalescesTimedMouseWheel(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer r.Close()
	defer w.Close()

	reader := bufio.NewReader(r)
	go func() {
		_, _ = w.Write([]byte("\x1b[<64;3;3M"))
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("\x1b[<64;3;3M"))
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("\x1b[<0;3;3M"))
		_ = w.Close()
	}()

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with first ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(timed coalesced wheel) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(timed coalesced wheel) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 64; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.repeat, 2; got != want {
		t.Fatalf("mouse.repeat = %d, want %d", got, want)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with next ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(next event after timed wheel coalescing) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(next event after timed wheel coalescing) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 0; got != want {
		t.Fatalf("next mouse.button = %d, want %d", got, want)
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
