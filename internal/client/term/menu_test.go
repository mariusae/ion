package term

import (
	"bytes"
	"path/filepath"
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
	}, nil, "", wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

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
	}, "", wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

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

func TestBuildContextMenuIncludesLatestCommandWhenMissingFromMenu(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "in.txt",
	})

	menu := buildContextMenu(state, nil, []wire.MenuCommand{
		{Command: ":lsp:goto", Label: "symbol"},
	}, "!ls -la", wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

	var labels []string
	found := false
	for _, item := range menu.items {
		if item.kind != menuCommand {
			continue
		}
		labels = append(labels, item.label)
		if item.command == "!ls -la" {
			found = true
			if !item.sepAfter {
				t.Fatalf("latest command sepAfter = false, want true")
			}
		}
	}
	if got, want := len(labels), 2; got != want {
		t.Fatalf("custom menu item count = %d, want %d", got, want)
	}
	if got, want := labels[1], " !ls -la"; got != want {
		t.Fatalf("latest command label = %q, want %q", got, want)
	}
	if !found {
		t.Fatal("latest command item missing from menu")
	}
}

func TestBuildContextMenuDoesNotDuplicateLatestCatalogCommand(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "in.txt",
	})

	menu := buildContextMenu(state, nil, []wire.MenuCommand{
		{Command: ":lsp:goto", Label: "symbol"},
	}, ":lsp:goto", wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

	commandCount := 0
	for _, item := range menu.items {
		if item.kind == menuCommand {
			commandCount++
		}
	}
	if got, want := commandCount, 1; got != want {
		t.Fatalf("command count = %d, want %d", got, want)
	}
}

func TestBuildContextMenuDoesNotDuplicateLatestBuiltInCommand(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "in.txt",
	})

	menu := buildContextMenu(state, nil, nil, ":term:write", wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

	for _, item := range menu.items {
		if item.kind == menuCommand && item.command == ":term:write" {
			t.Fatal("unexpected transient latest-command entry for built-in write command")
		}
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

func TestFormatMenuItemLinePreservesFilePrefixWhenTrimmed(t *testing.T) {
	t.Parallel()

	item := menuItem{
		label:    " '. /very/long/path/to/internal/client/term/grid_test.go",
		shortcut: "(7)",
		kind:     menuFile,
		current:  true,
	}

	got := formatMenuItemLine(item, 24)
	if !strings.HasPrefix(got, "│ '. ") {
		t.Fatalf("formatMenuItemLine() = %q, want file prefix preserved", got)
	}
	if !strings.Contains(got, "grid_test.go") {
		t.Fatalf("formatMenuItemLine() = %q, want file tail preserved", got)
	}
	if !strings.HasSuffix(got, "(7)│") {
		t.Fatalf("formatMenuItemLine() = %q, want shortcut aligned at end", got)
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

	menu := buildContextMenu(state, files, nil, "", wire.NavigationStack{}, 10, 10, sticky)
	if got, want := menu.hover, menuItemIndexByFileID(menu.items, 1); got != want {
		t.Fatalf("menu.hover = %d, want previous file index %d", got, want)
	}
}

func TestBuildContextMenuAssignsMenuShortcuts(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "b.txt",
	})

	menu := buildContextMenu(state, []wire.MenuFile{
		{ID: 1, Name: "short.go", Path: "/tmp/project/pkg/target/short.go"},
		{ID: 2, Name: "other.go", Path: "/tmp/project/pkg/other/other.go", Current: true},
	}, []wire.MenuCommand{
		{Command: ":lsp:goto", Label: "symbol", Shortcut: "g"},
		{Command: ":lsp:show", Label: "hover"},
	}, "!ls", wire.NavigationStack{}, 10, 10, menuStickyState{itemIndex: -1})

	var gotCommandShortcuts []string
	var gotFileShortcuts []string
	for _, item := range menu.items {
		switch item.kind {
		case menuCommand:
			gotCommandShortcuts = append(gotCommandShortcuts, item.shortcut)
		case menuFile:
			gotFileShortcuts = append(gotFileShortcuts, item.shortcut)
		}
	}
	if got, want := strings.Join(gotCommandShortcuts, ","), "(M-g),,"; got != want {
		t.Fatalf("command shortcuts = %q, want %q", got, want)
	}
	if got, want := strings.Join(gotFileShortcuts, ","), "(1),(2)"; got != want {
		t.Fatalf("file shortcuts = %q, want %q", got, want)
	}
}

func TestMenuShortcutLookup(t *testing.T) {
	t.Parallel()

	menu := &menuState{
		visible: true,
		items: []menuItem{
			{label: " look", shortcut: "(l)", kind: menuLook},
			{label: " symbol", shortcut: "(M-g)", keyRune: 'g', kind: menuCommand, command: ":lsp:goto"},
			{label: " hover", shortcut: "(M-h)", keyRune: 'h', kind: menuCommand, command: ":lsp:show"},
			{label: " '. main.go", shortcut: "(1)", kind: menuFile, fileID: 1, current: true},
			{label: "    util.go", shortcut: "(2)", kind: menuFile, fileID: 2},
		},
		hover: 0,
	}

	item, idx, ok := menu.itemForShortcut('l')
	if !ok {
		t.Fatal("itemForShortcut('l') = false, want true")
	}
	if got, want := idx, 0; got != want {
		t.Fatalf("builtin shortcut index = %d, want %d", got, want)
	}
	if got, want := item.kind, menuLook; got != want {
		t.Fatalf("builtin shortcut kind = %v, want %v", got, want)
	}

	item, idx, ok = menu.itemForMetaShortcut('h')
	if !ok {
		t.Fatal("itemForMetaShortcut('h') = false, want true")
	}
	if got, want := idx, 2; got != want {
		t.Fatalf("meta command index = %d, want %d", got, want)
	}
	if got, want := item.command, ":lsp:show"; got != want {
		t.Fatalf("meta command = %q, want %q", got, want)
	}

	item, idx, ok = menu.itemForShortcut('2')
	if !ok {
		t.Fatal("itemForShortcut('2') = false, want true")
	}
	if got, want := idx, 4; got != want {
		t.Fatalf("file shortcut index = %d, want %d", got, want)
	}
	if got, want := item.fileID, 2; got != want {
		t.Fatalf("file shortcut fileID = %d, want %d", got, want)
	}
}

func TestMenuCommandByMetaShortcutUsesExplicitShortcutOnly(t *testing.T) {
	t.Parallel()

	command, ok := menuCommandByMetaShortcut([]wire.MenuCommand{
		{Command: ":lsp:goto", Label: "symbol", Shortcut: "g"},
		{Command: ":lsp:show", Label: "hover"},
	}, 'g')
	if !ok {
		t.Fatal("menuCommandByMetaShortcut('g') = false, want true")
	}
	if got, want := command.Command, ":lsp:goto"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}

	if _, ok := menuCommandByMetaShortcut([]wire.MenuCommand{
		{Command: ":lsp:goto", Label: "symbol", Shortcut: "g"},
		{Command: ":lsp:show", Label: "hover"},
	}, 'h'); ok {
		t.Fatal("menuCommandByMetaShortcut('h') = true, want false without explicit shortcut")
	}
}

