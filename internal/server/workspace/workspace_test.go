package workspace

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestWorkspaceWatcherReloadsCleanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ws := New()
	state := ws.NewSessionState()
	if err := ws.Bootstrap(state, []string{path}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	if err := os.WriteFile(path, []byte("beta\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.txt: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes a.txt: %v", err)
	}

	waitForWorkspace(t, func() bool {
		view, err := ws.CurrentView(state)
		return err == nil && view.Text == "beta\n"
	})

	files, err := ws.MenuFiles(state)
	if err != nil {
		t.Fatalf("MenuFiles() error = %v", err)
	}
	if got, want := len(files), 1; got != want {
		t.Fatalf("menu files = %d, want %d", got, want)
	}
	if files[0].Dirty {
		t.Fatalf("menu dirty = true, want false after auto-reload")
	}
	if files[0].Changed {
		t.Fatalf("menu changed = true, want false after auto-reload")
	}
}

func TestWorkspaceWatcherMarksDirtyChangedFileAndSaveStillRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ws := New()
	state := ws.NewSessionState()
	if err := ws.Bootstrap(state, []string{path}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := ws.Replace(state, 0, 5, "local"); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	if err := os.WriteFile(path, []byte("remote\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.txt: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes a.txt: %v", err)
	}

	waitForWorkspace(t, func() bool {
		files, err := ws.MenuFiles(state)
		return err == nil && len(files) == 1 && files[0].Dirty && files[0].Changed
	})

	view, err := ws.CurrentView(state)
	if err != nil {
		t.Fatalf("CurrentView() error = %v", err)
	}
	if got, want := view.Text, "local\n"; got != want {
		t.Fatalf("view.Text = %q, want %q while disk change is deferred", got, want)
	}

	status, err := ws.Save(state)
	if err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	if got, want := status, "?warning: write might change good version of `"+path+"'"; got != want {
		t.Fatalf("first Save() status = %q, want %q", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "remote\n"; got != want {
		t.Fatalf("disk contents after first save = %q, want %q", got, want)
	}

	status, err = ws.Save(state)
	if err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	if got, want := status, path+": #6"; got != want {
		t.Fatalf("second Save() status = %q, want %q", got, want)
	}
	waitForWorkspace(t, func() bool {
		files, err := ws.MenuFiles(state)
		return err == nil && len(files) == 1 && !files[0].Dirty && !files[0].Changed
	})
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() after overwrite error = %v", err)
	}
	if got, want := string(data), "local\n"; got != want {
		t.Fatalf("disk contents after second save = %q, want %q", got, want)
	}
}

func TestSetSessionStatusAppearsInCurrentView(t *testing.T) {
	t.Parallel()

	ws := New()
	state := ws.NewSessionState()
	if err := ws.Bootstrap(state, nil, io.Discard, io.Discard); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	ws.SetSessionStatus(state, "lsp[gopls] ready")

	view, err := ws.CurrentView(state)
	if err != nil {
		t.Fatalf("CurrentView() error = %v", err)
	}
	if got, want := view.Status, "lsp[gopls] ready"; got != want {
		t.Fatalf("view.Status = %q, want %q", got, want)
	}
	if view.StatusSeq == 0 {
		t.Fatal("view.StatusSeq = 0, want non-zero sequence")
	}
}

func waitForWorkspace(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for workspace watcher state")
}
