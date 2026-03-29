package term

import (
	"bytes"
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
	}, nil, wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

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

func TestBuildContextMenuIncludesCustomCommands(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "in.txt",
	})

	menu := buildContextMenu(state, nil, []wire.MenuCommand{
		{Command: ":lsp:goto", Label: "symbol"},
		{Command: ":lsp:show", Label: ""},
	}, wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

	var labels []string
	for _, item := range menu.items {
		if item.kind != menuCommand {
			continue
		}
		labels = append(labels, item.label)
	}
	if got, want := len(labels), 2; got != want {
		t.Fatalf("custom menu item count = %d, want %d", got, want)
	}
	if got, want := labels[0], " symbol"; got != want {
		t.Fatalf("first custom label = %q, want %q", got, want)
	}
	if got, want := labels[1], " :lsp:show"; got != want {
		t.Fatalf("second custom label = %q, want %q", got, want)
	}
}

func TestFormatMenuBorderUsesContiguousUnicodeBorders(t *testing.T) {
	t.Parallel()

	if got, want := formatMenuBorder(" title ", 12, '╭', '╮', '─'), "╭── title ───╮"; got != want {
		t.Fatalf("formatMenuBorder() = %q, want %q", got, want)
	}
	if got, want := formatMenuBorder("", 6, '├', '┤', '─'), "├──────┤"; got != want {
		t.Fatalf("separator border = %q, want %q", got, want)
	}
}

func TestDrawMenuUsesUniformTopRowTint(t *testing.T) {
	t.Parallel()

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	menu := &menuState{
		visible: true,
		x:       0,
		y:       0,
		width:   10,
		height:  3,
		title:   " title ",
		items:   []menuItem{{label: " item", kind: menuCut}},
	}

	var out bytes.Buffer
	if err := drawMenu(&out, menu, theme); err != nil {
		t.Fatalf("drawMenu() error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, theme.titlePrefix()) {
		t.Fatalf("drawMenu() = %q, want no distinct title-row tint", got)
	}
	if !strings.Contains(got, theme.subtlePrefix()+"╭") {
		t.Fatalf("drawMenu() = %q, want top border rendered with subtle tint", got)
	}
}

func TestWriteMenuItemBoldsCurrentFile(t *testing.T) {
	t.Parallel()

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	item := menuItem{label: " '. in.txt", kind: menuFile, current: true}

	var out bytes.Buffer
	if err := writeMenuItem(&out, 0, 0, 16, item, false, theme); err != nil {
		t.Fatalf("writeMenuItem() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, sgr("1", theme.bgCode(theme.subtleBG))+"│ '. in.txt") {
		t.Fatalf("writeMenuItem() = %q, want current file row bold on subtle background", got)
	}
}

func TestWriteMenuItemHoverDoesNotBoldNonCurrentRow(t *testing.T) {
	t.Parallel()

	theme := buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor)
	item := menuItem{label: " cut", kind: menuCut}

	var out bytes.Buffer
	if err := writeMenuItem(&out, 0, 0, 16, item, true, theme); err != nil {
		t.Fatalf("writeMenuItem() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, theme.prefixFor(theme.cursorBG)+"│ cut") {
		t.Fatalf("writeMenuItem() = %q, want hover background without title tint", got)
	}
	if strings.Contains(got, sgr("1", theme.bgCode(theme.cursorBG))+"│ cut") {
		t.Fatalf("writeMenuItem() = %q, want non-current hover row unbolded", got)
	}
}

func TestDirtyMarkUsesDoubleQuoteForDirtyChangedFile(t *testing.T) {
	t.Parallel()

	if got, want := dirtyMark(true, true), '"'; got != want {
		t.Fatalf("dirtyMark(dirty+changed) = %q, want %q", got, want)
	}
	if got, want := dirtyMark(true, false), '\''; got != want {
		t.Fatalf("dirtyMark(dirty) = %q, want %q", got, want)
	}
	if got, want := dirtyMark(false, false), ' '; got != want {
		t.Fatalf("dirtyMark(clean) = %q, want %q", got, want)
	}
}

func TestBuildContextMenuStickyFileHoverPrefersPreviousFile(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "b.txt",
	})

	files := []wire.MenuFile{
		{ID: 1, Name: "a.txt"},
		{ID: 2, Name: "b.txt", Current: true},
		{ID: 3, Name: "c.txt"},
	}
	sticky := menuStickyState{
		itemIndex:          3,
		preferPreviousFile: true,
		previousFileID:     1,
	}

	menu := buildContextMenu(state, files, nil, wire.NavigationStack{}, 10, 10, sticky)
	if got, want := menu.hover, menuItemIndexByFileID(menu.items, 1); got != want {
		t.Fatalf("menu.hover = %d, want previous file index %d", got, want)
	}
}

