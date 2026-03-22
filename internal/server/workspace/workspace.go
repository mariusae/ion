package workspace

import (
	"bytes"
	"io"
	"os"

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
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if _, _, err := f.LoadInitial(bytes.NewReader(data)); err != nil {
				return err
			}
			w.session.AddFile(f)
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
	text, err := w.session.CurrentText()
	if err != nil {
		return wire.BufferView{}, err
	}
	dot := w.session.CurrentDot()
	return wire.BufferView{
		Text:     text,
		DotStart: int(dot.P1),
		DotEnd:   int(dot.P2),
	}, nil
}
