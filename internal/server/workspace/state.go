package workspace

import "ion/internal/core/text"

type sessionSelection struct {
	dot  text.Range
	mark text.Range
}

type fileScratchState struct {
	dot  text.Range
	ndot text.Range
	mark text.Range
}

// SessionState holds per-client workspace state while sharing the underlying files.
type SessionState struct {
	current   *text.File
	currentOK bool
	status    string
	statusSeq uint64
	selection map[*text.File]sessionSelection
}

// NewSessionState allocates one per-client state object bound to the current workspace snapshot.
func (w *Workspace) NewSessionState() *SessionState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return &SessionState{
		current:   w.defaultCurrentLocked(),
		currentOK: true,
		selection: make(map[*text.File]sessionSelection),
	}
}

func (w *Workspace) withSessionState(state *SessionState) func() {
	previous := w.session.Current
	scratch := w.snapshotFileScratchLocked()
	current := previous
	if state != nil {
		if state.currentOK {
			current = state.current
			if current != nil && !w.hasFileLocked(current) {
				current = nil
			}
		} else {
			current = w.defaultCurrentLocked()
		}
		w.applySessionSelectionLocked(state)
	}
	w.session.Current = current
	return func() {
		if state != nil {
			w.captureSessionSelectionLocked(state)
			state.currentOK = true
			if w.session.Current != nil && !w.hasFileLocked(w.session.Current) {
				state.current = nil
			} else {
				state.current = w.session.Current
			}
		}
		w.restoreFileScratchLocked(scratch)
		w.session.Current = previous
	}
}

func (w *Workspace) snapshotFileScratchLocked() map[*text.File]fileScratchState {
	if w == nil || w.session == nil {
		return nil
	}
	out := make(map[*text.File]fileScratchState, len(w.session.Files))
	for _, f := range w.session.Files {
		if f == nil {
			continue
		}
		out[f] = fileScratchState{
			dot:  f.Dot,
			ndot: f.NDot,
			mark: f.Mark,
		}
	}
	return out
}

func (w *Workspace) restoreFileScratchLocked(scratch map[*text.File]fileScratchState) {
	if w == nil || w.session == nil {
		return
	}
	for f, state := range scratch {
		if f == nil || !w.hasFileLocked(f) {
			continue
		}
		f.Dot = state.dot
		f.NDot = state.ndot
		f.Mark = state.mark
	}
}

func (w *Workspace) applySessionSelectionLocked(state *SessionState) {
	if w == nil || w.session == nil || state == nil {
		return
	}
	for _, f := range w.session.Files {
		if f == nil {
			continue
		}
		selection, ok := state.selection[f]
		if !ok {
			continue
		}
		f.Dot = selection.dot
		f.NDot = selection.dot
		f.Mark = selection.mark
	}
}

func (w *Workspace) captureSessionSelectionLocked(state *SessionState) {
	if w == nil || w.session == nil || state == nil {
		return
	}
	next := make(map[*text.File]sessionSelection, len(w.session.Files))
	for _, f := range w.session.Files {
		if f == nil {
			continue
		}
		next[f] = sessionSelection{
			dot:  f.Dot,
			mark: f.Mark,
		}
	}
	state.selection = next
}

func (w *Workspace) hasFileLocked(target *text.File) bool {
	if target == nil || w == nil || w.session == nil {
		return false
	}
	for _, f := range w.session.Files {
		if f == target {
			return true
		}
	}
	return false
}

func (w *Workspace) defaultCurrentLocked() *text.File {
	if w == nil || w.session == nil {
		return nil
	}
	if w.session.Current != nil && w.hasFileLocked(w.session.Current) {
		return w.session.Current
	}
	if len(w.session.Files) == 0 {
		return nil
	}
	return w.session.Files[0]
}
