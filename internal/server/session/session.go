package session

import (
	"io"
	"sync/atomic"

	"ion/internal/core/cmdlang"
	"ion/internal/proto/wire"
	"ion/internal/server/workspace"
)

var nextID atomic.Uint64

// DownloadSession binds one client's I/O streams to the shared workspace.
type DownloadSession struct {
	id     uint64
	ws     *workspace.Workspace
	stdout io.Writer
	stderr io.Writer
}

// NewDownload constructs a server-side download session over one workspace.
func NewDownload(ws *workspace.Workspace, stdout, stderr io.Writer) *DownloadSession {
	return &DownloadSession{
		id:     nextID.Add(1),
		ws:     ws,
		stdout: stdout,
		stderr: stderr,
	}
}

// ID reports the server-assigned session identifier.
func (s *DownloadSession) ID() uint64 {
	if s == nil {
		return 0
	}
	return s.id
}

// Bootstrap loads the initial file set for this client.
func (s *DownloadSession) Bootstrap(files []string) error {
	return s.ws.Bootstrap(files, s.stdout, s.stderr)
}

// Execute forwards one parsed command for this client.
func (s *DownloadSession) Execute(cmd *cmdlang.Cmd) (bool, error) {
	return s.ws.Execute(cmd, s.stdout, s.stderr)
}

// TermSession extends a download session with terminal-oriented server methods.
type TermSession struct {
	*DownloadSession
}

var _ wire.DownloadService = (*DownloadSession)(nil)
var _ wire.TermService = (*TermSession)(nil)

// NewTerm constructs a server-side terminal session over one workspace.
func NewTerm(ws *workspace.Workspace, stdout, stderr io.Writer) *TermSession {
	return &TermSession{
		DownloadSession: NewDownload(ws, stdout, stderr),
	}
}

// CurrentView returns the current file text and selection for this client.
func (s *TermSession) CurrentView() (wire.BufferView, error) {
	return s.ws.CurrentView()
}

// MenuFiles returns the current workspace menu snapshot.
func (s *TermSession) MenuFiles() ([]wire.MenuFile, error) {
	return s.ws.MenuFiles()
}

// FocusFile changes this client's current file selection.
func (s *TermSession) FocusFile(id int) (wire.BufferView, error) {
	return s.ws.FocusFile(id)
}

// SetDot updates the current selection.
func (s *TermSession) SetDot(start, end int) (wire.BufferView, error) {
	return s.ws.SetDot(start, end)
}

// Replace applies one text edit and returns the refreshed view.
func (s *TermSession) Replace(start, end int, text string) (wire.BufferView, error) {
	return s.ws.Replace(start, end, text)
}

// Undo reverts the most recent edit and returns the refreshed view.
func (s *TermSession) Undo() (wire.BufferView, error) {
	return s.ws.Undo()
}

// Save writes the current file and returns the resulting status line.
func (s *TermSession) Save() (string, error) {
	return s.ws.Save()
}
