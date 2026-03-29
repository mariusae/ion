package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"

	clientsession "ion/internal/client/session"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

func TestResidentAttachKeyUsesTmuxSessionWhenAvailable(t *testing.T) {
	t.Parallel()

	tmux := &fakeTmux{sessionID: "$7"}
	key, err := residentAttachKey(attachRuntime{
		getenv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux.sock"
			}
			return ""
		},
		getwd:   func() (string, error) { return "/tmp/work", nil },
		tempDir: t.TempDir,
		tmux:    tmux.run,
	})
	if err != nil {
		t.Fatalf("residentAttachKey() error = %v", err)
	}
	if got, want := key, "tmux-session:$7"; got != want {
		t.Fatalf("residentAttachKey() = %q, want %q", got, want)
	}
}

func TestResidentAttachKeyFallsBackToWorkingDirectory(t *testing.T) {
	t.Parallel()

	key, err := residentAttachKey(attachRuntime{
		getenv:  func(string) string { return "" },
		getwd:   func() (string, error) { return "/tmp/work/dir", nil },
		tempDir: t.TempDir,
		tmux:    runTmuxCommand,
	})
	if err != nil {
		t.Fatalf("residentAttachKey() error = %v", err)
	}
	if got, want := key, "cwd:/tmp/work/dir"; got != want {
		t.Fatalf("residentAttachKey() = %q, want %q", got, want)
	}
}

func TestWithResidentServerSocketClientsCreatesServerWhenMissing(t *testing.T) {
	t.Parallel()

	socketPath, cleanup, err := attachTestSocketPath()
	if err != nil {
		t.Fatalf("attachTestSocketPath() error = %v", err)
	}
	defer cleanup()
	if err := withResidentServerSocketClients(workspace.New(), socketPath, io.Discard, io.Discard, func(client, interruptClient *clientsession.Client, refresh <-chan struct{}, created bool) error {
		if !created {
			return fmt.Errorf("created = false, want true")
		}
		if refresh == nil {
			return fmt.Errorf("refresh = nil, want notifier channel")
		}
		return client.Bootstrap(nil)
	}); err != nil {
		t.Fatalf("withResidentServerSocketClients(create) error = %v", err)
	}
}

func TestWithResidentServerSocketClientsAttachesToExistingServer(t *testing.T) {
	t.Parallel()

	socketPath, cleanup, err := attachTestSocketPath()
	if err != nil {
		t.Fatalf("attachTestSocketPath() error = %v", err)
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
		_ = os.Remove(socketPath)
		if err := <-serverErr; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	}()

	if err := withResidentServerSocketClients(workspace.New(), socketPath, io.Discard, io.Discard, func(client, interruptClient *clientsession.Client, refresh <-chan struct{}, created bool) error {
		if created {
			return fmt.Errorf("created = true, want false")
		}
		if refresh != nil {
			return fmt.Errorf("refresh != nil for attached client")
		}
		if err := client.Bootstrap(nil); err != nil {
			return err
		}
		view, err := client.CurrentView()
		if err != nil {
			return err
		}
		if view.ID == 0 {
			return fmt.Errorf("view.ID = 0, want bootstrapped current file")
		}
		return nil
	}); err != nil {
		t.Fatalf("withResidentServerSocketClients(attach) error = %v", err)
	}
}

func attachTestSocketPath() (string, func(), error) {
	dir := "/tmp"
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "ion-attach-*.sock")
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
