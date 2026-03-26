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
	if got, want := svc.openCalls, [][]string{{"one.txt", "two.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenFiles() = %#v, want %#v", got, want)
	}
	if len(svc.focusCalls) != 0 {
		t.Fatalf("FocusFile() = %#v, want none", svc.focusCalls)
	}
	if got, want := svc.addresses, []string{`"two\.txt"/^func`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetAddress() = %#v, want %#v", got, want)
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
	if len(svc.focusCalls) != 0 {
		t.Fatalf("FocusFile() = %#v, want none", svc.focusCalls)
	}
	if got, want := svc.addresses, []string{`"todo\.txt"/unicode`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetAddress() = %#v, want %#v", got, want)
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
