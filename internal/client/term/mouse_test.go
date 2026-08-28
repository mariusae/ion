package term

import (
	"bufio"
	"fmt"
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

func TestReadBufferEscapeMetaShortcuts(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1ba\x1b2\x1b'\x1b!"))

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC for alt-a: %v", err)
	}
	key, mouse, err := readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(alt-a) error = %v", err)
	}
	if got, want := key, metaKey('a'); got != want {
		t.Fatalf("alt-a key = %d, want %d", got, want)
	}
	if mouse != nil {
		t.Fatalf("alt-a mouse = %#v, want nil", mouse)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC for alt-2: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(alt-2) error = %v", err)
	}
	if got, want := key, metaKey('2'); got != want {
		t.Fatalf("alt-2 key = %d, want %d", got, want)
	}
	if mouse != nil {
		t.Fatalf("alt-2 mouse = %#v, want nil", mouse)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC for alt-quote: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(alt-quote) error = %v", err)
	}
	if got, want := key, metaKey('\''); got != want {
		t.Fatalf("alt-quote key = %d, want %d", got, want)
	}
	if mouse != nil {
		t.Fatalf("alt-quote mouse = %#v, want nil", mouse)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC for alt-bang: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(alt-bang) error = %v", err)
	}
	if got, want := key, metaKey('!'); got != want {
		t.Fatalf("alt-bang key = %d, want %d", got, want)
	}
	if mouse != nil {
		t.Fatalf("alt-bang mouse = %#v, want nil", mouse)
	}
}

func TestLegacyAltKeyTranslatesEditorMetaBindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  int
		want int
	}{
		{name: "alt-b", key: metaKey('b'), want: keyAltLeft},
		{name: "alt-f", key: metaKey('f'), want: keyAltRight},
		{name: "alt-i passthrough", key: metaKey('i'), want: metaKey('i')},
		{name: "alt-v passthrough", key: metaKey('v'), want: metaKey('v')},
		{name: "alt-w passthrough", key: metaKey('w'), want: metaKey('w')},
		{name: "alt-backspace", key: metaKey(0x7f), want: keyAltBackspace},
		{name: "alt-a passthrough", key: metaKey('a'), want: metaKey('a')},
	}

	for _, tt := range tests {
		if got := legacyAltKey(tt.key); got != tt.want {
			t.Fatalf("%s legacyAltKey() = %d, want %d", tt.name, got, tt.want)
		}
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

func TestHandleMouseEventScrollbarClicksPageViewport(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 3, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	state := newBufferState(wire.BufferView{
		Text: strings.Join([]string{
			"one",
			"two",
			"three",
			"four",
			"five",
			"six",
		}, "\n"),
		DotStart: 0,
		DotEnd:   0,
	})
	selecting := false
	selectStart := 0

	if ok := handleMouseEvent(state, nil, mouseEvent{button: 2, x: scrollbarColumn(), y: 1, pressed: true}, &selecting, &selectStart); !ok {
		t.Fatal("right scrollbar click handled = false, want true")
	}
	if got, want := string(state.text[state.origin:lineEnd(state.text, state.origin)]), "four"; got != want {
		t.Fatalf("origin line after right click = %q, want %q", got, want)
	}
	if got, want := state.cursor, 0; got != want {
		t.Fatalf("cursor after right click = %d, want %d", got, want)
	}

	if ok := handleMouseEvent(state, nil, mouseEvent{button: 0, x: scrollbarColumn(), y: 1, pressed: true}, &selecting, &selectStart); !ok {
		t.Fatal("left scrollbar click handled = false, want true")
	}
	if got, want := state.origin, 0; got != want {
		t.Fatalf("origin after left click = %d, want %d", got, want)
	}
}

func TestHandleMouseEventScrollbarClickDistanceScalesScrollRows(t *testing.T) {
	t.Parallel()

	prevRows, prevCols := termRows, termCols
	termRows, termCols = 20, 12
	t.Cleanup(func() {
		termRows, termCols = prevRows, prevCols
	})

	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i+1)
	}
	state := newBufferState(wire.BufferView{
		Text:     strings.Join(lines, "\n"),
		DotStart: 0,
		DotEnd:   0,
	})
	selecting := false
	selectStart := 0

	if got, want := scrollbarClickRows(state, nil, 0), 0; got != want {
		t.Fatalf("scrollbarClickRows(top) = %d, want %d", got, want)
	}
	if got, want := scrollbarClickRows(state, nil, 2), 4; got != want {
		t.Fatalf("scrollbarClickRows(0.2x thumb) = %d, want %d", got, want)
	}
	if got, want := scrollbarClickRows(state, nil, 10), 20; got != want {
		t.Fatalf("scrollbarClickRows(1.0x thumb) = %d, want %d", got, want)
	}
	if got, want := scrollbarClickRows(state, nil, 14), 28; got != want {
		t.Fatalf("scrollbarClickRows(1.4x thumb) = %d, want %d", got, want)
	}
	if ok := handleMouseEvent(state, nil, mouseEvent{button: 2, x: scrollbarColumn(), y: 14, pressed: true}, &selecting, &selectStart); !ok {
		t.Fatal("right scrollbar click handled = false, want true")
	}
	if got, want := string(state.text[state.origin:lineEnd(state.text, state.origin)]), "line29"; got != want {
		t.Fatalf("origin line after scaled right click = %q, want %q", got, want)
	}
}

