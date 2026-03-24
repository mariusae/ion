package session

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

func TestClientRoundTripsDownloadAndTermRequests(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	socketPath, cleanup, err := testSocketPath()
	if err != nil {
		t.Fatalf("testSocketPath() error = %v", err)
	}
	defer cleanup()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	server := transport.New(workspace.New())
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		if err := <-serverErr; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	}()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	client, err := DialUnix(socketPath, &stdout, &stderr)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer client.Close()

	if err := client.Bootstrap([]string{path}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, " -. "+path+"\n") {
		t.Fatalf("bootstrap stderr = %q, want status line", got)
	}

	ok, err := client.Execute(",p\n")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !ok {
		t.Fatal("Execute() reported stop, want continue")
	}
	if got := stdout.String(); got != "alpha\nbeta\n" {
		t.Fatalf("stdout after print = %q, want %q", got, "alpha\nbeta\n")
	}

	view, err := client.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() error = %v", err)
	}
	if view.ID == 0 {
		t.Fatalf("view.ID = 0, want stable file id")
	}
	if got, want := view.Name, path; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}

	view, err = client.Replace(0, 5, "omega")
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if got, want := view.Text, "omega\nbeta\n"; got != want {
		t.Fatalf("view.Text = %q, want %q", got, want)
	}

	files, err := client.MenuFiles()
	if err != nil {
		t.Fatalf("MenuFiles() error = %v", err)
	}
	if len(files) != 1 || files[0].Name != path || !files[0].Dirty || !files[0].Current {
		t.Fatalf("MenuFiles() = %#v, want one current dirty file %q", files, path)
	}
	if got, want := files[0].ID, view.ID; got != want {
		t.Fatalf("file id = %d, want current view id %d", got, want)
	}
}

func testSocketPath() (string, func(), error) {
	dir := "/tmp"
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "ion-test-*.sock")
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	return path, func() {
		_ = os.Remove(path)
	}, nil
}
