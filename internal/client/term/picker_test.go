package term

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ion/internal/proto/wire"
)

func TestCommandCompletionsFromDocsIncludesLocalTermAndMenuCommands(t *testing.T) {
	t.Parallel()

	completions := commandCompletionsFromDocs([]wire.NamespaceProviderDoc{{
		Namespace: "lsp",
		Commands: []wire.NamespaceCommandDoc{{
			Name:    "goto",
			Summary: "jump to definition",
		}},
	}}, []wire.MenuCommand{{
		Command: ":demo:show",
		Label:   "show demo state",
	}})

	names := make(map[string]string, len(completions))
	for _, completion := range completions {
		names[completion.name] = completion.summary
	}
	for _, want := range []string{":help", ":term:snarf", ":term:tmux", ":term:send", ":term:regexp", ":term:split", ":lsp:goto", ":demo:show"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("missing completion %q in %#v", want, names)
		}
	}
	if got, want := names[":demo:show"], "show demo state"; got != want {
		t.Fatalf("menu command summary = %q, want %q", got, want)
	}
}

func TestBuildCommandPickerItemsDedupesHistoryAndPrefersLatest(t *testing.T) {
	t.Parallel()

	items, preferred := buildCommandPickerItems([]wire.NamespaceProviderDoc{{
		Namespace: "lsp",
		Commands: []wire.NamespaceCommandDoc{{
			Name:    "goto",
			Summary: "jump to definition",
		}},
	}}, nil, []string{"!ls", ":help :lsp:goto", "!ls"})

	if got, want := preferred, "history:000001"; got != want {
		t.Fatalf("preferred = %q, want %q", got, want)
	}
	if got, want := items[0].value, ":help :lsp:goto"; got != want {
		t.Fatalf("first picker item = %q, want %q", got, want)
	}
	if got, want := items[1].value, "!ls"; got != want {
		t.Fatalf("second picker item = %q, want %q", got, want)
	}
	if got, want := items[2].value, ":help"; got != want {
		t.Fatalf("first catalog picker item = %q, want %q", got, want)
	}
}

func TestOverlayCommandPickerDefaultsToPreferredAndFilters(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.openPicker(overlayModeCommandPicker, []overlayPickerItem{
		{key: "catalog::help", label: ":help - show detailed help", value: ":help", search: ":help show detailed help"},
		{key: "catalog::term:snarf", label: ":term:snarf - copy the current selection", value: ":term:snarf", search: ":term:snarf copy selection"},
		{key: "catalog::lsp:goto", label: ":lsp:goto - jump to definition", value: ":lsp:goto", search: ":lsp:goto jump definition"},
	}, "catalog::term:snarf")

	selected, ok := overlay.pickerSelected()
	if !ok {
		t.Fatal("pickerSelected() = false, want selected preferred entry")
	}
	if got, want := selected.value, ":term:snarf"; got != want {
		t.Fatalf("selected value = %q, want %q", got, want)
	}

	overlay.insert([]rune("goto"))
	selected, ok = overlay.pickerSelected()
	if !ok {
		t.Fatal("pickerSelected() after filter = false, want one filtered entry")
	}
	if got, want := selected.value, ":lsp:goto"; got != want {
		t.Fatalf("filtered selected value = %q, want %q", got, want)
	}
	if got, want := len(overlay.picker.filtered), 1; got != want {
		t.Fatalf("filtered len = %d, want %d", got, want)
	}
}

func TestBuildFilePickerItemsPrefersCurrentFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	items, preferred := buildFilePickerItems([]wire.MenuFile{
		{ID: 1, Name: "a.txt", Path: filepath.Join(root, "a.txt")},
		{ID: 2, Name: "b.txt", Path: filepath.Join(root, "hidden", "b.txt"), Dirty: true, Current: true},
		{ID: 3, Name: "c.txt", Path: filepath.Join(root, "c.txt"), Changed: true},
	}, 0)

	if got, want := preferred, "file:2"; got != want {
		t.Fatalf("preferred = %q, want %q", got, want)
	}
	if got, want := len(items), 3; got != want {
		t.Fatalf("len(items) = %d, want %d", got, want)
	}
	if got, want := items[1].fileID, 2; got != want {
		t.Fatalf("current item fileID = %d, want %d", got, want)
	}
	if got, want := items[1].label, "'. b.txt"; got != want {
		t.Fatalf("current item label = %q, want %q", got, want)
	}
	if strings.Contains(items[1].search, "hidden") {
		t.Fatalf("current item search = %q, want rendered text only", items[1].search)
	}
}

func TestBuildFilePickerItemsPrefersPreviousUIFile(t *testing.T) {
	t.Parallel()

	_, preferred := buildFilePickerItems([]wire.MenuFile{
		{ID: 1, Name: "a.txt"},
		{ID: 2, Name: "b.txt", Current: true},
		{ID: 3, Name: "c.txt"},
	}, 1)

	if got, want := preferred, "file:1"; got != want {
		t.Fatalf("preferred = %q, want %q", got, want)
	}
}

