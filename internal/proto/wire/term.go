package wire

import "time"

// BufferView is the server-owned text and current selection state presented to
// the terminal client.
type BufferView struct {
	ID        int
	Text      string
	Name      string
	Path      string
	DotStart  int
	DotEnd    int
	Status    string
	StatusSeq uint64
}

// MenuFile is the server-owned file-menu entry presented to the terminal UI.
type MenuFile struct {
	ID      int
	Name    string
	Path    string
	Dirty   bool
	Changed bool
	Current bool
}

// MenuCommand is one shared custom menu action exposed by the server.
type MenuCommand struct {
	Command string
	Label   string
}

// MenuSnapshot is the full context-menu model shared with terminal clients.
type MenuSnapshot struct {
	Files    []MenuFile
	Commands []MenuCommand
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

// SessionSummary describes one live server-managed session.
type SessionSummary struct {
	ID               uint64
	Owner            bool
	Controlled       bool
	Taken            bool
	CurrentFile      string
	LastActiveUnixMs int64
}

// Invocation describes one delegated extension command invocation.
type Invocation struct {
	ID        uint64
	SessionID uint64
	Script    string
}

// SessionStatusUpdate carries one transient session-scoped status message.
type SessionStatusUpdate struct {
	SessionID uint64
	Status    string
}

func (s SessionSummary) LastActive() time.Time {
	if s.LastActiveUnixMs == 0 {
		return time.Time{}
	}
	return time.UnixMilli(s.LastActiveUnixMs)
}

// TermService is the initial server-side surface needed by the terminal client.
type TermService interface {
	DownloadService
	CurrentView() (BufferView, error)
	OpenFiles(files []string) (BufferView, error)
	OpenTarget(path, address string) (BufferView, error)
	MenuFiles() ([]MenuFile, error)
	NavigationStack() (NavigationStack, error)
	FocusFile(id int) (BufferView, error)
	SetAddress(expr string) (BufferView, error)
	SetDot(start, end int) (BufferView, error)
	Replace(start, end int, text string) (BufferView, error)
	Undo() (BufferView, error)
	Save() (string, error)
}
