package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeBModeClient struct {
	openCalls [][]string
}

func (c *fakeBModeClient) OpenFiles(files []string) error {
	c.openCalls = append(c.openCalls, append([]string(nil), files...))
	return nil
}

func (c *fakeBModeClient) Close() error { return nil }

type fakeTmux struct {
	sessionID string
	windowID  string
	splitPane string
	calls     [][]string
}

func (t *fakeTmux) run(args ...string) (string, error) {
	t.calls = append(t.calls, append([]string(nil), args...))
	switch args[0] {
	case "display-message":
		switch args[len(args)-1] {
		case "#{session_id}":
			return t.sessionID + "\n", nil
		case "#{window_id}":
			return t.windowID + "\n", nil
		}
	case "split-window":
		return t.splitPane + "\n", nil
	case "select-window", "select-pane":
		return "", nil
	}
	return "", nil
}

func TestRunBModeFallsBackToTerminalOutsideTmux(t *testing.T) {
	t.Parallel()

	called := false
	rt := bModeRuntime{
		getenv:  func(string) string { return "" },
		tempDir: t.TempDir,
		runTerm: func(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
			called = true
			return nil
		},
	}

	if err := runBModeWith(config{bmode: true}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if !called {
		t.Fatal("runTerm was not called outside tmux")
	}
}

func TestRunBModePlumbsToResidentPane(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	tmux := &fakeTmux{sessionID: "$1", windowID: "@2", splitPane: "%9"}
	client := &fakeBModeClient{}
	rt := bModeRuntime{
		getenv: func(key string) string {
			switch key {
			case "TMUX":
				return "/tmp/tmux.sock"
			case "TMUX_PANE":
				return "%4"
			default:
				return ""
			}
		},
		tempDir: func() string { return tempDir },
		dial: func(path string) (bModeClient, error) {
			want := tmuxWindowPaths(tempDir, "$1.@2").socketPath
			if path != want {
				t.Fatalf("dial path = %q, want %q", path, want)
			}
			return client, nil
		},
		tmux: func(args ...string) (string, error) {
			return tmux.run(args...)
		},
		runTerm: runTerm,
	}
	paths := tmuxWindowPaths(tempDir, "$1.@2")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, files: []string{"a.txt", "b.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if got, want := client.openCalls, [][]string{{"a.txt", "b.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenFiles() calls = %#v, want %#v", got, want)
	}
	lastTwo := tmux.calls[len(tmux.calls)-2:]
	if !reflect.DeepEqual(lastTwo, [][]string{{"select-window", "-t", "@2"}, {"select-pane", "-t", "%9"}}) {
		t.Fatalf("focus calls = %#v, want select-window/select-pane", lastTwo)
	}
}

func TestRunBModeSplitsNewPaneWhenNoResidentExists(t *testing.T) {
	t.Parallel()

	tmux := &fakeTmux{sessionID: "$1", windowID: "@2", splitPane: "%9"}
	rt := bModeRuntime{
		getenv: func(key string) string {
			switch key {
			case "TMUX":
				return "/tmp/tmux.sock"
			case "TMUX_PANE":
				return "%4"
			default:
				return ""
			}
		},
		getwd:      func() (string, error) { return "/tmp/work", nil },
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
		tempDir:    t.TempDir,
		dial: func(string) (bModeClient, error) {
			return nil, errors.New("dial unix: connect: no such file or directory")
		},
		tmux: func(args ...string) (string, error) {
			return tmux.run(args...)
		},
		runTerm: runTerm,
	}

	if err := runBModeWith(config{bmode: true, files: []string{"a.txt", "b b.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	foundSplit := false
	for _, call := range tmux.calls {
		if len(call) < 7 || call[0] != "split-window" {
			continue
		}
		foundSplit = true
		if got, want := call[1:6], []string{"-c", "/tmp/work", "-P", "-F", "#{pane_id}"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("split-window args = %#v, want %#v", got, want)
		}
		if got := call[6]; !strings.Contains(got, "exec '/tmp/bin/ion' -b-serve -- 'a.txt' 'b b.txt'") {
			t.Fatalf("split-window command = %q, want hidden b-serve exec", got)
		}
	}
	if !foundSplit {
		t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
	}
}

func TestParseArgsRecognizesBMode(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-B", "alpha"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.bmode || cfg.download || cfg.bserve {
		t.Fatalf("config = %#v, want bmode only", cfg)
	}
	if got, want := cfg.files, []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}