func TestScreenToPosRejectsScrollbarColumn(t *testing.T) {
	t.Parallel()

	prevCols := termCols
	termCols = 12
	t.Cleanup(func() {
		termCols = prevCols
	})

	state := newBufferState(wire.BufferView{Text: "alpha\n"})
	if _, ok := screenToPos(state, nil, 0, scrollbarColumn()); ok {
		t.Fatal("screenToPos(scrollbar column) ok = true, want false")
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

func TestShouldRevealHUDFromBottomWheel(t *testing.T) {
	prevRows := termRows
	termRows = 10
	t.Cleanup(func() {
		termRows = prevRows
	})

	hidden := newOverlayState()
	visible := newOverlayState()
	visible.visible = true
	tests := []struct {
		name    string
		overlay *overlayState
		event   mouseEvent
		want    bool
	}{
		{name: "bottom row", overlay: hidden, event: mouseEvent{button: 64, y: 9}, want: true},
		{name: "second bottom row", overlay: hidden, event: mouseEvent{button: 64, y: 8}, want: true},
		{name: "above reveal zone", overlay: hidden, event: mouseEvent{button: 64, y: 7}},
		{name: "wheel down", overlay: hidden, event: mouseEvent{button: 65, y: 9}},
		{name: "horizontal wheel", overlay: hidden, event: mouseEvent{button: 66, y: 9}},
		{name: "left click", overlay: hidden, event: mouseEvent{button: 0, y: 9, pressed: true}},
		{name: "already visible", overlay: visible, event: mouseEvent{button: 64, y: 9}},
		{name: "outside terminal", overlay: hidden, event: mouseEvent{button: 64, y: 10}},
	}

	for _, tt := range tests {
		if got := shouldRevealHUDFromBottomWheel(tt.overlay, tt.event); got != tt.want {
			t.Errorf("%s: shouldRevealHUDFromBottomWheel() = %v, want %v", tt.name, got, tt.want)
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
	}, nil, nil, nil)
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
	}, nil, nil, nil)
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
	}, nil, nil, nil)
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
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent() error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent() handled = false, want true for coalesced scroll")
	}
	if got, want := overlay.scroll, 2; got != want {
		t.Fatalf("overlay.scroll = %d, want %d", got, want)
	}
}

