package term

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ion/internal/proto/wire"
)

func TestCompleteOverlayCommandInputUniqueMatch(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		namespaceDocs: []wire.NamespaceProviderDoc{{
			Namespace: "lsp",
			Commands: []wire.NamespaceCommandDoc{{
				Name:    "goto",
				Summary: "jump to definition",
			}},
		}},
	}
	overlay := newOverlayState()
	overlay.input = []rune(":lsp:g")
	overlay.cursor = len(overlay.input)

	handled, lines, err := completeOverlayCommandInput(svc, overlay)
	if err != nil {
		t.Fatalf("completeOverlayCommandInput() error = %v", err)
	}
	if !handled {
		t.Fatal("completeOverlayCommandInput() handled = false, want true")
	}
	if got, want := string(overlay.input), ":lsp:goto"; got != want {
		t.Fatalf("overlay input = %q, want %q", got, want)
	}
	if len(lines) != 0 {
		t.Fatalf("completion lines = %#v, want none", lines)
	}
}

func TestCompleteOverlayCommandInputMultipleMatchesExtendCommonPrefixAndDisplaySummaries(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		namespaceDocs: []wire.NamespaceProviderDoc{{
			Namespace: "foo",
			Commands: []wire.NamespaceCommandDoc{
				{Name: "bar", Summary: "bar the foo"},
				{Name: "baz", Summary: "baz the foo"},
			},
		}},
	}
	overlay := newOverlayState()
	overlay.input = []rune(":foo")
	overlay.cursor = len(overlay.input)

	handled, lines, err := completeOverlayCommandInput(svc, overlay)
	if err != nil {
		t.Fatalf("completeOverlayCommandInput() error = %v", err)
	}
	if !handled {
		t.Fatal("completeOverlayCommandInput() handled = false, want true")
	}
	if got, want := string(overlay.input), ":foo:ba"; got != want {
		t.Fatalf("overlay input = %q, want %q", got, want)
	}
	wantLines := []string{
		":foo:bar - bar the foo",
		":foo:baz - baz the foo",
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Fatalf("completion lines = %#v, want %#v", lines, wantLines)
	}
}

func TestCompleteOverlayCommandInputOnlyHandlesLeadingCommandToken(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		namespaceDocs: []wire.NamespaceProviderDoc{{
			Namespace: "foo",
			Commands:  []wire.NamespaceCommandDoc{{Name: "bar", Summary: "bar the foo"}},
		}},
	}
	overlay := newOverlayState()
	overlay.input = []rune("x :foo")
	overlay.cursor = len(overlay.input)

	handled, lines, err := completeOverlayCommandInput(svc, overlay)
	if err != nil {
		t.Fatalf("completeOverlayCommandInput() error = %v", err)
	}
	if handled {
		t.Fatalf("completeOverlayCommandInput() handled = true, want false (lines=%#v)", lines)
	}
}

func TestCompleteOverlayFileInputSharedPrefixThenListing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		path := filepath.Join(root, "foo", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	overlay := newOverlayState()
	overlay.input = []rune("f")
	overlay.cursor = len(overlay.input)

	handled, lines := completeOverlayFileInput(overlay, root)
	if !handled {
		t.Fatal("completeOverlayFileInput() handled = false, want true")
	}
	if got, want := string(overlay.input), "foo/"; got != want {
		t.Fatalf("overlay input after first completion = %q, want %q", got, want)
	}
	if len(lines) != 0 {
		t.Fatalf("first completion lines = %#v, want none", lines)
	}

	handled, lines = completeOverlayFileInput(overlay, root)
	if !handled {
		t.Fatal("second completeOverlayFileInput() handled = false, want true")
	}
	wantLines := []string{"a", "b", "c"}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Fatalf("second completion lines = %#v, want %#v", lines, wantLines)
	}
}

func TestCompleteOverlayFileInputUniqueMatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "alpha.txt")
	if err := os.WriteFile(path, []byte("alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	overlay := newOverlayState()
	overlay.input = []rune("alp")
	overlay.cursor = len(overlay.input)

	handled, lines := completeOverlayFileInput(overlay, root)
	if !handled {
		t.Fatal("completeOverlayFileInput() handled = false, want true")
	}
	if got, want := string(overlay.input), "alpha.txt"; got != want {
		t.Fatalf("overlay input = %q, want %q", got, want)
	}
	if len(lines) != 0 {
		t.Fatalf("completion lines = %#v, want none", lines)
	}
}

func TestCompleteOverlayFileInputEmptyStringAlsoWorks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		path := filepath.Join(root, "foo", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	overlay := newOverlayState()
	handled, lines := completeOverlayFileInput(overlay, root)
	if !handled {
		t.Fatal("completeOverlayFileInput() handled = false, want true")
	}
	if got, want := string(overlay.input), "foo/"; got != want {
		t.Fatalf("overlay input = %q, want %q", got, want)
	}
	if len(lines) != 0 {
		t.Fatalf("completion lines = %#v, want none", lines)
	}
}
