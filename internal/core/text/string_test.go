package text

import (
	"errors"
	"testing"
)

func TestStringInsertDelete(t *testing.T) {
	t.Parallel()

	s := NewStringFromUTF8("ab")
	ins := NewStringFromUTF8("XY")
	if err := s.Insert(&ins, 1); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if got, want := s.UTF8(), "aXYb"; got != want {
		t.Fatalf("after insert got %q want %q", got, want)
	}
	if err := s.Delete(1, 3); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if got, want := s.UTF8(), "ab"; got != want {
		t.Fatalf("after delete got %q want %q", got, want)
	}
}

func TestStringCompareTrailingNUL(t *testing.T) {
	t.Parallel()

	a := NewStringFromUTF8("abc")
	b := NewString0()
	if err := b.DupRunes([]rune{'a', 'b', 'c', 0}); err != nil {
		t.Fatalf("dup runes failed: %v", err)
	}

	if got := CompareString(&a, &b); got != 0 {
		t.Fatalf("compare a vs b = %d, want 0", got)
	}
	if got := CompareString(&b, &a); got != 0 {
		t.Fatalf("compare b vs a = %d, want 0", got)
	}
}

func TestStringIsPrefix(t *testing.T) {
	t.Parallel()

	a := NewStringFromUTF8("ab")
	b := NewStringFromUTF8("abc")
	if !IsPrefix(&a, &b) {
		t.Fatal("expected ab to be a prefix of abc")
	}

	nul := NewString0()
	if err := nul.DupRunes([]rune{'a', 0, 'x'}); err != nil {
		t.Fatalf("dup runes failed: %v", err)
	}
	target := NewStringFromUTF8("ab")
	if !IsPrefix(&nul, &target) {
		t.Fatal("expected embedded NUL prefix handling to match sam")
	}
}

func TestStringEnsureTooLong(t *testing.T) {
	t.Parallel()

	s := NewString()
	err := s.Ensure(MaxStringRunes + 1)
	if !errors.Is(err, ErrStringTooLong) {
		t.Fatalf("Ensure() error = %v, want %v", err, ErrStringTooLong)
	}
}

func TestNullTerminatedLen(t *testing.T) {
	t.Parallel()

	if got, want := NullTerminatedLen([]rune{'a', 'b', 0, 'c'}), 2; got != want {
		t.Fatalf("NullTerminatedLen() = %d, want %d", got, want)
	}
}
