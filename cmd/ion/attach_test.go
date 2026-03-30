package main

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

	"ion/internal/proto/wire"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

func TestResidentAttachKeyUsesTmuxWindowWhenAvailable(t *testing.T) {
	t.Parallel()

	tmux := &fakeTmux{sessionID: "$7", windowID: "@7"}
	key, err := residentAttachKey(config{}, residentRuntime{
		getenv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux.sock"
			}
			return ""
		},
		getwd:      func() (string, error) { return "/tmp/work", nil },
		tempDir:    t.TempDir,
		tmux:       tmux.run,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
	})
	if err != nil {
		t.Fatalf("residentAttachKey() error = %v", err)
	}
	if got, want := key, "tmux-window:@7"; got != want {
		t.Fatalf("residentAttachKey() = %q, want %q", got, want)
	}
}

func TestResidentAttachKeyUsesPaneOverrideWindowWhenProvided(t *testing.T) {
	t.Parallel()

	tmux := &fakeTmux{
		sessionID: "$7",
		windowID:  "@7",
		paneWindows: map[string]string{
			"%54": "@54",
		},
	}
	key, err := residentAttachKey(config{paneID: "%54"}, residentRuntime{
		getenv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux.sock"
			}
			return ""
		},
		getwd:      func() (string, error) { return "/tmp/work", nil },
		tempDir:    t.TempDir,
		tmux:       tmux.run,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
	})
	if err != nil {
		t.Fatalf("residentAttachKey() error = %v", err)
	}
	if got, want := key, "tmux-window:@54"; got != want {
		t.Fatalf("residentAttachKey() = %q, want %q", got, want)
	}
}

func TestResidentAttachKeyFallsBackToWorkingDirectory(t *testing.T) {
	t.Parallel()

	key, err := residentAttachKey(config{}, residentRuntime{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return "/tmp/work/dir", nil },
		tempDir:    t.TempDir,
		tmux:       runTmuxCommand,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
	})
	if err != nil {
		t.Fatalf("residentAttachKey() error = %v", err)
	}
	if got, want := key, "cwd:/tmp/work/dir"; got != want {
		t.Fatalf("residentAttachKey() = %q, want %q", got, want)
	}
}

func TestResidentPathsUseSharedPrefix(t *testing.T) {
	t.Parallel()

	paths, err := residentPathsForRuntime(config{}, residentRuntime{
		getenv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux.sock"
			}
			return ""
		},
		getwd:      func() (string, error) { return "/tmp/work", nil },
		tempDir:    func() string { return "/tmp" },
		tmux:       (&fakeTmux{sessionID: "$9", windowID: "@9"}).run,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
	})
	if err != nil {
		t.Fatalf("residentPathsForRuntime() error = %v", err)
	}
	if got, want := paths.socketPath, "/tmp/ion/"+hashedPathBase(residentPathVersionPrefix, "tmux-window:@9")+".sock"; got != want {
		t.Fatalf("socketPath = %q, want %q", got, want)
	}
	if got, want := paths.panePath, "/tmp/ion/"+hashedPathBase(residentPathVersionPrefix, "tmux-window:@9")+".pane"; got != want {
		t.Fatalf("panePath = %q, want %q", got, want)
	}
}

func TestRunResidentDownloadModeWithAcceptsDoubleColonQuitAlias(t *testing.T) {
	t.Parallel()

	base := "/tmp"
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		base = os.TempDir()
	}
	wd, err := os.MkdirTemp(base, "ion-a-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(wd)
	})

	rt := residentRuntime{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return wd, nil },
		tempDir:    func() string { return base },
		tmux:       runTmuxCommand,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
		spawn: func(cfg config, socketPath string) error {
			if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
				return err
			}
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				return err
			}
			server := transport.New(workspace.New())
			done := make(chan error, 1)
			go func() {
				done <- server.Serve(listener)
			}()
			t.Cleanup(func() {
				_ = listener.Close()
				if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
					t.Fatalf("Serve() error = %v", err)
				}
				_ = os.Remove(socketPath)
			})
			return nil
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runResidentDownloadModeWith(config{cmode: true, download: true}, strings.NewReader("::Q\n"), &stdout, &stderr, rt); err != nil {
		t.Fatalf("runResidentDownloadModeWith() error = %v", err)
	}
}

func TestDialSocketClientsInterruptCancelsRunningCommand(t *testing.T) {
	t.Parallel()

	socketPath, cleanup, err := makeSocketPath()
	if err != nil {
		t.Fatalf("makeSocketPath() error = %v", err)
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
	client, interrupt, _, stopRefresh, err := dialSocketClients(socketPath, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("dialSocketClients() error = %v", err)
	}
	defer stopRefresh()
	defer client.Close()

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
			return
		case <-ticker.C:
			if err := interrupt(); err != nil {
				t.Fatalf("interrupt() error = %v", err)
			}
		case <-deadline:
			t.Fatal("Execute(!sleep 10) did not stop after interrupt")
		}
	}
}

func TestSelectAlternateResidentSessionSkipsCurrentAndTaken(t *testing.T) {
	t.Parallel()

	sessionID, ok := selectAlternateResidentSession([]wire.SessionSummary{
		{ID: 11},
		{ID: 12, Taken: true},
		{ID: 13},
	}, 11)
	if !ok {
		t.Fatal("selectAlternateResidentSession() = false, want match")
	}
	if got, want := sessionID, uint64(13); got != want {
		t.Fatalf("sessionID = %d, want %d", got, want)
	}
}
