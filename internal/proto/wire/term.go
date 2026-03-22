package wire

// BufferView is the server-owned text and current selection state presented to
// the terminal client.
type BufferView struct {
	Text     string
	Name     string
	DotStart int
	DotEnd   int
}

// MenuFile is the server-owned file-menu entry presented to the terminal UI.
type MenuFile struct {
	ID      int
	Name    string
	Dirty   bool
	Current bool
}

// TermService is the initial server-side surface needed by the terminal client.
type TermService interface {
	DownloadService
	CurrentView() (BufferView, error)
	MenuFiles() ([]MenuFile, error)
	FocusFile(id int) (BufferView, error)
	SetDot(start, end int) (BufferView, error)
	Replace(start, end int, text string) (BufferView, error)
	Undo() (BufferView, error)
	Save() (string, error)
}
