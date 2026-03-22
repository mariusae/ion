package text

import (
	"bytes"
	"testing"
)

func TestBufferInsertReadDelete(t *testing.T) {
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

	b := NewBuffer(d)
	if err := b.Insert(0, []rune("hello world")); err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if got, want := b.Len(), 11; got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}

	read := make([]rune, 5)
	if err := b.Read(6, read); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got, want := string(read), "world"; got != want {
		t.Fatalf("Read() = %q, want %q", got, want)
	}

	if err := b.Delete(5, 6); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, want := b.String(), "helloworld"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestBufferSpansMultipleBlocks(t *testing.T) {
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

	b := NewBuffer(d)
	src := make([]rune, MaxBlock+25)
	for i := range src {
		src[i] = rune('a' + (i % 26))
	}
	if err := b.Insert(0, src); err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	got := make([]rune, len(src))
	if err := b.Read(0, got); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(got) != string(src) {
		t.Fatal("multi-block round trip mismatch")
	}
}

func TestBufferLoadDropsNulls(t *testing.T) {
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

	b := NewBuffer(d)
	loaded, sawNulls, err := b.Load(0, bytes.NewReader([]byte{'a', 0, 'b'}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !sawNulls {
		t.Fatal("expected sawNulls")
	}
	if got, want := loaded, 2; got != want {
		t.Fatalf("loaded = %d, want %d", got, want)
	}
	if got, want := b.String(), "ab"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestBufferReset(t *testing.T) {
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

	b := NewBuffer(d)
	if err := b.Insert(0, []rune("abc")); err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if err := b.Reset(); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if got := b.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
	if got := b.String(); got != "" {
		t.Fatalf("String() = %q, want empty", got)
	}
}
