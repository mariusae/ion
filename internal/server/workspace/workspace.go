package workspace

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"ion/internal/core/cmdlang"
	"ion/internal/core/exec"
	"ion/internal/core/text"
	"ion/internal/proto/wire"
)

// Workspace owns the authoritative shared editing state for the current server
// process. It is the initial server-side wrapper around the sam-compatible core.
type Workspace struct {
	mu      sync.Mutex
	session *exec.Session
}

// New constructs a workspace backed by a core execution session.
func New() *Workspace {
	return NewWithOptions(exec.ShellInputEmpty, true)
}

// NewWithAutoIndent constructs a workspace with one autoindent policy.
func NewWithAutoIndent(autoIndent bool) *Workspace {
	return NewWithOptions(exec.ShellInputEmpty, autoIndent)
}

// NewWithShellInput constructs a workspace with one shell-stdin policy.
func NewWithShellInput(mode exec.ShellInputMode) *Workspace {
	return NewWithOptions(mode, true)
}

// NewWithOptions constructs a workspace with one shell-stdin and autoindent policy.
func NewWithOptions(mode exec.ShellInputMode, autoIndent bool) *Workspace {
	sess := exec.NewSession(io.Discard)
	sess.Diag = io.Discard
	sess.ShellInput = mode
	sess.AutoIndent = autoIndent
	return &Workspace{session: sess}
}

// SetShellEnv appends fixed shell environment entries for commands run in this workspace.
func (w *Workspace) SetShellEnv(env []string) {
	if w == nil || w.session == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.session.ShellEnv = append([]string(nil), env...)
}

// Bootstrap loads the initial file set for a download-mode client.
func (w *Workspace) Bootstrap(state *SessionState, files []string, stdout, stderr io.Writer) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	restore := w.bindIO(stdout, stderr)
	defer restore()
	restoreState := w.withSessionState(state)
	defer restoreState()

	if len(files) == 0 {
		if len(w.session.Files) > 0 {
			return w.session.PrintCurrentStatus()
		}
		d, err := text.NewDisk()
		if err != nil {
			return err
		}
		f := text.NewFile(d)
		f.Unread = false
		w.session.AddFile(f)
	} else {
		for _, name := range files {
			d, err := text.NewDisk()
			if err != nil {
				return err
			}
			f := text.NewFile(d)
			s := text.NewStringFromUTF8(name)
			if err := f.Name.DupString(&s); err != nil {
				return err
			}
			if _, err := os.Stat(name); err != nil && errors.Is(err, os.ErrNotExist) {
				f.Unread = false
			}
			w.session.AddFile(f)
		}
		if w.session.Current != nil {
			if err := w.session.LoadCurrentIfUnread(); err != nil {
				return err
			}
		}
	}

	return w.session.PrintCurrentStatus()
}

// Execute forwards one parsed command into the authoritative core session.
func (w *Workspace) Execute(state *SessionState, cmd *cmdlang.Cmd, stdout, stderr io.Writer) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	restore := w.bindIO(stdout, stderr)
	defer restore()
	restoreState := w.withSessionState(state)
	defer restoreState()
	return w.session.Execute(cmd)
}

// Interrupt interrupts one currently running external shell command.
func (w *Workspace) Interrupt() error {
	if w == nil || w.session == nil {
		return nil
	}
	return w.session.InterruptShell()
}

// CurrentView returns the current file text and selection state for the
// initial terminal client.
func (w *Workspace) CurrentView(state *SessionState) (wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()
	return w.currentView()
}

