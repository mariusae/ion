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

func TestLastPageOrigin(t *testing.T) {
	t.Parallel()

	text := []rune("l1\nl2\nl3\nl4\nl5\n")
	if got, want := lastPageOrigin(text, 2), 12; got != want {
		t.Fatalf("lastPageOrigin() = %d, want %d", got, want)
	}
}
