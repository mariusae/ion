package workspace

import (
	"io"
	"strings"

	"ion/internal/core/cmdlang"
	"ion/internal/core/exec"
	"ion/internal/core/text"
	"ion/internal/proto/wire"
)

// Workspace owns the authoritative shared editing state for the current server
// process. It is the initial server-side wrapper around the sam-compatible core.
type Workspace struct {
	session *exec.Session
}

// New constructs a workspace backed by a core execution session.
func New(stdout, stderr io.Writer) *Workspace {
	sess := exec.NewSession(stdout)
	sess.Diag = stderr
	return &Workspace{session: sess}
}

// Bootstrap loads the initial file set for a download-mode client.
func (w *Workspace) Bootstrap(files []string) error {
	if len(files) == 0 {
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
func (w *Workspace) Execute(cmd *cmdlang.Cmd) (bool, error) {
	return w.session.Execute(cmd)
}

// CurrentView returns the current file text and selection state for the
// initial terminal client.
func (w *Workspace) CurrentView() (wire.BufferView, error) {
	return w.currentView()
}

// MenuFiles returns the current file-menu snapshot for the terminal client.
func (w *Workspace) MenuFiles() ([]wire.MenuFile, error) {
	files := w.session.MenuFiles()
	out := make([]wire.MenuFile, 0, len(files))
	for i, f := range files {
		out = append(out, wire.MenuFile{
			ID:      i,
			Name:    f.Name,
			Dirty:   f.Dirty,
			Current: f.Current,
		})
	}
	return out, nil
}

// FocusFile switches the current file by file-menu position.
func (w *Workspace) FocusFile(id int) (wire.BufferView, error) {
	if err := w.session.FocusFileIndex(id); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// SetDot updates the current selection for the terminal client.
func (w *Workspace) SetDot(start, end int) (wire.BufferView, error) {
	if err := w.session.SetCurrentDot(text.Posn(start), text.Posn(end)); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// Replace edits the current file through the server-owned core session.
func (w *Workspace) Replace(start, end int, repl string) (wire.BufferView, error) {
	if err := w.session.ReplaceCurrent(text.Posn(start), text.Posn(end), repl); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// Undo reverts the latest change in the current file.
func (w *Workspace) Undo() (wire.BufferView, error) {
	if err := w.session.UndoCurrent(); err != nil {
		return wire.BufferView{}, err
	}
	return w.currentView()
}

// Save writes the current file and returns the resulting status message.
func (w *Workspace) Save() (string, error) {
	return w.session.SaveCurrent()
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
		Text:     text,
		Name:     name,
		DotStart: int(dot.P1),
		DotEnd:   int(dot.P2),
	}, nil
}