func TestBuildContextMenuIncludesPreviousAndNextNavigationItems(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "b.txt",
	})

	nav := wire.NavigationStack{
		Entries: []wire.NavigationEntry{
			{Label: "a.txt:#0"},
			{Label: "b.txt:#6,#10"},
			{Label: "c.txt:#0"},
		},
		Current: 1,
	}

	menu := buildContextMenu(state, []wire.MenuFile{{ID: 2, Name: "b.txt", Current: true}}, nil, nav, 10, 10, menuStickyState{itemIndex: -1})
	foundPrev := false
	foundNext := false
	for _, item := range menu.items {
		if item.kind == menuHistoryPrev {
			foundPrev = true
			if got, want := item.label, " -  a.txt:#0"; got != want {
				t.Fatalf("prev item label = %q, want %q", got, want)
			}
		}
		if item.kind == menuHistoryNext {
			foundNext = true
			if got, want := item.label, " -  c.txt:#0"; got != want {
				t.Fatalf("next item label = %q, want %q", got, want)
			}
		}
	}
	if !foundPrev || !foundNext {
		t.Fatalf("foundPrev=%v foundNext=%v, want both true", foundPrev, foundNext)
	}
}

func TestNextMenuStickyStateForFileNavigationTargetsPreviousFile(t *testing.T) {
	t.Parallel()

	menu := &menuState{
		items: []menuItem{
			{kind: menuCut},
			{kind: menuFile, fileID: 1},
			{kind: menuFile, fileID: 2, current: true},
			{kind: menuFile, fileID: 3},
		},
	}

	sticky := nextMenuStickyState(menu, 3, menu.items[3])
	if !sticky.preferPreviousFile {
		t.Fatalf("preferPreviousFile = false, want true")
	}
	if got, want := sticky.previousFileID, 2; got != want {
		t.Fatalf("previousFileID = %d, want %d", got, want)
	}
	if got, want := sticky.itemIndex, 3; got != want {
		t.Fatalf("itemIndex = %d, want %d", got, want)
	}
}

func TestBuildContextMenuStickyHistoryHoverPrefersPreviousCommandKind(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "b.txt",
	})

	nav := wire.NavigationStack{
		Entries: []wire.NavigationEntry{
			{Label: "a.txt:#0"},
			{Label: "b.txt:#6,#10"},
		},
		Current: 1,
	}
	sticky := menuStickyState{
		itemIndex:     7,
		preferHistory: true,
		historyKind:   menuHistoryPrev,
	}

	menu := buildContextMenu(state, nil, nil, nav, 10, 10, sticky)
	if got, want := menu.hover, menuItemIndexByKind(menu.items, menuHistoryPrev); got != want {
		t.Fatalf("menu.hover = %d, want previous history index %d", got, want)
	}
}

func TestNextMenuStickyStateForHistoryNavigationTargetsCommandKind(t *testing.T) {
	t.Parallel()

	menu := &menuState{
		items: []menuItem{
			{kind: menuCut},
			{kind: menuHistoryPrev},
			{kind: menuFile, fileID: 2, current: true},
		},
	}

	sticky := nextMenuStickyState(menu, 1, menu.items[1])
	if !sticky.preferHistory {
		t.Fatalf("preferHistory = false, want true")
	}
	if got, want := sticky.historyKind, menuHistoryPrev; got != want {
		t.Fatalf("historyKind = %v, want %v", got, want)
	}
	if got, want := sticky.itemIndex, 1; got != want {
		t.Fatalf("itemIndex = %d, want %d", got, want)
	}
}

func TestStickyHistoryFallsBackToOtherDirection(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "b.txt",
	})

	// At end of stack: prev exists, next does not.
	nav := wire.NavigationStack{
		Entries: []wire.NavigationEntry{
			{Label: "a.txt:#0"},
			{Label: "b.txt:#6,#10"},
		},
		Current: 1,
	}
	sticky := menuStickyState{
		itemIndex:     8,
		preferHistory: true,
		historyKind:   menuHistoryNext,
	}

	menu := buildContextMenu(state, []wire.MenuFile{{ID: 2, Name: "b.txt", Current: true}}, nil, nav, 10, 10, sticky)
	want := menuItemIndexByKind(menu.items, menuHistoryPrev)
	if want < 0 {
		t.Fatal("expected prev history item in menu")
	}
	if got := menu.hover; got != want {
		t.Fatalf("menu.hover = %d, want prev history index %d", got, want)
	}
}

func TestStickyHistoryEmptyStackSelectsNothing(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "b.txt",
	})

	// Empty stack: neither prev nor next exists.
	sticky := menuStickyState{
		itemIndex:     8,
		preferHistory: true,
		historyKind:   menuHistoryNext,
	}

	menu := buildContextMenu(state, []wire.MenuFile{{ID: 2, Name: "b.txt", Current: true}}, nil, wire.NavigationStack{}, 10, 10, sticky)
	if got := menu.hover; got != -1 {
		t.Fatalf("menu.hover = %d, want -1 (no selection)", got)
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

func TestPlumbTokenKeepsGenericAddressSuffix(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "foo.py:#56,#81\n",
		DotStart: 0,
		DotEnd:   0,
	})
	state.cursor = strings.Index("foo.py:#56,#81\n", "#56")

	if got, want := plumbToken(state), "foo.py:#56,#81"; got != want {
		t.Fatalf("plumbToken() = %q, want %q", got, want)
	}
}
