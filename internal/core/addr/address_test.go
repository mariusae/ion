package addr

import (
	"strings"
	"testing"

	"ion/internal/core/text"
)

func newLoadedFile(t *testing.T, name, body string) *text.File {
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
	if name != "" {
		s := text.NewStringFromUTF8(name)
		if err := f.Name.DupString(&s); err != nil {
			t.Fatalf("DupString() error = %v", err)
		}
	}
	if _, _, err := f.LoadInitial(strings.NewReader(body)); err != nil {
		t.Fatalf("LoadInitial() error = %v", err)
	}
	return f
}

func TestCharAddr(t *testing.T) {
	t.Parallel()

	f := newLoadedFile(t, "", "hello\n")
	got, err := CharAddr(3, Address{F: f}, 0)
	if err != nil {
		t.Fatalf("CharAddr() error = %v", err)
	}
	if got.R.P1 != 3 || got.R.P2 != 3 {
		t.Fatalf("CharAddr() = [%d,%d), want [3,3)", got.R.P1, got.R.P2)
	}
}

func TestLineAddrAbsoluteAndBackward(t *testing.T) {
	t.Parallel()

	f := newLoadedFile(t, "", "zero\none\ntwo\nthree\n")
	line2, err := LineAddr(2, Address{F: f}, 0)
	if err != nil {
		t.Fatalf("LineAddr absolute error = %v", err)
	}
	if line2.R.P1 != 5 || line2.R.P2 != 9 {
		t.Fatalf("line2 = [%d,%d), want [5,9)", line2.R.P1, line2.R.P2)
	}

	prev, err := LineAddr(1, Address{F: f, R: text.Range{P1: 9, P2: 13}}, -1)
	if err != nil {
		t.Fatalf("LineAddr backward error = %v", err)
	}
	if prev.R.P1 != 5 || prev.R.P2 != 9 {
		t.Fatalf("prev line = [%d,%d), want [5,9)", prev.R.P1, prev.R.P2)
	}
}

func TestResolveSearch(t *testing.T) {
	t.Parallel()

	f := newLoadedFile(t, "", "alpha\nbeta\ngamma\n")
	re := text.NewStringFromUTF8("beta")
	ev := &Evaluator{}
	got, err := ev.Resolve(&Addr{Type: '/', Re: &re}, Address{F: f}, 0)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.R.P1 != 6 || got.R.P2 != 10 {
		t.Fatalf("search = [%d,%d), want [6,10)", got.R.P1, got.R.P2)
	}
}

func TestResolveRange(t *testing.T) {
	t.Parallel()

	f := newLoadedFile(t, "", "zero\none\ntwo\nthree\n")
	ev := &Evaluator{}
	ap := &Addr{
		Type: ',',
		Left: &Addr{Type: 'l', Num: 2},
		Next: &Addr{Type: 'l', Num: 3},
	}
	got, err := ev.Resolve(ap, Address{F: f}, 0)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.R.P1 != 5 || got.R.P2 != 13 {
		t.Fatalf("range = [%d,%d), want [5,13)", got.R.P1, got.R.P2)
	}
}

func TestResolveQuotedFileMatch(t *testing.T) {
	t.Parallel()

	f1 := newLoadedFile(t, "alpha.txt", "one\n")
	f2 := newLoadedFile(t, "beta.txt", "two\n")
	re := text.NewStringFromUTF8("beta")
	ev := &Evaluator{Files: []*text.File{f1, f2}}
	got, err := ev.Resolve(&Addr{Type: '"', Re: &re}, Address{F: f1}, 0)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.F != f2 {
		t.Fatal("quoted file search did not pick the expected file")
	}
}
