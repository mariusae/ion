package session

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	clienttarget "ion/internal/client/target"
	"ion/internal/core/cmdlang"
	"ion/internal/proto/wire"
	"ion/internal/server/workspace"
)

var nextID atomic.Uint64

// DownloadSession binds one client's I/O streams to the shared workspace.
type DownloadSession struct {
	id      uint64
	ws      *workspace.Workspace
	state   *workspace.SessionState
	stdout  io.Writer
	stderr  io.Writer
	parser  *cmdlang.Parser
	history navigationStack
}

// NewDownload constructs a server-side download session over one workspace.
func NewDownload(ws *workspace.Workspace, stdout, stderr io.Writer) *DownloadSession {
	var state *workspace.SessionState
	if ws != nil {
		state = ws.NewSessionState()
	}
	return &DownloadSession{
		id:      nextID.Add(1),
		ws:      ws,
		state:   state,
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
	if err := s.ws.Bootstrap(s.state, files, s.stdout, s.stderr); err != nil {
		return err
	}
	return s.recordCurrentView()
}

// Execute parses and forwards one command script for this client.
func (s *DownloadSession) Execute(script string) (bool, error) {
	if handled, view, err := s.executeDemoCommand(script); handled {
		if err != nil {
			return false, err
		}
		if view != nil {
			s.recordView(*view)
		}
		return true, nil
	}

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
	if handled, err := s.executeAddressedB(cmd); handled || err != nil {
		if err != nil {
			return false, err
		}
		if recErr := s.recordCurrentView(); recErr != nil {
			return false, recErr
		}
		return true, nil
	}
	ok, err := s.ws.Execute(s.state, cmd, s.stdout, s.stderr)
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

func (s *DownloadSession) executeAddressedB(cmd *cmdlang.Cmd) (bool, error) {
	if s == nil || cmd == nil || cmd.Cmdc != 'B' || cmd.Text == nil {
		return false, nil
	}
	raw := strings.TrimRight(cmd.Text.UTF8(), "\x00")
	rest := strings.TrimLeft(raw, " \t")
	if rest == "" || rest[0] == '<' {
		return false, nil
	}
	fields := strings.Fields(rest)
	targets := clienttarget.ParseAll(fields)
	hasAddress := false
	for _, target := range targets {
		if target.Address != "" {
			hasAddress = true
			break
		}
	}
	if !hasAddress || len(targets) == 0 {
		return false, nil
	}
	paths := clienttarget.Paths(targets)
	if err := s.ws.OpenFilesPathsNoNameless(s.state, paths, s.stdout, s.stderr); err != nil {
		return true, err
	}
	last := targets[len(targets)-1]
	if last.Address == "" {
		return true, nil
	}
	_, err := s.ws.SetAddress(s.state, last.Address)
	return true, err
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
	return s.ws.CurrentView(s.state)
}

// Interrupt interrupts one currently running external command in the shared workspace.
func (s *TermSession) Interrupt() error {
	return s.ws.Interrupt()
}

// OpenFiles opens one explicit file list in the shared workspace.
// Navigation is not recorded here; the caller is expected to follow up
// with FocusFile and/or SetAddress which handle recording.
func (s *TermSession) OpenFiles(files []string) (wire.BufferView, error) {
	return s.ws.OpenFiles(s.state, files, s.stdout, s.stderr)
}

// OpenTarget opens one file target and applies its address as one logical navigation.
func (s *TermSession) OpenTarget(path, address string) (wire.BufferView, error) {
	if path == "" {
		return wire.BufferView{}, fmt.Errorf("no target path")
	}
	var view wire.BufferView
	var err error
	current, curErr := s.ws.CurrentView(s.state)
	if curErr == nil && current.Name == path {
		view = current
	} else {
		if err := s.ws.OpenFilesPathsNoNameless(s.state, []string{path}, s.stdout, s.stderr); err != nil {
			return wire.BufferView{}, err
		}
		view, err = s.ws.CurrentView(s.state)
		if err != nil {
			return wire.BufferView{}, err
		}
	}
	if address != "" {
		view, err = s.ws.SetAddress(s.state, address)
		if err != nil {
			return wire.BufferView{}, err
		}
	}
	s.recordView(view)
	return view, nil
}

// MenuFiles returns the current workspace menu snapshot.
func (s *TermSession) MenuFiles() ([]wire.MenuFile, error) {
	return s.ws.MenuFiles(s.state)
}

// NavigationStack returns this client's navigation stack snapshot.
func (s *TermSession) NavigationStack() (wire.NavigationStack, error) {
	return s.navigationStack(), nil
}

// FocusFile changes this client's current file selection.
func (s *TermSession) FocusFile(id int) (wire.BufferView, error) {
	before, _ := s.ws.CurrentView(s.state)
	view, err := s.ws.FocusFile(s.state, id)
	if err != nil {
		return wire.BufferView{}, err
	}
	if before.ID != view.ID {
		s.recordView(view)
	}
	return view, nil
}

// SetAddress resolves one sam address against the current file.
func (s *TermSession) SetAddress(expr string) (wire.BufferView, error) {
	view, err := s.ws.SetAddress(s.state, expr)
	if err != nil {
		return wire.BufferView{}, err
	}
	s.recordView(view)
	return view, nil
}

// SetDot updates the current selection.
func (s *TermSession) SetDot(start, end int) (wire.BufferView, error) {
	return s.ws.SetDot(s.state, start, end)
}

// Replace applies one text edit and returns the refreshed view.
func (s *TermSession) Replace(start, end int, text string) (wire.BufferView, error) {
	return s.ws.Replace(s.state, start, end, text)
}

// Undo reverts the most recent edit and returns the refreshed view.
func (s *TermSession) Undo() (wire.BufferView, error) {
	return s.ws.Undo(s.state)
}

// Save writes the current file and returns the resulting status line.
func (s *TermSession) Save() (string, error) {
	return s.ws.Save(s.state)
}