// OpenFiles opens one explicit file list and returns the refreshed current view.
func (w *Workspace) OpenFiles(state *SessionState, files []string, stdout, stderr io.Writer) (wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	restore := w.bindIO(stdout, stderr)
	defer restore()
	restoreState := w.withSessionState(state)
	defer restoreState()

	if err := w.session.OpenFilesPathsAtomic(files); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// OpenFilesPaths opens one explicit file list and returns the refreshed current view.
func (w *Workspace) OpenFilesPaths(state *SessionState, files []string, stdout, stderr io.Writer) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	restore := w.bindIO(stdout, stderr)
	defer restore()
	restoreState := w.withSessionState(state)
	defer restoreState()
	return w.session.OpenFilesPathsAtomic(files)
}

// OpenFilesPathsNoNameless opens one explicit file list while suppressing the
// plain `B current-file` shortcut that creates a nameless buffer.
func (w *Workspace) OpenFilesPathsNoNameless(state *SessionState, files []string, stdout, stderr io.Writer) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	restore := w.bindIO(stdout, stderr)
	defer restore()
	restoreState := w.withSessionState(state)
	defer restoreState()
	return w.session.OpenFilesPathsAtomicNoNameless(files)
}

// MenuFiles returns the current file-menu snapshot for the terminal client.
func (w *Workspace) MenuFiles(state *SessionState) ([]wire.MenuFile, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()

	files := w.session.MenuFiles()
	out := make([]wire.MenuFile, 0, len(files))
	for _, f := range files {
		out = append(out, wire.MenuFile{
			ID:      f.ID,
			Name:    f.Name,
			Dirty:   f.Dirty,
			Current: f.Current,
		})
	}
	return out, nil
}

// FocusFile switches the current file by stable file ID.
func (w *Workspace) FocusFile(state *SessionState, id int) (wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()

	if err := w.session.FocusFileID(id); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// SetAddress resolves one sam address against the current file.
func (w *Workspace) SetAddress(state *SessionState, expr string) (wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()

	if err := w.session.SetCurrentAddress(expr); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// SetDot updates the current selection for the terminal client.
func (w *Workspace) SetDot(state *SessionState, start, end int) (wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()

	if err := w.session.SetCurrentDot(text.Posn(start), text.Posn(end)); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// Replace edits the current file through the server-owned core session.
func (w *Workspace) Replace(state *SessionState, start, end int, repl string) (wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()

	if err := w.session.ReplaceCurrent(text.Posn(start), text.Posn(end), repl); err != nil {
		return wire.BufferView{}, err
	}
	cursor := text.Posn(start + len([]rune(repl)))
	if err := w.session.SetCurrentDot(cursor, cursor); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// Undo reverts the latest change in the current file.
func (w *Workspace) Undo(state *SessionState) (wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()

	if err := w.session.UndoCurrent(); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// Save writes the current file and returns the resulting status message.
func (w *Workspace) Save(state *SessionState) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()
	return w.session.SaveCurrent()
}

// PrintCurrentStatus writes the current file status line through the bound
// command/session diagnostics stream.
func (w *Workspace) PrintCurrentStatus(state *SessionState, stdout, stderr io.Writer) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	restore := w.bindIO(stdout, stderr)
	defer restore()
	restoreState := w.withSessionState(state)
	defer restoreState()
	return w.session.PrintCurrentStatus()
}

func (w *Workspace) bindIO(stdout, stderr io.Writer) func() {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	oldOut := w.session.Out
	oldDiag := w.session.Diag
	w.session.Out = stdout
	w.session.Diag = stderr
	return func() {
		w.session.Out = oldOut
		w.session.Diag = oldDiag
	}
}

func (w *Workspace) currentView() (wire.BufferView, error) {
	text, err := w.session.CurrentText()
	if err != nil {
		return wire.BufferView{}, err
	}
	dot := w.session.CurrentDot()
	name := ""
	if w.session.Current != nil {
		name = strings.TrimRight(strings.TrimSpace(w.session.Current.Name.UTF8()), "\x00")
	}
	return wire.BufferView{
		ID:       w.session.CurrentFileID(),
		Text:     text,
		Name:     name,
		DotStart: int(dot.P1),
		DotEnd:   int(dot.P2),
	}, nil
}
