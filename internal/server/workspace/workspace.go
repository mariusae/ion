package workspace

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"ion/internal/core/cmdlang"
	"ion/internal/core/exec"
	"ion/internal/core/text"
	"ion/internal/proto/wire"
)

// Workspace owns the authoritative shared editing state for the current server
// process. It is the initial server-side wrapper around the sam-compatible core.
type Workspace struct {
	mu           sync.Mutex
	session      *exec.Session
	watcher      *fsnotify.Watcher
	watchedDirs  map[string]int
	watchedPaths map[string]struct{}
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
	w := &Workspace{
		session:      sess,
		watchedDirs:  make(map[string]int),
		watchedPaths: make(map[string]struct{}),
	}
	w.initWatcher()
	return w
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
	w.resyncWatchesLocked()
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
	ok, err := w.session.Execute(cmd)
	w.resyncWatchesLocked()
	return ok, err
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
	return w.currentView(state)
}

// BufferSnapshots returns the current shared buffer contents without requiring
// a visible client session.
func (w *Workspace) BufferSnapshots() ([]wire.BufferView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w == nil || w.session == nil {
		return nil, nil
	}
	menu := w.session.MenuFiles()
	previous := w.session.Current
	defer func() {
		w.session.Current = previous
	}()
	out := make([]wire.BufferView, 0, len(menu))
	for _, entry := range menu {
		if entry.ID == 0 {
			continue
		}
		if err := w.session.FocusFileID(entry.ID); err != nil {
			return nil, err
		}
		text, err := w.session.CurrentText()
		if err != nil {
			return nil, err
		}
		out = append(out, wire.BufferView{
			ID:   entry.ID,
			Name: entry.Name,
			Text: text,
		})
	}
	return out, nil
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
	w.resyncWatchesLocked()
	return w.currentView(state)
}

// OpenFilesPaths opens one explicit file list and returns the refreshed current view.
func (w *Workspace) OpenFilesPaths(state *SessionState, files []string, stdout, stderr io.Writer) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	restore := w.bindIO(stdout, stderr)
	defer restore()
	restoreState := w.withSessionState(state)
	defer restoreState()
	err := w.session.OpenFilesPathsAtomic(files)
	w.resyncWatchesLocked()
	return err
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
	err := w.session.OpenFilesPathsAtomicNoNameless(files)
	w.resyncWatchesLocked()
	return err
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
			Changed: f.Changed,
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
	return w.currentView(state)
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
	return w.currentView(state)
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
	return w.currentView(state)
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
	return w.currentView(state)
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
	return w.currentView(state)
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

// SetSessionStatus stores one transient session-local status message.
func (w *Workspace) SetSessionStatus(state *SessionState, status string) {
	if w == nil || state == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	state.status = strings.TrimSpace(status)
	state.statusSeq++
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

func (w *Workspace) currentView(state *SessionState) (wire.BufferView, error) {
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
		ID:        w.session.CurrentFileID(),
		Text:      text,
		Name:      name,
		DotStart:  int(dot.P1),
		DotEnd:    int(dot.P2),
		Status:    stateStatus(state),
		StatusSeq: stateStatusSeq(state),
	}, nil
}

func stateStatus(state *SessionState) string {
	if state == nil {
		return ""
	}
	return state.status
}

func stateStatusSeq(state *SessionState) uint64 {
	if state == nil {
		return 0
	}
	return state.statusSeq
}

func (w *Workspace) initWatcher() {
	if w == nil {
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	w.watcher = watcher
	go w.watchLoop()
}

func (w *Workspace) watchLoop() {
	if w == nil || w.watcher == nil {
		return
	}
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			w.handleWatchPath(event.Name)
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Workspace) handleWatchPath(path string) {
	if w == nil || path == "" {
		return
	}
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.session.Files {
		if watchPathForFile(f) != clean {
			continue
		}
		w.handleWatchedFileLocked(f)
	}
}

func (w *Workspace) handleWatchedFileLocked(f *text.File) {
	if w == nil || w.session == nil || f == nil || f.Unread {
		return
	}
	name := watchPathForFile(f)
	if name == "" {
		return
	}
	meta, ok, err := statWatchedFile(name)
	if err != nil {
		return
	}
	if !ok {
		if f.IsDirty() {
			f.DiskChanged = true
		}
		return
	}
	if f.StatKnown && f.Dev == meta.dev && f.Inode == meta.inode && f.Mtime == meta.mtime {
		return
	}
	if f.IsDirty() {
		f.DiskChanged = true
		return
	}
	_ = w.session.ReloadFileFromDisk(f)
}

func (w *Workspace) resyncWatchesLocked() {
	if w == nil || w.watcher == nil || w.session == nil {
		return
	}
	next := make(map[string]struct{})
	for _, f := range w.session.Files {
		path := watchPathForFile(f)
		if path == "" {
			continue
		}
		next[path] = struct{}{}
	}
	for path := range w.watchedPaths {
		if _, ok := next[path]; ok {
			continue
		}
		w.removeWatchDirLocked(filepath.Dir(path))
		delete(w.watchedPaths, path)
	}
	for path := range next {
		if _, ok := w.watchedPaths[path]; ok {
			continue
		}
		if w.addWatchDirLocked(filepath.Dir(path)) {
			w.watchedPaths[path] = struct{}{}
		}
	}
}

func (w *Workspace) addWatchDirLocked(dir string) bool {
	if w == nil || w.watcher == nil || dir == "" {
		return false
	}
	if w.watchedDirs[dir] > 0 {
		w.watchedDirs[dir]++
		return true
	}
	if err := w.watcher.Add(dir); err != nil {
		return false
	}
	w.watchedDirs[dir] = 1
	return true
}

func (w *Workspace) removeWatchDirLocked(dir string) {
	if w == nil || w.watcher == nil || dir == "" {
		return
	}
	count := w.watchedDirs[dir]
	if count <= 1 {
		_ = w.watcher.Remove(dir)
		delete(w.watchedDirs, dir)
		return
	}
	w.watchedDirs[dir] = count - 1
}

func watchPathForFile(f *text.File) string {
	if f == nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimRight(f.Name.UTF8(), "\x00"))
	if name == "" {
		return ""
	}
	path, err := filepath.Abs(filepath.Clean(name))
	if err != nil {
		return ""
	}
	return path
}

type watchedFileStat struct {
	dev   uint64
	inode uint64
	mtime int64
}

func statWatchedFile(name string) (watchedFileStat, bool, error) {
	info, err := os.Stat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return watchedFileStat{}, false, nil
		}
		return watchedFileStat{}, false, err
	}
	meta := watchedFileStat{mtime: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		meta.dev = uint64(st.Dev)
		meta.inode = uint64(st.Ino)
	}
	return meta, true, nil
}
