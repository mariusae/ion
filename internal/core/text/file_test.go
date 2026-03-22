package text

import (
	"bytes"
	"strings"
	"testing"
)

func TestFileLogInsertUpdateUndo(t *testing.T) {
	t.Parallel()

	d, err := NewDisk()
	if err != nil {
		t.Fatalf("NewDisk() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	f := NewFile(d)
	f.Unread = false

	if err := f.LogInsert(0, []rune("hello"), 1); err != nil {
		t.Fatalf("LogInsert() error = %v", err)
	}
	if _, _, _, err := f.Update(false); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got, want := f.B.String(), "hello"; got != want {
		t.Fatalf("buffer = %q, want %q", got, want)
	}

	if _, _, err := f.Undo(true, true); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	if got, want := f.B.String(), ""; got != want {
		t.Fatalf("after undo buffer = %q, want empty", got)
	}
}

func TestFileChangeAndUndoRestoresContent(t *testing.T) {
	t.Parallel()

	d, err := NewDisk()
	if err != nil {
		t.Fatalf("NewDisk() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	f := NewFile(d)
	f.Unread = false
	if err := f.B.Insert(0, []rune("one\ntwo\n")); err != nil {
		t.Fatalf("seed insert error = %v", err)
	}

	if err := f.LogDelete(0, Posn(f.B.Len()), 1); err != nil {
		t.Fatalf("LogDelete() error = %v", err)
	}
	if err := f.LogInsert(Posn(f.B.Len()), []rune("three\nfour\n"), 1); err != nil {
		t.Fatalf("LogInsert() error = %v", err)
	}
	if _, _, _, err := f.Update(false); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got, want := f.B.String(), "three\nfour\n"; got != want {
		t.Fatalf("after update buffer = %q, want %q", got, want)
	}

	if _, _, err := f.Undo(true, true); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	if got, want := f.B.String(), "one\ntwo\n"; got != want {
		t.Fatalf("after undo buffer = %q, want %q", got, want)
	}
}

func TestFileLogSetNameUpdateUndo(t *testing.T) {
	t.Parallel()

	d, err := NewDisk()
	if err != nil {
		t.Fatalf("NewDisk() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	f := NewFile(d)
	f.Unread = false
	orig := NewStringFromUTF8("old.txt")
	if err := f.Name.DupString(&orig); err != nil {
		t.Fatalf("DupString() error = %v", err)
	}

	next := NewStringFromUTF8("new.txt")
	if err := f.LogSetName(&next, 1); err != nil {
		t.Fatalf("LogSetName() error = %v", err)
	}
	if _, _, _, err := f.Update(false); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got, want := f.Name.UTF8(), "new.txt"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}

	if _, _, err := f.Undo(true, true); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	if got, want := f.Name.UTF8(), "old.txt"; got != want {
		t.Fatalf("after undo name = %q, want %q", got, want)
	}
}

func TestFileLoadInitialAndWriteTo(t *testing.T) {
	t.Parallel()

	d, err := NewDisk()
	if err != nil {
		t.Fatalf("NewDisk() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	f := NewFile(d)
	loaded, sawNulls, err := f.LoadInitial(strings.NewReader("alpha\nbeta\n"))
	if err != nil {
		t.Fatalf("LoadInitial() error = %v", err)
	}
	if sawNulls {
		t.Fatal("LoadInitial() unexpectedly reported nulls")
	}
	if got, want := loaded, Posn(11); got != want {
		t.Fatalf("loaded = %d, want %d", got, want)
	}
	if f.Unread {
		t.Fatal("file should no longer be unread after LoadInitial")
	}
	if f.Mod {
		t.Fatal("file should be clean after null-free initial load")
	}
	if f.CleanSeq != f.Seq {
		t.Fatalf("CleanSeq = %d, want %d", f.CleanSeq, f.Seq)
	}

	var out bytes.Buffer
	if _, err := f.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	if got, want := out.String(), "alpha\nbeta\n"; got != want {
		t.Fatalf("WriteTo() = %q, want %q", got, want)
	}
}

func TestFileLoadRejectsActiveUndo(t *testing.T) {
	t.Parallel()

	d, err := NewDisk()
	if err != nil {
		t.Fatalf("NewDisk() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	f := NewFile(d)
	f.Seq = 1
	if _, _, err := f.Load(0, strings.NewReader("x")); err == nil {
		t.Fatal("Load() succeeded with active undo state, want error")
	}
}
