package target

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ion/internal/proto/wire"
)

type fakeService struct {
	menuFiles   []wire.MenuFile
	openCalls   [][]string
	openTargets []Target
	focusCalls  []int
	addresses   []string
	nextViewSeq int
}

func (f *fakeService) MenuFiles() ([]wire.MenuFile, error) {
	out := make([]wire.MenuFile, len(f.menuFiles))
	copy(out, f.menuFiles)
	return out, nil
}

func (f *fakeService) FocusFile(id int) (wire.BufferView, error) {
	f.focusCalls = append(f.focusCalls, id)
	for i := range f.menuFiles {
		f.menuFiles[i].Current = f.menuFiles[i].ID == id
	}
	return wire.BufferView{Name: f.nameForID(id)}, nil
}

func (f *fakeService) OpenFiles(files []string) (wire.BufferView, error) {
	f.openCalls = append(f.openCalls, append([]string(nil), files...))
	name := ""
	if len(files) > 0 {
		name = files[len(files)-1]
	}
	for _, file := range files {
		f.nextViewSeq++
		for i := range f.menuFiles {
			f.menuFiles[i].Current = false
		}
		f.menuFiles = append(f.menuFiles, wire.MenuFile{ID: 100 + f.nextViewSeq, Name: file, Current: true})
	}
	return wire.BufferView{Name: name}, nil
}

func (f *fakeService) OpenTarget(path, address string) (wire.BufferView, error) {
	f.openTargets = append(f.openTargets, Target{Path: path, Address: address})
	return wire.BufferView{Name: "addressed", DotStart: 7, DotEnd: 7}, nil
}

func (f *fakeService) SetAddress(expr string) (wire.BufferView, error) {
	f.addresses = append(f.addresses, expr)
	return wire.BufferView{Name: "addressed", DotStart: 7, DotEnd: 7}, nil
}

func (f *fakeService) nameForID(id int) string {
	for _, file := range f.menuFiles {
		if file.ID == id {
			return file.Name
		}
	}
	return ""
}

