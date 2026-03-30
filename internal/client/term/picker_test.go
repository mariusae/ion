package term

import (
	"reflect"
	"strings"
	"testing"

	"ion/internal/proto/wire"
)

func TestCommandCompletionsFromDocsIncludesLocalIonAndMenuCommands(t *testing.T) {
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
	for _, want := range []string{":help", ":ion:snarf", ":ion:regexp", ":ion:new", ":lsp:goto", ":demo:show"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("missing completion %q in %#v", want, names)
		}
	}
	if got, want := names[":demo:show"], "show demo state"; got != want {
		t.Fatalf("menu command summary = %q, want %q", got, want)
	}
}

func TestBuildCommandPickerItemsPrefersLastCommand(t *testing.T) {
	t.Parallel()

	items, preferred := buildCommandPickerItems([]wire.NamespaceProviderDoc{{
		Namespace: "lsp",
		Commands: []wire.NamespaceCommandDoc{{
			Name:    "goto",
			Summary: "jump to definition",
		}},
	}}, nil, ":help :lsp:goto")

	if got, want := preferred, ":help :lsp:goto"; got != want {
		t.Fatalf("preferred = %q, want %q", got, want)
	}
	if got, want := items[0].value, ":help :lsp:goto"; got != want {
		t.Fatalf("first picker item = %q, want %q", got, want)
	}
	if got, want := items[0].label, ":help :lsp:goto - last command"; got != want {
		t.Fatalf("first picker label = %q, want %q", got, want)
	}
}

func TestOverlayCommandPickerDefaultsToPreferredAndFilters(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.openPicker(overlayModeCommandPicker, []overlayPickerItem{
		{label: ":help - show detailed help", value: ":help", search: ":help show detailed help"},
		{label: ":ion:snarf - copy the current selection", value: ":ion:snarf", search: ":ion:snarf copy selection"},
		{label: ":lsp:goto - jump to definition", value: ":lsp:goto", search: ":lsp:goto jump definition"},
	}, ":ion:snarf")

	selected, ok := overlay.pickerSelected()
	if !ok {
		t.Fatal("pickerSelected() = false, want selected preferred entry")
	}
	if got, want := selected.value, ":ion:snarf"; got != want {
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

	items, preferred := buildFilePickerItems([]wire.MenuFile{
		{ID: 1, Name: "a.txt"},
		{ID: 2, Name: "b.txt", Dirty: true, Current: true},
		{ID: 3, Name: "c.txt", Changed: true},
	}, 0)

	if got, want := preferred, "b.txt"; got != want {
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
}

func TestBuildFilePickerItemsPrefersPreviousUIFile(t *testing.T) {
	t.Parallel()

	_, preferred := buildFilePickerItems([]wire.MenuFile{
		{ID: 1, Name: "a.txt"},
		{ID: 2, Name: "b.txt", Current: true},
		{ID: 3, Name: "c.txt"},
	}, 1)

	if got, want := preferred, "a.txt"; got != want {
		t.Fatalf("preferred = %q, want %q", got, want)
	}
}

func TestOverlayPickerPromptHasNoPrefix(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.openPicker(overlayModeCommandPicker, []overlayPickerItem{
		{label: ":help", value: ":help", search: ":help"},
	}, ":help")
	overlay.insert([]rune("abc"))

	lines := overlay.renderPromptLines()
	if got, want := len(lines), 1; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	if strings.HasPrefix(lines[0].text, "> ") {
		t.Fatalf("prompt line = %q, want no picker prompt prefix", lines[0].text)
	}
}

func TestAugmentNamespaceDocsAddsLocalIonCommands(t *testing.T) {
	t.Parallel()

	docs := augmentNamespaceDocs([]wire.NamespaceProviderDoc{{
		Namespace: "ion",
		Commands: []wire.NamespaceCommandDoc{{
			Name:    "Q",
			Summary: "quit",
		}},
	}})
	if got, want := len(docs), 1; got != want {
		t.Fatalf("len(docs) = %d, want %d", got, want)
	}
	var names []string
	for _, command := range docs[0].Commands {
		names = append(names, command.Name)
	}
	for _, want := range []string{"Q", "write", "cut", "snarf", "paste", "look", "regexp", "plumb", "plumb2", "new"} {
		found := false
		for _, got := range names {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing local ion command %q in %#v", want, names)
		}
	}
	if !reflect.DeepEqual(docs[0].Commands[0], wire.NamespaceCommandDoc{Name: "Q", Summary: "quit"}) {
		t.Fatalf("existing command mutated: %#v", docs[0].Commands[0])
	}
}
