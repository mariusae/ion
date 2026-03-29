package session

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestClientInterruptStopsRunningShellCommand(t *testing.T) {
	t.Parallel()

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
		t.Fatalf("DialUnix(client) error = %v", err)
	}
	defer client.Close()

	interruptClient, err := DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(interrupt) error = %v", err)
	}
	defer interruptClient.Close()

	if err := client.Bootstrap(nil); err != nil {
		t.Fatalf("Bootstrap(nil) error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := client.Execute("!sleep 10\n")
		done <- err
	}()

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Execute(!sleep 10) error = %v", err)
			}
			if got := stderr.String(); strings.Contains(got, "?warning: exit status not 0") {
				t.Fatalf("stderr = %q, want no non-zero exit warning for interrupted shell", got)
			}
			return
		case <-ticker.C:
			if err := interruptClient.Interrupt(); err != nil {
				t.Fatalf("Interrupt() error = %v", err)
			}
		case <-deadline:
			t.Fatal("Execute(!sleep 10) did not stop after interrupt")
		}
	}
}

func TestClientsKeepIndependentCurrentFileSelections(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fileA := filepath.Join(root, "a.txt")
	fileB := filepath.Join(root, "b.txt")
	if err := os.WriteFile(fileA, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.txt) error = %v", err)
	}
	if err := os.WriteFile(fileB, []byte("beta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.txt) error = %v", err)
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

	client1, err := DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(client1) error = %v", err)
	}
	defer client1.Close()
	client2, err := DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(client2) error = %v", err)
	}
	defer client2.Close()

	if err := client1.Bootstrap([]string{fileA, fileB}); err != nil {
		t.Fatalf("client1.Bootstrap() error = %v", err)
	}
	if err := client2.Bootstrap(nil); err != nil {
		t.Fatalf("client2.Bootstrap(nil) error = %v", err)
	}

	view2, err := client2.CurrentView()
	if err != nil {
		t.Fatalf("client2.CurrentView() initial error = %v", err)
	}
	if got, want := view2.Name, fileA; got != want {
		t.Fatalf("client2 initial current file = %q, want %q", got, want)
	}

	if _, err := client1.OpenTarget(fileB, ""); err != nil {
		t.Fatalf("client1.OpenTarget(fileB) error = %v", err)
	}

	view1, err := client1.CurrentView()
	if err != nil {
		t.Fatalf("client1.CurrentView() error = %v", err)
	}
	if got, want := view1.Name, fileB; got != want {
		t.Fatalf("client1 current file = %q, want %q", got, want)
	}

	view2, err = client2.CurrentView()
	if err != nil {
		t.Fatalf("client2.CurrentView() after client1 change error = %v", err)
	}
	if got, want := view2.Name, fileA; got != want {
		t.Fatalf("client2 current file after client1 change = %q, want %q", got, want)
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
