package text

import "errors"

const (
	minStringSize = 16
	maxStringSize = 256

	// MaxStringRunes mirrors sam's STRSIZE limit.
	MaxStringRunes = 4096
)

// ErrStringTooLong reports that a sam-style String exceeded the allowed size.
var ErrStringTooLong = errors.New("text: string too long")

// String is the sam-compatible mutable rune buffer used by the core parser and engine.
//
// It intentionally permits embedded and trailing NUL runes because the original code
// relies on them for some comparisons and temporary values.
type String struct {
	runes []rune
}

// NewString constructs an empty string with the same small initial capacity as sam.
func NewString() String {
	return String{runes: make([]rune, 0, minStringSize)}
}

// NewString0 constructs a string containing a single trailing NUL rune.
func NewString0() String {
	s := NewString()
	s.runes = append(s.runes, 0)
	return s
}

// NewStringFromUTF8 constructs a String from a UTF-8 Go string.
func NewStringFromUTF8(v string) String {
	s := NewString()
	s.runes = append(s.runes, []rune(v)...)
	return s
}

// Len reports the number of runes, including any trailing NUL.
func (s *String) Len() int {
	return len(s.runes)
}

// Runes returns the underlying rune slice.
func (s *String) Runes() []rune {
	return s.runes
}

// UTF8 converts the string into a Go string, preserving embedded NULs.
func (s *String) UTF8() string {
	return string(s.runes)
}

// Close releases the backing storage.
func (s *String) Close() {
	s.runes = nil
}

// Zero resets the string and drops excess capacity similar to sam's Strzero.
func (s *String) Zero() {
	if cap(s.runes) > maxStringSize {
		s.runes = make([]rune, 0, maxStringSize)
		return
	}
	s.runes = s.runes[:0]
}

// Ensure reserves space for at least n runes.
func (s *String) Ensure(n int) error {
	if n > MaxStringRunes {
		return ErrStringTooLong
	}
	if cap(s.runes) >= n {
		return nil
	}

	nextCap := n + 100
	if nextCap < minStringSize {
		nextCap = minStringSize
	}
	buf := make([]rune, len(s.runes), nextCap)
	copy(buf, s.runes)
	s.runes = buf
	return nil
}

// DupRunes replaces the string with a copy of r.
func (s *String) DupRunes(r []rune) error {
	if err := s.Ensure(len(r)); err != nil {
		return err
	}
	s.runes = s.runes[:len(r)]
	copy(s.runes, r)
	return nil
}

// DupString replaces the string with a copy of other.
func (s *String) DupString(other *String) error {
	return s.DupRunes(other.runes)
}

// Add appends one rune.
func (s *String) Add(r rune) error {
	if err := s.Ensure(len(s.runes) + 1); err != nil {
		return err
	}
	s.runes = append(s.runes, r)
	return nil
}

// Insert inserts other at p0.
func (s *String) Insert(other *String, p0 Posn) error {
	i := int(p0)
	if i < 0 || i > len(s.runes) {
		return errors.New("text: insert position out of range")
	}
	if err := s.Ensure(len(s.runes) + len(other.runes)); err != nil {
		return err
	}

	oldLen := len(s.runes)
	s.runes = s.runes[:oldLen+len(other.runes)]
	copy(s.runes[i+len(other.runes):], s.runes[i:oldLen])
	copy(s.runes[i:], other.runes)
	return nil
}

// Delete removes the half-open range [p1, p2).
func (s *String) Delete(p1, p2 Posn) error {
	i := int(p1)
	j := int(p2)
	if i < 0 || j < i || j > len(s.runes) {
		return errors.New("text: delete range out of range")
	}
	copy(s.runes[i:], s.runes[j:])
	s.runes = s.runes[:len(s.runes)-(j-i)]
	return nil
}

// CompareString matches sam's Strcmp behavior, including trailing-NUL equivalence.
func CompareString(a, b *String) int {
	n := len(a.runes)
	if len(b.runes) < n {
		n = len(b.runes)
	}
	for i := 0; i < n; i++ {
		if delta := int(a.runes[i] - b.runes[i]); delta != 0 {
			return delta
		}
	}

	delta := len(a.runes) - len(b.runes)
	if delta == 1 && len(a.runes) > 0 && a.runes[len(a.runes)-1] == 0 {
		return 0
	}
	if delta == -1 && len(b.runes) > 0 && b.runes[len(b.runes)-1] == 0 {
		return 0
	}
	return delta
}

// IsPrefix reports whether a is a prefix of b using sam's NUL-aware semantics.
func IsPrefix(a, b *String) bool {
	n := len(a.runes)
	if len(b.runes) < n {
		n = len(b.runes)
	}
	for i := 0; i < n; i++ {
		if a.runes[i] != b.runes[i] {
			return a.runes[i] == 0
		}
	}
	return len(a.runes) <= len(b.runes)
}

// NullTerminatedLen matches sam's Strlen helper over a NUL-terminated rune slice.
func NullTerminatedLen(r []rune) int {
	for i, v := range r {
		if v == 0 {
			return i
		}
	}
	return len(r)
}
