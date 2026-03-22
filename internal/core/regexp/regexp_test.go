package regexp

import (
	"strings"
	"testing"

	"ion/internal/core/text"
)

func newLoadedFile(t *testing.T, s string) *text.File {
	t.Helper()
	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("NewDisk() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	f := text.NewFile(d)
	if _, _, err := f.LoadInitial(strings.NewReader(s)); err != nil {
		t.Fatalf("LoadInitial() error = %v", err)
	}
	return f
}

func mustCompile(t *testing.T, expr string) *Pattern {
	t.Helper()
	s := text.NewStringFromUTF8(expr)
	p, err := Compile(&s)
	if err != nil {
		t.Fatalf("Compile(%q) error = %v", expr, err)
	}
	return p
}

func TestExecuteLiteral(t *testing.T) {
	t.Parallel()
	f := newLoadedFile(t, "well hello there\n")
	p := mustCompile(t, "hello")
	got, ok, err := p.Execute(f, 0, maxPosn())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !ok {
		t.Fatal("Execute() reported no match")
	}
	if got.P[0].P1 != 5 || got.P[0].P2 != 10 {
		t.Fatalf("match = [%d,%d), want [5,10)", got.P[0].P1, got.P[0].P2)
	}
}

func TestExecuteSubmatch(t *testing.T) {
	t.Parallel()
	f := newLoadedFile(t, "xxabczz")
	p := mustCompile(t, "(ab)c")
	got, ok, err := p.Execute(f, 0, maxPosn())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !ok {
		t.Fatal("Execute() reported no match")
	}
	if got.P[0].P1 != 2 || got.P[0].P2 != 5 {
		t.Fatalf("whole match = [%d,%d), want [2,5)", got.P[0].P1, got.P[0].P2)
	}
	if got.P[1].P1 != 2 || got.P[1].P2 != 4 {
		t.Fatalf("submatch = [%d,%d), want [2,4)", got.P[1].P1, got.P[1].P2)
	}
}

func TestExecuteClassAndAnchors(t *testing.T) {
	t.Parallel()
	f := newLoadedFile(t, "alpha\nbeta\ngamma\n")
	p := mustCompile(t, "^b[et]+a$")
	got, ok, err := p.Execute(f, 0, maxPosn())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !ok {
		t.Fatal("Execute() reported no match")
	}
	if got.P[0].P1 != 6 || got.P[0].P2 != 10 {
		t.Fatalf("match = [%d,%d), want [6,10)", got.P[0].P1, got.P[0].P2)
	}
}

func TestExecuteAnyDoesNotCrossNewline(t *testing.T) {
	t.Parallel()
	f := newLoadedFile(t, "a\nc")
	p := mustCompile(t, "a.c")
	_, ok, err := p.Execute(f, 0, maxPosn())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if ok {
		t.Fatal("Execute() matched across newline")
	}
}

func TestBackwardExecute(t *testing.T) {
	t.Parallel()
	f := newLoadedFile(t, "ab xx ab yy")
	p := mustCompile(t, "ab")
	got, ok, err := p.BExecute(f, text.Posn(f.B.Len()))
	if err != nil {
		t.Fatalf("BExecute() error = %v", err)
	}
	if !ok {
		t.Fatal("BExecute() reported no match")
	}
	if got.P[0].P1 != 6 || got.P[0].P2 != 8 {
		t.Fatalf("backward match = [%d,%d), want [6,8)", got.P[0].P1, got.P[0].P2)
	}
}
