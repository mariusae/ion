package wire

// BufferView is the server-owned text and current selection state presented to
// the terminal client.
type BufferView struct {
	ID       int
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

// NavigationEntry is one formatted navigation-stack location label.
type NavigationEntry struct {
	Label string
}

// NavigationStack is the per-client navigation stack plus current position.
type NavigationStack struct {
	Entries []NavigationEntry
	Current int
}

// TermService is the initial server-side surface needed by the terminal client.
type TermService interface {
	DownloadService
	CurrentView() (BufferView, error)
	OpenFiles(files []string) (BufferView, error)
	MenuFiles() ([]MenuFile, error)
	NavigationStack() (NavigationStack, error)
	FocusFile(id int) (BufferView, error)
	SetAddress(expr string) (BufferView, error)
	SetDot(start, end int) (BufferView, error)
	Replace(start, end int, text string) (BufferView, error)
	Undo() (BufferView, error)
	Save() (string, error)
}
