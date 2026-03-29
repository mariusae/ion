package workspace

import (
	"io"
	"path/filepath"
	"testing"
)

func TestBootstrapMissingFileKeepsEmptyNamedBuffer(t *testing.T) {
	t.Parallel()

	ws := New()
	state := ws.NewSessionState()
	missing := filepath.Join(t.TempDir(), "missing.txt")

	if err := ws.Bootstrap(state, []string{missing}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	view, err := ws.CurrentView(state)
	if err != nil {
		t.Fatalf("CurrentView() error = %v", err)
	}
	if got, want := view.Name, missing; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
	if view.Text != "" {
		t.Fatalf("view.Text = %q, want empty buffer", view.Text)
	}
}
