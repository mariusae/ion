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
	id      uint64
	ws      *workspace.Workspace
	stdout  io.Writer
	stderr  io.Writer
	parser  *cmdlang.Parser
	history navigationStack
}

// NewDownload constructs a server-side download session over one workspace.
func NewDownload(ws *workspace.Workspace, stdout, stderr io.Writer) *DownloadSession {
	return &DownloadSession{
		id:      nextID.Add(1),
		ws:      ws,
		stdout:  stdout,
		stderr:  stderr,
		parser:  cmdlang.NewParserRunes(nil),
		history: navigationStack{index: -1},
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
	if err := s.ws.Bootstrap(files, s.stdout, s.stderr); err != nil {
		return err
	}
	return s.recordCurrentView()
}

// Execute parses and forwards one command script for this client.
func (s *DownloadSession) Execute(script string) (bool, error) {
	runes := []rune(script)
	s.parser.ResetRunes(runes)
	cmd, err := s.parser.ParseWithFinal(true)
	if err != nil {
		return false, err
	}
	if consumed := s.parser.Consumed(); consumed != len(runes) {
		return false, cmdlang.ErrNeedMoreInput
	}
	if cmd == nil {
		return true, nil
	}
	switch cmd.Cmdc {
	case 'N':
		return s.navigate(1)
	case 'P':
		return s.navigate(-1)
	case 'S':
		return s.showNavigationStack()
	}
	ok, err := s.ws.Execute(cmd, s.stdout, s.stderr)
	if err != nil || !ok {
		return ok, err
	}
	if shouldRecordCommandNavigation(cmd) {
		if recErr := s.recordCurrentView(); recErr != nil {
			return false, recErr
		}
	}
	return ok, nil
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

// OpenFiles opens one explicit file list in the shared workspace.
func (s *TermSession) OpenFiles(files []string) (wire.BufferView, error) {
	view, err := s.ws.OpenFiles(files, s.stdout, s.stderr)
	if err != nil {
		return wire.BufferView{}, err
	}
	s.recordView(view)
	return view, nil
}

// MenuFiles returns the current workspace menu snapshot.
func (s *TermSession) MenuFiles() ([]wire.MenuFile, error) {
	return s.ws.MenuFiles()
}

// FocusFile changes this client's current file selection.
func (s *TermSession) FocusFile(id int) (wire.BufferView, error) {
	view, err := s.ws.FocusFile(id)
	if err != nil {
		return wire.BufferView{}, err
	}
	s.recordView(view)
	return view, nil
}

// SetAddress resolves one sam address against the current file.
func (s *TermSession) SetAddress(expr string) (wire.BufferView, error) {
	view, err := s.ws.SetAddress(expr)
	if err != nil {
		return wire.BufferView{}, err
	}
	s.recordView(view)
	return view, nil
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