func TestParseNumericSuffix(t *testing.T) {
	t.Parallel()

	got := Parse("README.md:12:4")
	want := Target{Path: "README.md", Address: "12+#3"}
	if got != want {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseSearchSuffix(t *testing.T) {
	t.Parallel()

	got := Parse("file.go:/^func")
	want := Target{Path: "file.go", Address: "/^func"}
	if got != want {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseGenericAddressSuffix(t *testing.T) {
	t.Parallel()

	got := Parse("foo.py:#56,#81")
	want := Target{Path: "foo.py", Address: "#56,#81"}
	if got != want {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseGenericRegexpAddressSuffix(t *testing.T) {
	t.Parallel()

	got := Parse("foo.rs:/func.bar")
	want := Target{Path: "foo.rs", Address: "/func.bar"}
	if got != want {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestTrimTokenUsesLongestValidAddressedPrefix(t *testing.T) {
	t.Parallel()

	if got, want := TrimToken("src/main.go:29:21:use"), "src/main.go:29:21"; got != want {
		t.Fatalf("TrimToken() = %q, want %q", got, want)
	}
	if got, want := TrimToken("foo.py:#56,#81"), "foo.py:#56,#81"; got != want {
		t.Fatalf("TrimToken() = %q, want %q", got, want)
	}
	if got, want := TrimToken("foo.rs:/func.bar)"), "foo.rs:/func.bar"; got != want {
		t.Fatalf("TrimToken() = %q, want %q", got, want)
	}
}

func TestParsePrefersExistingLiteralPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "notes:2")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := Parse(path)
	want := Target{Path: path}
	if got != want {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
}

func TestOpenAppliesLastAddress(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	view, err := Open(svc, []string{"one.txt", "two.txt:/^func"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := svc.openCalls, [][]string{{"one.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenFiles() = %#v, want %#v", got, want)
	}
	if got, want := svc.openTargets, []Target{{Path: "two.txt", Address: "/^func"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenTarget() = %#v, want %#v", got, want)
	}
	if len(svc.focusCalls) != 0 {
		t.Fatalf("FocusFile() = %#v, want none", svc.focusCalls)
	}
	if len(svc.addresses) != 0 {
		t.Fatalf("SetAddress() = %#v, want none", svc.addresses)
	}
	if got, want := view.Name, "addressed"; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
}

func TestOpenFocusesExistingFileInsteadOfReopeningIt(t *testing.T) {
	t.Parallel()

	svc := &fakeService{
		menuFiles: []wire.MenuFile{
			{ID: 7, Name: "todo.txt", Current: true},
		},
	}
	view, err := Open(svc, []string{"todo.txt:/unicode"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(svc.openCalls) != 0 {
		t.Fatalf("OpenFiles() calls = %#v, want none", svc.openCalls)
	}
	if got, want := svc.openTargets, []Target{{Path: "todo.txt", Address: "/unicode"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenTarget() = %#v, want %#v", got, want)
	}
	if len(svc.focusCalls) != 0 {
		t.Fatalf("FocusFile() = %#v, want none", svc.focusCalls)
	}
	if len(svc.addresses) != 0 {
		t.Fatalf("SetAddress() = %#v, want none", svc.addresses)
	}
	if got, want := view.Name, "addressed"; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
}

func TestOpenWithoutAddressStillFocusesExistingFile(t *testing.T) {
	t.Parallel()

	svc := &fakeService{
		menuFiles: []wire.MenuFile{
			{ID: 7, Name: "todo.txt", Current: false},
			{ID: 8, Name: "other.txt", Current: true},
		},
	}
	_, err := Open(svc, []string{"todo.txt"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := svc.focusCalls, []int{7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusFile() = %#v, want %#v", got, want)
	}
	if len(svc.addresses) != 0 {
		t.Fatalf("SetAddress() = %#v, want none", svc.addresses)
	}
}

func TestOpenWithoutAddressUsesOpenTargetForMissingFinalFile(t *testing.T) {
	t.Parallel()

	svc := &fakeService{
		menuFiles: []wire.MenuFile{
			{ID: 7, Name: "loaded.txt", Current: true},
		},
	}
	view, err := Open(svc, []string{"loaded.txt", "missing.txt"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(svc.openCalls) != 0 {
		t.Fatalf("OpenFiles() calls = %#v, want none", svc.openCalls)
	}
	if got, want := svc.openTargets, []Target{{Path: "missing.txt", Address: ""}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenTarget() = %#v, want %#v", got, want)
	}
	if len(svc.focusCalls) != 0 {
		t.Fatalf("FocusFile() = %#v, want none", svc.focusCalls)
	}
	if got, want := view.Name, "addressed"; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
}

func TestParseAddressOnly(t *testing.T) {
	t.Parallel()

	if got, ok := ParseAddressOnly("#56,#81"); !ok || got != "#56,#81" {
		t.Fatalf("ParseAddressOnly(#56,#81) = (%q, %v), want (#56,#81, true)", got, ok)
	}
	if got, ok := ParseAddressOnly("5:2"); !ok || got != "5+#1" {
		t.Fatalf("ParseAddressOnly(5:2) = (%q, %v), want (5+#1, true)", got, ok)
	}
	if got, ok := ParseAddressOnly("README.md:/one"); ok || got != "" {
		t.Fatalf("ParseAddressOnly(file target) = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestOpenTokenAppliesBareAddressToCurrentFile(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	view, err := OpenToken(svc, "#56,#81")
	if err != nil {
		t.Fatalf("OpenToken() error = %v", err)
	}
	if got, want := svc.addresses, []string{"#56,#81"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetAddress() = %#v, want %#v", got, want)
	}
	if len(svc.openTargets) != 0 {
		t.Fatalf("OpenTarget() = %#v, want none", svc.openTargets)
	}
	if got, want := view.Name, "addressed"; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
}

func TestOpenTokenKeepsAddressedFileTargetAsOpen(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	_, err := OpenToken(svc, "README.md:/one")
	if err != nil {
		t.Fatalf("OpenToken() error = %v", err)
	}
	if got, want := svc.openTargets, []Target{{Path: "README.md", Address: "/one"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenTarget() = %#v, want %#v", got, want)
	}
	if len(svc.addresses) != 0 {
		t.Fatalf("SetAddress() = %#v, want none", svc.addresses)
	}
}