func TestHandleOverlayMouseEventDragTopBorderExpandsMaxHeight(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 12
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.visible = true
	for i := 0; i < 10; i++ {
		overlay.addOutput("alpha")
	}

	if got, want := overlayHeight(overlay), 4; got != want {
		t.Fatalf("overlayHeight(initial) = %d, want %d", got, want)
	}

	handled, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  0,
		y:       overlayTopRow(overlay),
		pressed: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent(press) error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent(press) handled = false, want true")
	}

	handled, err = handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  32,
		y:       4,
		pressed: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent(drag) error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent(drag) handled = false, want true")
	}
	if got, want := overlay.maxHeightRows, 8; got != want {
		t.Fatalf("overlay.maxHeightRows = %d, want %d", got, want)
	}
	if got, want := overlayHeight(overlay), 8; got != want {
		t.Fatalf("overlayHeight(expanded) = %d, want %d", got, want)
	}

	handled, err = handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  0,
		y:       4,
		pressed: false,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent(release) error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent(release) handled = false, want true")
	}
	if overlay.resizing {
		t.Fatal("overlay.resizing = true, want false after release")
	}
}

func TestHandleOverlayMouseEventDragTopBorderShrinksMaxHeight(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 18
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.visible = true
	for i := 0; i < 20; i++ {
		overlay.addOutput("alpha")
	}

	handled, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  0,
		y:       overlayTopRow(overlay),
		pressed: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent(press) error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent(press) handled = false, want true")
	}

	handled, err = handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  32,
		y:       14,
		pressed: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent(drag) error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent(drag) handled = false, want true")
	}
	if got, want := overlay.maxHeightRows, 4; got != want {
		t.Fatalf("overlay.maxHeightRows = %d, want %d", got, want)
	}
	if got, want := overlayHeight(overlay), 4; got != want {
		t.Fatalf("overlayHeight(shrunk) = %d, want %d", got, want)
	}
}

func TestHandleOverlayMouseEventMiddleClickPastesIntoPrompt(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.open("")
	called := false

	handled, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  1,
		x:       0,
		y:       termRows - overlayBottomPadRows(overlay) - 1,
		pressed: true,
	}, nil, nil, func() error {
		called = true
		overlay.insert([]rune("paste"))
		return nil
	})
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent() error = %v", err)
	}
	if !handled {
		t.Fatal("handleOverlayMouseEvent() handled = false, want true")
	}
	if !called {
		t.Fatal("paste callback not invoked")
	}
	if got, want := string(overlay.input), "paste"; got != want {
		t.Fatalf("overlay input = %q, want %q", got, want)
	}
}

func TestHandleOverlayMouseEventWheelDownInPromptDoesNotPaste(t *testing.T) {
	t.Parallel()

	prevRows := termRows
	termRows = 8
	t.Cleanup(func() {
		termRows = prevRows
	})

	overlay := newOverlayState()
	overlay.open("")
	called := false

	_, err := handleOverlayMouseEvent(io.Discard, overlay, mouseEvent{
		button:  65,
		x:       0,
		y:       termRows - overlayBottomPadRows(overlay) - 1,
		pressed: true,
	}, nil, nil, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("handleOverlayMouseEvent() error = %v", err)
	}
	if called {
		t.Fatal("paste callback invoked for wheel-down event")
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

func TestReadBufferEscapeMouseWaitsForDelayedPrefixAfterESC(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer r.Close()
	defer w.Close()

	reader := bufio.NewReader(r)
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("[<35;12;23M"))
		_ = w.Close()
	}()

	key, mouse, err := readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(delayed prefix) error = %v", err)
	}
	if key != keyMouse {
		t.Fatalf("readBufferEscape(delayed prefix) key = %d, want keyMouse", key)
	}
	if mouse == nil {
		t.Fatalf("delayed prefix mouse event = nil, want value")
	}
	if got, want := mouse.button, 35; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.x, 11; got != want {
		t.Fatalf("mouse.x = %d, want %d", got, want)
	}
	if got, want := mouse.y, 22; got != want {
		t.Fatalf("mouse.y = %d, want %d", got, want)
	}
}

