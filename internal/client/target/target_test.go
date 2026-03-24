package target

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ion/internal/proto/wire"
)

type fakeService struct {
	openFiles []string
	addresses []string
}

func (f *fakeService) OpenFiles(files []string) (wire.BufferView, error) {
	f.openFiles = append([]string(nil), files...)
	name := ""
	if len(files) > 0 {
		name = files[len(files)-1]
	}
	return wire.BufferView{Name: name}, nil
}

func (f *fakeService) SetAddress(expr string) (wire.BufferView, error) {
	f.addresses = append(f.addresses, expr)
	return wire.BufferView{Name: "addressed", DotStart: 7, DotEnd: 7}, nil
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
	if got, want := svc.openFiles, []string{"one.txt", "two.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenFiles() = %#v, want %#v", got, want)
	}
	if got, want := svc.addresses, []string{"/^func"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetAddress() = %#v, want %#v", got, want)
	}
	if got, want := view.Name, "addressed"; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
}
