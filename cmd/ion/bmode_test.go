package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	clienttarget "ion/internal/client/target"
	"ion/internal/proto/wire"
)

type fakeBModeClient struct {
	bootstrapCalls [][]string
	menuFiles      []wire.MenuFile
	openCalls      [][]string
	focusCalls     []int
	addresses      []string
	nextID         int
}

func (c *fakeBModeClient) Bootstrap(files []string) error {
	c.bootstrapCalls = append(c.bootstrapCalls, append([]string(nil), files...))
	if len(files) == 0 {
		if len(c.menuFiles) == 0 {
			c.nextID++
			c.menuFiles = append(c.menuFiles, wire.MenuFile{ID: c.nextID, Current: true})
		}
		return nil
	}
	for _, name := range files {
		seen := false
		for _, file := range c.menuFiles {
			if file.Name == name {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		c.nextID++
		c.menuFiles = append(c.menuFiles, wire.MenuFile{ID: c.nextID, Name: name})
	}
	return nil
}

func (c *fakeBModeClient) MenuFiles() ([]wire.MenuFile, error) {
	out := make([]wire.MenuFile, len(c.menuFiles))
	copy(out, c.menuFiles)
	return out, nil
}

func (c *fakeBModeClient) FocusFile(id int) (wire.BufferView, error) {
	c.focusCalls = append(c.focusCalls, id)
	for i := range c.menuFiles {
		c.menuFiles[i].Current = c.menuFiles[i].ID == id
	}
	return wire.BufferView{}, nil
}

func (c *fakeBModeClient) OpenFiles(files []string) (wire.BufferView, error) {
	c.openCalls = append(c.openCalls, append([]string(nil), files...))
	for _, name := range files {
		c.nextID++
		for i := range c.menuFiles {
			c.menuFiles[i].Current = false
		}
		c.menuFiles = append(c.menuFiles, wire.MenuFile{ID: c.nextID, Name: name, Current: true})
	}
	return wire.BufferView{}, nil
}

func (c *fakeBModeClient) SetAddress(expr string) (wire.BufferView, error) {
	c.addresses = append(c.addresses, expr)
	return wire.BufferView{}, nil
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
			if got, want := cfg.files, []string{"alpha"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("runTerm files = %#v, want %#v", got, want)
			}
			called = true
			return nil
		},
	}

	if err := runBModeWith(config{bmode: true, files: []string{"alpha"}}, nil, io.Discard, io.Discard, rt); err != nil {
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
			want := tmuxWindowPaths(tempDir, "@2").socketPath
			if path != want {
				t.Fatalf("dial path = %q, want %q", path, want)
			}
			return client, nil
		},
		tmux: func(args ...string) (string, error) {
			return tmux.run(args...)
		},
		runTerm: runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "@2")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, files: []string{"a.txt", "b.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{{"a.txt", "b.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
	if got, want := client.openCalls, [][]string(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenFiles() calls = %#v, want %#v", got, want)
	}
	if got, want := client.focusCalls, []int{2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusFile() calls = %#v, want %#v", got, want)
	}
	if len(client.addresses) != 0 {
		t.Fatalf("SetAddress() calls = %#v, want none", client.addresses)
	}
	lastTwo := tmux.calls[len(tmux.calls)-2:]
	if !reflect.DeepEqual(lastTwo, [][]string{{"select-window", "-t", "@2"}, {"select-pane", "-t", "%9"}}) {
		t.Fatalf("focus calls = %#v, want select-window/select-pane", lastTwo)
	}
}

func TestRunBModePlumbsAddressedTargetToResidentPane(t *testing.T) {
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
		dial:    func(string) (bModeClient, error) { return client, nil },
		tmux:    func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm: runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "@2")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, files: []string{"README.md:12:4"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{{"README.md"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
	if got, want := client.openCalls, [][]string(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenFiles() calls = %#v, want %#v", got, want)
	}
	if got, want := client.focusCalls, []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusFile() calls = %#v, want %#v", got, want)
	}
	if got, want := client.addresses, []string{"12+#3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetAddress() calls = %#v, want %#v", got, want)
	}
}

func TestRunBModeFocusesExistingResidentFileInsteadOfOpeningNameless(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	tmux := &fakeTmux{sessionID: "$1", windowID: "@2", splitPane: "%9"}
	client := &fakeBModeClient{
		menuFiles: []wire.MenuFile{
			{ID: 7, Name: "todo.txt", Current: true},
		},
		nextID: 7,
	}
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
		dial:    func(string) (bModeClient, error) { return client, nil },
		tmux:    func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm: runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "@2")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, files: []string{"todo.txt:/unicode"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if len(client.bootstrapCalls) != 0 {
		t.Fatalf("Bootstrap() calls = %#v, want none", client.bootstrapCalls)
	}
	if len(client.openCalls) != 0 {
		t.Fatalf("OpenFiles() calls = %#v, want none", client.openCalls)
	}
	if got, want := client.focusCalls, []int{7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusFile() calls = %#v, want %#v", got, want)
	}
	if got, want := client.addresses, []string{"/unicode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetAddress() calls = %#v, want %#v", got, want)
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
		runTerm: runTermWithTargets,
	}

	if err := runBModeWith(config{bmode: true, autoindent: true, files: []string{"a.txt", "b b.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
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

func TestRunBModeSplitPassesAutoIndentFlagWhenDisabled(t *testing.T) {
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
		tmux:    func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm: runTermWithTargets,
	}

	if err := runBModeWith(config{bmode: true, autoindent: false, files: []string{"a.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	for _, call := range tmux.calls {
		if len(call) >= 7 && call[0] == "split-window" {
			if !strings.Contains(call[6], "exec '/tmp/bin/ion' -A -b-serve -- 'a.txt'") {
				t.Fatalf("split-window command = %q, want -A propagated to b-serve", call[6])
			}
			return
		}
	}
	t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
}

func TestRunBModeBootstrapsMissingResidentFileBeforeFocus(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	tmux := &fakeTmux{sessionID: "$1", windowID: "@2", splitPane: "%9"}
	client := &fakeBModeClient{
		menuFiles: []wire.MenuFile{
			{ID: 7, Name: "todo.txt", Current: true},
		},
		nextID: 7,
	}
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
		dial:    func(string) (bModeClient, error) { return client, nil },
		tmux:    func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm: runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "@2")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, files: []string{"missing.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{{"missing.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
	if got, want := client.focusCalls, []int{8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusFile() calls = %#v, want %#v", got, want)
	}
	if len(client.openCalls) != 0 {
		t.Fatalf("OpenFiles() calls = %#v, want none", client.openCalls)
	}
}

func TestBootstrapTargetSessionKeepsMissingTargetsFocusable(t *testing.T) {
	t.Parallel()

	client := &fakeBModeClient{}
	targets := clienttarget.ParseAll([]string{"missing.txt"})

	if err := bootstrapTargetSession(client, targets); err != nil {
		t.Fatalf("bootstrapTargetSession() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{{"missing.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
	if _, err := clienttarget.Open(client, []string{"missing.txt"}); err != nil {
		t.Fatalf("clienttarget.Open() error = %v", err)
	}
	if got, want := client.focusCalls, []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusFile() calls = %#v, want %#v", got, want)
	}
	if len(client.openCalls) != 0 {
		t.Fatalf("OpenFiles() calls = %#v, want none", client.openCalls)
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

func TestParseArgsDisablesAutoIndentWithAFlag(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-A", "alpha"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if cfg.autoindent {
		t.Fatalf("config.autoindent = true, want false")
	}
	if got, want := cfg.files, []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}
