package wire

// BufferView is the server-owned text and current selection state presented to
// the terminal client.
type BufferView struct {
	Text     string
	DotStart int
	DotEnd   int
}

// TermService is the initial server-side surface needed by the terminal client.
type TermService interface {
	DownloadService
	CurrentView() (BufferView, error)
}
