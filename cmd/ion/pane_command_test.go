package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	clientsession "ion/internal/client/session"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type paneOptionTmux struct {
	mu      sync.Mutex
	paneID  string
	options map[string]string
}

func (t *paneOptionTmux) run(args ...string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(args) == 0 {
		return "", errors.New("missing tmux command")
	}
	switch args[0] {
	case "display-message":
		return t.paneID + "\n", nil
	case "show-options":
		option := args[len(args)-1]
		value, ok := t.options[option]
		if !ok {
			return "", fmt.Errorf("unknown option %s", option)
		}
		return value + "\n", nil
	case "set-option":
		unset := false
		for _, arg := range args {
			if arg == "-u" {
				unset = true
			}
		}
		if unset {
			option := args[len(args)-1]
			delete(t.options, option)
			return "", nil
		}
		if len(args) < 2 {
			return "", errors.New("missing pane option")
		}
		t.options[args[len(args)-2]] = args[len(args)-1]
		return "", nil
	default:
		return "", fmt.Errorf("unexpected tmux command %q", args[0])
	}
}

func TestPaneCommandExecutesInPublishedSession(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "ion.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server := transport.New(workspace.New())
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	defer func() {
		_ = listener.Close()
		if err := <-serverErr; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	}()

	owner, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer owner.Close()
	session, err := owner.NewSession()
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := session.Execute("B " + file + "\n"); err != nil {
		t.Fatalf("Execute(B) error = %v", err)
	}

	tmux := &paneOptionTmux{paneID: "%9", options: make(map[string]string)}
	rt := residentRuntime{
		getenv: func(name string) string {
			switch name {
			case "TMUX":
				return "/tmp/tmux.sock"
			case "TMUX_PANE":
				return "%9"
			default:
				return ""
			}
		},
		tmux: tmux.run,
	}
	clear, err := publishPaneSession(rt, socketPath, owner)
	if err != nil {
		t.Fatalf("publishPaneSession() error = %v", err)
	}
	if got, want := tmux.options[tmuxIonSocketOption], socketPath; got != want {
		t.Fatalf("published socket = %q, want %q", got, want)
	}
	if got, want := tmux.options[tmuxIonSessionOption], strconv.FormatUint(session.ID(), 10); got != want {
		t.Fatalf("published session = %q, want %q", got, want)
	}

	var stdout, stderr bytes.Buffer
	err = runPaneCommandModeWith(config{pmode: true, paneID: "%9", files: []string{"f"}}, &stdout, &stderr, rt)
	if err != nil {
		t.Fatalf("runPaneCommandModeWith() error = %v", err)
	}
	if got := stdout.String() + stderr.String(); !strings.Contains(got, file) {
		t.Fatalf("command output = %q, want current file %q", got, file)
	}
	if _, err := session.CurrentView(); err != nil {
		t.Fatalf("owner did not regain session control: %v", err)
	}
	sessions, err := owner.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("session count = %d, want %d", got, want)
	}

	clear()
	if len(tmux.options) != 0 {
		t.Fatalf("pane options after cleanup = %#v, want empty", tmux.options)
	}
}

func TestPaneCommandReportsUnregisteredPane(t *testing.T) {
	t.Parallel()
	tmux := &paneOptionTmux{paneID: "%8", options: make(map[string]string)}
	err := runPaneCommandModeWith(
		config{pmode: true, paneID: "%8", files: []string{"f"}},
		io.Discard,
		io.Discard,
		residentRuntime{tmux: tmux.run},
	)
	if err == nil || !strings.Contains(err.Error(), "pane %8 has no active ion session") {
		t.Fatalf("runPaneCommandModeWith() error = %v, want inactive-pane error", err)
	}
}

func TestResolveTmuxPaneFallsBackFromPercentIndex(t *testing.T) {
	t.Parallel()

	var targets []string
	tmux := func(args ...string) (string, error) {
		for i := 0; i+1 < len(args); i++ {
			if args[i] != "-t" {
				continue
			}
			targets = append(targets, args[i+1])
			switch args[i+1] {
			case "%1":
				return "", errors.New("no such pane: %1")
			case "1":
				return "%366\n", nil
			}
		}
		return "", errors.New("missing target")
	}

	paneID, err := resolveTmuxPane(tmux, "%1")
	if err != nil {
		t.Fatalf("resolveTmuxPane() error = %v", err)
	}
	if got, want := paneID, "%366"; got != want {
		t.Fatalf("resolveTmuxPane() = %q, want %q", got, want)
	}
	if got, want := strings.Join(targets, ","), "%1,1"; got != want {
		t.Fatalf("tmux targets = %q, want %q", got, want)
	}
}
