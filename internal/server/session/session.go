package session

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"

	ionaddr "ion/internal/core/addr"
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

func (s *DownloadSession) BindIO(stdout, stderr io.Writer) func() {
	if s == nil {
		return func() {}
	}
	previousStdout := s.stdout
	previousStderr := s.stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	s.stdout = stdout
	s.stderr = stderr
	return func() {
		s.stdout = previousStdout
		s.stderr = previousStderr
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
	script = normalizeIonNamespaceAlias(script)
	if isSessionQuitCommand(script) {
		return false, nil
	}
	if handled, view, err := s.executeDemoCommand(script); handled {
		if err != nil {
			return false, err
		}
		if view != nil {
			s.recordView(*view)
		}
		return true, nil
	}
	if trimmed := strings.TrimSpace(script); strings.HasPrefix(trimmed, ":") {
		return false, fmt.Errorf("unknown command `%s'", trimmed)
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

func isSessionQuitCommand(script string) bool {
	switch strings.TrimSpace(script) {
	case "Q", ":ion:Q":
		return true
	default:
		return false
	}
}

func normalizeIonNamespaceAlias(script string) string {
	trimmed := strings.TrimLeft(script, " \t")
	if !strings.HasPrefix(trimmed, "::") {
		return script
	}
	prefixLen := len(script) - len(trimmed)
	return script[:prefixLen] + ":ion:" + trimmed[2:]
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
	targets := parseSessionTargets(fields)
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
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Path == "" {
			continue
		}
		paths = append(paths, target.Path)
	}
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

type sessionTarget struct {
	Path    string
	Address string
}

func parseSessionTargets(args []string) []sessionTarget {
	targets := make([]sessionTarget, 0, len(args))
	for _, arg := range args {
		targets = append(targets, parseSessionTarget(arg))
	}
	return targets
}

func parseSessionTarget(arg string) sessionTarget {
	if arg == "" {
		return sessionTarget{}
	}
	if path, addr, ok := splitSessionTarget(arg); ok {
		return sessionTarget{Path: path, Address: addr}
	}
	return sessionTarget{Path: arg}
}

func splitSessionTarget(arg string) (string, string, bool) {
	for i := 0; i < len(arg); i++ {
		if arg[i] != ':' {
			continue
		}
		base := arg[:i]
		suffix := arg[i+1:]
		if base == "" || suffix == "" {
			continue
		}
		addr, ok := normalizeSessionAddressSuffix(suffix)
		if !ok {
			continue
		}
		return base, addr, true
	}
	return "", "", false
}

func normalizeSessionAddressSuffix(suffix string) (string, bool) {
	if suffix == "" || strings.ContainsAny(suffix, " \t\r\n") {
		return "", false
	}
	if addr, ok := normalizeSessionLegacyLineColumn(suffix); ok {
		return addr, true
	}
	if isValidSessionAddressExpr(suffix) {
		return suffix, true
	}
	return "", false
}

func normalizeSessionLegacyLineColumn(suffix string) (string, bool) {
	last := strings.LastIndexByte(suffix, ':')
	if last <= 0 || last+1 >= len(suffix) {
		if _, err := strconv.Atoi(suffix); err == nil {
			return suffix, true
		}
		return "", false
	}
	line, err := strconv.Atoi(suffix[:last])
	if err != nil {
		return "", false
	}
	col, err := strconv.Atoi(suffix[last+1:])
	if err != nil {
		return "", false
	}
	addr := strconv.Itoa(line)
	if col > 1 {
		addr += "+#" + strconv.Itoa(col-1)
	}
	return addr, true
}

func isValidSessionAddressExpr(expr string) bool {
	parser := cmdlang.NewParser(expr + "\n")
	cmd, err := parser.Parse()
	if err != nil || cmd == nil {
		return false
	}
	return cmd.Cmdc == '\n' && cmd.Addr != nil && validateSessionAddr(cmd.Addr)
}

func validateSessionAddr(a *ionaddr.Addr) bool {
	if a == nil {
		return false
	}
	if a.Left != nil && !validateSessionAddr(a.Left) {
		return false
	}
	if a.Next != nil && !validateSessionAddr(a.Next) {
		return false
	}
	switch a.Type {
	case '#', 'l', '.', '$', '\'', '?', '/':
		return true
	case ',', ';':
		return a.Left != nil && a.Next != nil
	case '+', '-':
		if a.Next == nil {
			return true
		}
		return validateSessionAddr(a.Next)
	default:
		return false
	}
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
