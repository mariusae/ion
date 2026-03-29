package workspace

import "ion/internal/core/text"

// SessionState holds per-client workspace state while sharing the underlying files.
type SessionState struct {
	current   *text.File
	status    string
	statusSeq uint64
}

// NewSessionState allocates one per-client state object bound to the current workspace snapshot.
func (w *Workspace) NewSessionState() *SessionState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return &SessionState{current: w.defaultCurrentLocked()}
}

func (w *Workspace) withSessionState(state *SessionState) func() {
	previous := w.session.Current
	current := previous
	if state != nil {
		current = state.current
		if current != nil && !w.hasFileLocked(current) {
			current = nil
		}
		if current == nil {
			current = w.defaultCurrentLocked()
		}
	}
	w.session.Current = current
	return func() {
		if state != nil {
			if w.session.Current != nil && !w.hasFileLocked(w.session.Current) {
				state.current = w.defaultCurrentLocked()
			} else {
				state.current = w.session.Current
			}
		}
		w.session.Current = previous
	}
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