func TestBuildContextMenuIncludesPopNavigationItem(t *testing.T) {
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

	menu := buildContextMenu(state, []wire.MenuFile{{ID: 2, Name: "b.txt", Current: true}}, nil, "", nav, 10, 10, menuStickyState{itemIndex: -1})
	foundPop := false
	for _, item := range menu.items {
		if item.kind == menuHistoryPop {
			foundPop = true
			if got, want := item.label, " a.txt:#0"; got != want {
				t.Fatalf("pop item label = %q, want %q", got, want)
			}
		}
	}
	if !foundPop {
		t.Fatal("expected pop navigation item in menu")
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

func TestBuildContextMenuStickyHistoryHoverPrefersPop(t *testing.T) {
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
		historyKind:   menuHistoryPop,
	}

	menu := buildContextMenu(state, nil, nil, "", nav, 10, 10, sticky)
	if got, want := menu.hover, menuItemIndexByKind(menu.items, menuHistoryPop); got != want {
		t.Fatalf("menu.hover = %d, want pop history index %d", got, want)
	}
}

func TestNextMenuStickyStateForHistoryNavigationTargetsCommandKind(t *testing.T) {
	t.Parallel()

	menu := &menuState{
		items: []menuItem{
			{kind: menuCut},
			{kind: menuHistoryPop},
			{kind: menuFile, fileID: 2, current: true},
		},
	}

	sticky := nextMenuStickyState(menu, 1, menu.items[1])
	if !sticky.preferHistory {
		t.Fatalf("preferHistory = false, want true")
	}
	if got, want := sticky.historyKind, menuHistoryPop; got != want {
		t.Fatalf("historyKind = %v, want %v", got, want)
	}
	if got, want := sticky.itemIndex, 1; got != want {
		t.Fatalf("itemIndex = %d, want %d", got, want)
	}
}

func TestStickyHistoryMissingPopSelectsNothing(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text: "alpha\nbeta\n",
		Name: "b.txt",
	})

	nav := wire.NavigationStack{
		Entries: []wire.NavigationEntry{
			{Label: "a.txt:#0"},
		},
		Current: 0,
	}
	sticky := menuStickyState{
		itemIndex:     8,
		preferHistory: true,
		historyKind:   menuHistoryPop,
	}

	menu := buildContextMenu(state, []wire.MenuFile{{ID: 2, Name: "b.txt", Current: true}}, nil, "", nav, 10, 10, sticky)
	if got := menu.hover; got != -1 {
		t.Fatalf("menu.hover = %d, want -1 when pop item is absent", got)
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
		historyKind:   menuHistoryPop,
	}

	menu := buildContextMenu(state, []wire.MenuFile{{ID: 2, Name: "b.txt", Current: true}}, nil, "", wire.NavigationStack{}, 10, 10, sticky)
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

func TestResolvePlumbTargetTokenUsesCurrentFileDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	state := newBufferState(wire.BufferView{
		Text:     "../other.go:29:21\n",
		Path:     filepath.Join(root, "subdir", "current.go"),
		DotStart: 0,
		DotEnd:   0,
	})

	if got, want := resolvePlumbTargetToken(state, "../other.go:29:21"), filepath.Join(root, "other.go")+":29+#20"; got != want {
		t.Fatalf("resolvePlumbTargetToken() = %q, want %q", got, want)
	}
}

func TestResolvePlumbTargetTokenLeavesBareAddressUnchanged(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "#56,#81\n",
		Path:     filepath.Join(t.TempDir(), "current.go"),
		DotStart: 0,
		DotEnd:   0,
	})

	if got, want := resolvePlumbTargetToken(state, "#56,#81"), "#56,#81"; got != want {
		t.Fatalf("resolvePlumbTargetToken() = %q, want %q", got, want)
	}
}
