package term

import (
	"strings"
	"testing"

	"ion/internal/proto/wire"
)

func TestBuildContextMenuIncludesCoreItemsAndFiles(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "in.txt",
	})
	state.origin = 3

	menu := buildContextMenu(state, []wire.MenuFile{
		{ID: 0, Name: "in.txt", Dirty: true, Current: true},
		{ID: 1, Name: "", Dirty: false, Current: false},
	}, 10, 10, -1)

	if !menu.visible {
		t.Fatalf("menu.visible = false, want true")
	}
	if got, want := menu.items[0].kind, menuWrite; got != want {
		t.Fatalf("first item kind = %v, want %v", got, want)
	}
	if !strings.Contains(menu.title, "in.txt") {
		t.Fatalf("menu.title = %q, want file name", menu.title)
	}
	if got, want := menu.items[len(menu.items)-1].label, "    (unnamed)"; !strings.Contains(got, want) {
		t.Fatalf("last item label = %q, want unnamed marker", got)
	}
	if got, want := menu.items[len(menu.items)-2].label, " '. in.txt"; got != want {
		t.Fatalf("current file label = %q, want %q", got, want)
	}
}

func TestPlumbTokenTrimsFileLineGarbage(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "src/main.go:29:21:use more\n",
		DotStart: 0,
		DotEnd:   0,
	})
	state.cursor = strings.Index("src/main.go:29:21:use more\n", ":29")

	if got, want := plumbToken(state), "src/main.go:29:21"; got != want {
		t.Fatalf("plumbToken() = %q, want %q", got, want)
	}
}
