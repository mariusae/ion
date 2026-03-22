package term

import (
	"testing"

	"ion/internal/proto/wire"
)

func TestNewBufferStateStartsAtDot(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\nbeta\ngamma\n",
		DotStart: 6,
		DotEnd:   10,
	})

	if got, want := state.cursor, 6; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
	if got, want := state.origin, 6; got != want {
		t.Fatalf("origin = %d, want %d", got, want)
	}
}

func TestMovePageDownByLines(t *testing.T) {
	t.Parallel()

	text := []rune("l1\nl2\nl3\nl4\nl5\n")
	if got, want := movePageDown(text, 0, 2), 6; got != want {
		t.Fatalf("movePageDown() = %d, want %d", got, want)
	}
}

func TestMoveLineDownPreservesColumn(t *testing.T) {
	t.Parallel()

	text := []rune("alpha\nxy\nomega\n")
	if got, want := moveLineDown(text, 3), 8; got != want {
		t.Fatalf("moveLineDown() = %d, want %d", got, want)
	}
}

func TestHandleBufferKeyCtrlAAndCtrlE(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\nbeta\n",
		DotStart: 8,
		DotEnd:   8,
	})
	handleBufferKey(state, 1)
	if got, want := state.cursor, 6; got != want {
		t.Fatalf("Ctrl-A cursor = %d, want %d", got, want)
	}
	handleBufferKey(state, 5)
	if got, want := state.cursor, 10; got != want {
		t.Fatalf("Ctrl-E cursor = %d, want %d", got, want)
	}
}
