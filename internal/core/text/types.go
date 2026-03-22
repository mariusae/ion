package text

// Posn is a file position or address, matching sam's core notion of location.
type Posn int64

// Range is a half-open interval over file positions.
type Range struct {
	P1 Posn
	P2 Posn
}