func TestBuildDirectoryPickerItemsListsCurrentDirectoryFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	currentPath := filepath.Join(root, "current.go")
	if err := os.WriteFile(currentPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(current) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir(subdir) error = %v", err)
	}

	items, preferred, err := buildDirectoryPickerItems(newBufferState(wire.BufferView{
		Name: "current.go",
		Path: currentPath,
	}), []wire.MenuFile{
		{ID: 1, Name: "current.go", Path: currentPath, Dirty: true, Current: true},
		{ID: 2, Name: "other.go", Path: filepath.Join(root, "other.go"), Dirty: true, Changed: true},
	})
	if err != nil {
		t.Fatalf("buildDirectoryPickerItems() error = %v", err)
	}
	if got, want := preferred, "path:"+currentPath; got != want {
		t.Fatalf("preferred = %q, want %q", got, want)
	}
	if got, want := len(items), 2; got != want {
		t.Fatalf("len(items) = %d, want %d", got, want)
	}
	if got, want := items[0].label, "'-. current.go"; got != want {
		t.Fatalf("first label = %q, want %q", got, want)
	}
	if got, want := items[1].label, "\"-  other.go"; got != want {
		t.Fatalf("second label = %q, want %q", got, want)
	}
	if got, want := items[0].path, currentPath; got != want {
		t.Fatalf("first path = %q, want %q", got, want)
	}
	if !items[0].current {
		t.Fatal("first item current = false, want true")
	}
	if got, want := items[1].path, filepath.Join(root, "other.go"); got != want {
		t.Fatalf("second path = %q, want %q", got, want)
	}
	if got, want := items[1].fileID, 2; got != want {
		t.Fatalf("second fileID = %d, want %d", got, want)
	}
	if strings.Contains(items[1].search, root) {
		t.Fatalf("second item search = %q, want rendered text only", items[1].search)
	}
}

func TestBuildDirectoryPickerItemsLeavesUnloadedFilesAligned(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	currentPath := filepath.Join(root, "current.go")
	unloadedPath := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(currentPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(current) error = %v", err)
	}
	if err := os.WriteFile(unloadedPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plain) error = %v", err)
	}

	items, _, err := buildDirectoryPickerItems(newBufferState(wire.BufferView{
		Name: "current.go",
		Path: currentPath,
	}), []wire.MenuFile{
		{ID: 1, Name: "current.go", Path: currentPath, Current: true},
	})
	if err != nil {
		t.Fatalf("buildDirectoryPickerItems() error = %v", err)
	}
	if got, want := items[1].label, "    plain.txt"; got != want {
		t.Fatalf("unloaded label = %q, want %q", got, want)
	}
}

func TestShouldPreviewDirectoryFileRejectsBinary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	textPath := filepath.Join(root, "text.txt")
	binPath := filepath.Join(root, "bin.dat")
	if err := os.WriteFile(textPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(text) error = %v", err)
	}
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("WriteFile(bin) error = %v", err)
	}

	ok, err := shouldPreviewDirectoryFile(textPath)
	if err != nil {
		t.Fatalf("shouldPreviewDirectoryFile(text) error = %v", err)
	}
	if !ok {
		t.Fatal("shouldPreviewDirectoryFile(text) = false, want true")
	}

	ok, err = shouldPreviewDirectoryFile(binPath)
	if err != nil {
		t.Fatalf("shouldPreviewDirectoryFile(binary) error = %v", err)
	}
	if ok {
		t.Fatal("shouldPreviewDirectoryFile(binary) = true, want false")
	}
}

func TestOverlayPickerPromptHasNoPrefix(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.openPicker(overlayModeCommandPicker, []overlayPickerItem{
		{key: "catalog::help", label: ":help", value: ":help", search: ":help"},
	}, "catalog::help")
	overlay.insert([]rune("abc"))

	lines := overlay.renderPromptLines()
	if got, want := len(lines), 1; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	if strings.HasPrefix(lines[0].text, "> ") {
		t.Fatalf("prompt line = %q, want no picker prompt prefix", lines[0].text)
	}
}

func TestAugmentNamespaceDocsAddsLocalTermCommands(t *testing.T) {
	t.Parallel()

	docs := augmentNamespaceDocs([]wire.NamespaceProviderDoc{{
		Namespace: "ion",
		Commands: []wire.NamespaceCommandDoc{{
			Name:    "Q",
			Summary: "quit",
		}},
	}})
	if got, want := len(docs), 2; got != want {
		t.Fatalf("len(docs) = %d, want %d", got, want)
	}
	termIdx := -1
	for i := range docs {
		if docs[i].Namespace == "term" {
			termIdx = i
			break
		}
	}
	if termIdx < 0 {
		t.Fatalf("missing term namespace in %#v", docs)
	}
	var names []string
	for _, command := range docs[termIdx].Commands {
		names = append(names, command.Name)
	}
	for _, want := range []string{"write", "cut", "snarf", "paste", "tmux", "send", "look", "regexp", "plumb", "plumb2", "split"} {
		found := false
		for _, got := range names {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing local term command %q in %#v", want, names)
		}
	}
	if !reflect.DeepEqual(docs[0].Commands[0], wire.NamespaceCommandDoc{Name: "Q", Summary: "quit"}) {
		t.Fatalf("existing command mutated: %#v", docs[0].Commands[0])
	}
}