func TestReadBufferEscapeMouseWaitsForDelayedMouseMarkerAfterCSI(t *testing.T) {
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
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("<35;12;23M"))
		_ = w.Close()
	}()

	key, mouse, err := readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(delayed marker) error = %v", err)
	}
	if key != keyMouse {
		t.Fatalf("readBufferEscape(delayed marker) key = %d, want keyMouse", key)
	}
	if mouse == nil {
		t.Fatalf("delayed marker mouse event = nil, want value")
	}
	if got, want := mouse.button, 35; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.x, 11; got != want {
		t.Fatalf("mouse.x = %d, want %d", got, want)
	}
	if got, want := mouse.y, 22; got != want {
		t.Fatalf("mouse.y = %d, want %d", got, want)
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

func TestReadBufferEscapeDoesNotCoalesceBufferedMouseWheel(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[<64;3;3M\x1b[<64;3;3M\x1b[<0;3;3M"))
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with first ESC: %v", err)
	}
	key, mouse, err := readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(buffered wheel) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(buffered wheel) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 64; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.count(), 1; got != want {
		t.Fatalf("mouse.count() = %d, want %d", got, want)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with second ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(second buffered wheel) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(second buffered wheel) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 64; got != want {
		t.Fatalf("second mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.count(), 1; got != want {
		t.Fatalf("second mouse.count() = %d, want %d", got, want)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with next ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, nil)
	if err != nil {
		t.Fatalf("readBufferEscape(next event after buffered wheel) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(next event after buffered wheel) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 0; got != want {
		t.Fatalf("next mouse.button = %d, want %d", got, want)
	}
}

func TestPeekMouseEventWaitsForFragmentedTimedSequence(t *testing.T) {
	t.Parallel()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer r.Close()
	defer w.Close()

	reader := bufio.NewReader(r)
	go func() {
		_, _ = w.Write([]byte("\x1b[<64;3"))
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte(";3M"))
		_ = w.Close()
	}()

	event, size, ok, err := peekMouseEvent(reader, r, 20_000)
	if err != nil {
		t.Fatalf("peekMouseEvent() error = %v", err)
	}
	if !ok {
		t.Fatal("peekMouseEvent() ok = false, want true")
	}
	if got, want := size, len("\x1b[<64;3;3M"); got != want {
		t.Fatalf("peekMouseEvent() size = %d, want %d", got, want)
	}
	if got, want := event.button, 64; got != want {
		t.Fatalf("peekMouseEvent() button = %d, want %d", got, want)
	}
	if got, want := event.x, 2; got != want {
		t.Fatalf("peekMouseEvent() x = %d, want %d", got, want)
	}
	if got, want := event.y, 2; got != want {
		t.Fatalf("peekMouseEvent() y = %d, want %d", got, want)
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

func TestReadBufferEscapeDoesNotWaitToCoalesceTimedMouseWheel(t *testing.T) {
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
		t.Fatalf("readBufferEscape(timed wheel) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(timed wheel) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 64; got != want {
		t.Fatalf("mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.count(), 1; got != want {
		t.Fatalf("mouse.count() = %d, want %d", got, want)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with second ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(second timed wheel event) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(second timed wheel event) = (%d, %#v), want mouse event", key, mouse)
	}
	if got, want := mouse.button, 64; got != want {
		t.Fatalf("second mouse.button = %d, want %d", got, want)
	}
	if got, want := mouse.count(), 1; got != want {
		t.Fatalf("second mouse.count() = %d, want %d", got, want)
	}

	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with next ESC: %v", err)
	}
	key, mouse, err = readBufferEscape(reader, r)
	if err != nil {
		t.Fatalf("readBufferEscape(next event after timed wheel) error = %v", err)
	}
	if key != keyMouse || mouse == nil {
		t.Fatalf("readBufferEscape(next event after timed wheel) = (%d, %#v), want mouse event", key, mouse)
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
	if got, want := pos, 3; got != want {
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
