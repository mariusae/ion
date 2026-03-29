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
	openTargets    []clienttarget.Target
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

func (c *fakeBModeClient) OpenTarget(path, address string) (wire.BufferView, error) {
	c.openTargets = append(c.openTargets, clienttarget.Target{Path: path, Address: address})
	return wire.BufferView{}, nil
}

func (c *fakeBModeClient) SetAddress(expr string) (wire.BufferView, error) {
	c.addresses = append(c.addresses, expr)
	return wire.BufferView{}, nil
}

func (c *fakeBModeClient) Close() error { return nil }

type fakeTmux struct {
	sessionID   string
	windowID    string
	paneWindows map[string]string
	splitPane   string
	calls       [][]string
}

func (t *fakeTmux) run(args ...string) (string, error) {
	t.calls = append(t.calls, append([]string(nil), args...))
	switch args[0] {
	case "display-message":
		target := ""
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "-t" {
				target = args[i+1]
				break
			}
		}
		switch args[len(args)-1] {
		case "#{session_id}":
			return t.sessionID + "\n", nil
		case "#{window_id}":
			if target != "" && t.paneWindows != nil {
				if windowID, ok := t.paneWindows[target]; ok {
					return windowID + "\n", nil
				}
			}
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

func TestRunBModeUsesPaneOverrideForResidentLookup(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	tmux := &fakeTmux{
		sessionID: "$1",
		windowID:  "@2",
		paneWindows: map[string]string{
			"%4":  "@2",
			"%54": "@54",
		},
		splitPane: "%9",
	}
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
			want := tmuxWindowPaths(tempDir, "@54").socketPath
			if path != want {
				t.Fatalf("dial path = %q, want %q", path, want)
			}
			return client, nil
		},
		tmux:    func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm: runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "@54")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, autoindent: true, paneID: "%54", files: []string{"a.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{{"a.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
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
	if got, want := client.openTargets, []clienttarget.Target{{Path: "README.md", Address: "12+#3"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenTarget() calls = %#v, want %#v", got, want)
	}
	if len(client.focusCalls) != 0 {
		t.Fatalf("FocusFile() calls = %#v, want none", client.focusCalls)
	}
	if len(client.addresses) != 0 {
		t.Fatalf("SetAddress() calls = %#v, want none", client.addresses)
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
	if got, want := client.openTargets, []clienttarget.Target{{Path: "todo.txt", Address: "/unicode"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenTarget() calls = %#v, want %#v", got, want)
	}
	if len(client.focusCalls) != 0 {
		t.Fatalf("FocusFile() calls = %#v, want none", client.focusCalls)
	}
	if len(client.addresses) != 0 {
		t.Fatalf("SetAddress() calls = %#v, want none", client.addresses)
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
		if len(call) < 9 || call[0] != "split-window" {
			continue
		}
		foundSplit = true
		if got, want := call[1:8], []string{"-t", "%4", "-c", "/tmp/work", "-P", "-F", "#{pane_id}"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("split-window args = %#v, want %#v", got, want)
		}
		if got := call[8]; !strings.Contains(got, "exec '/tmp/bin/ion' -b-serve -- '/tmp/work/a.txt' '/tmp/work/b b.txt'") {
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
		if len(call) >= 9 && call[0] == "split-window" {
			if !strings.Contains(call[8], "exec '/tmp/bin/ion' -no-autoindent -b-serve -- '/tmp/work/a.txt'") {
				t.Fatalf("split-window command = %q, want -no-autoindent propagated to b-serve", call[8])
			}
			return
		}
	}
	t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
}

func TestRunBModeSplitPassesPaneOverride(t *testing.T) {
	t.Parallel()

	tmux := &fakeTmux{
		sessionID: "$1",
		windowID:  "@2",
		paneWindows: map[string]string{
			"%4":  "@2",
			"%54": "@54",
		},
		splitPane: "%9",
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
		getwd:      func() (string, error) { return "/tmp/work", nil },
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
		tempDir:    t.TempDir,
		dial: func(string) (bModeClient, error) {
			return nil, errors.New("dial unix: connect: no such file or directory")
		},
		tmux:    func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm: runTermWithTargets,
	}

	if err := runBModeWith(config{bmode: true, autoindent: true, paneID: "%54", files: []string{"a.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	for _, call := range tmux.calls {
		if len(call) >= 9 && call[0] == "split-window" {
			if got, want := call[1:8], []string{"-t", "%54", "-c", "/tmp/work", "-P", "-F", "#{pane_id}"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("split-window args = %#v, want %#v", got, want)
			}
			if !strings.Contains(call[8], "exec '/tmp/bin/ion' -p '%54' -b-serve -- '/tmp/work/a.txt'") {
				t.Fatalf("split-window command = %q, want -p propagated to b-serve", call[8])
			}
			return
		}
	}
	t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
}

func TestRunBModeUsesPaneOverrideWithoutTMUXPANE(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	tmux := &fakeTmux{
		sessionID: "$1",
		windowID:  "@2",
		paneWindows: map[string]string{
			"%54": "@54",
		},
		splitPane: "%9",
	}
	client := &fakeBModeClient{}
	rt := bModeRuntime{
		getenv: func(key string) string {
			switch key {
			case "TMUX":
				return "/tmp/tmux.sock"
			default:
				return ""
			}
		},
		tempDir: func() string { return tempDir },
		dial: func(path string) (bModeClient, error) {
			want := tmuxWindowPaths(tempDir, "@54").socketPath
			if path != want {
				t.Fatalf("dial path = %q, want %q", path, want)
			}
			return client, nil
		},
		tmux:    func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm: runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "@54")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, paneID: "%54", files: []string{"a.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{{"a.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
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

func TestRunBModeResolvesRelativeTargetsAgainstCallerCWDForResidentPane(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	callerWD := filepath.Join(tempDir, "dir2")
	if err := os.MkdirAll(callerWD, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
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
		getwd:   func() (string, error) { return callerWD, nil },
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

	if err := runBModeWith(config{bmode: true, files: []string{"other.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	wantPath := filepath.Join(callerWD, "other.txt")
	if got, want := client.bootstrapCalls, [][]string{{wantPath}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
	if got, want := client.focusCalls, []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusFile() calls = %#v, want %#v", got, want)
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

func TestShouldPreloadAddressedStartup(t *testing.T) {
	t.Parallel()

	if shouldPreloadAddressedStartup(nil) {
		t.Fatal("shouldPreloadAddressedStartup(nil) = true, want false")
	}
	if shouldPreloadAddressedStartup(clienttarget.ParseAll([]string{"README.md"})) {
		t.Fatal("shouldPreloadAddressedStartup(non-addressed) = true, want false")
	}
	if !shouldPreloadAddressedStartup(clienttarget.ParseAll([]string{"README.md:/one"})) {
		t.Fatal("shouldPreloadAddressedStartup(addressed final target) = false, want true")
	}
	if shouldPreloadAddressedStartup(clienttarget.ParseAll([]string{"README.md:/one", "go.mod"})) {
		t.Fatal("shouldPreloadAddressedStartup(non-addressed final target) = true, want false")
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

func TestParseArgsRecognizesAttachMode(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-A", "alpha"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.attach {
		t.Fatalf("config.attach = false, want true")
	}
	if got, want := cfg.files, []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}

func TestParseArgsDisablesAutoIndentWithLongFlag(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-no-autoindent", "alpha"})
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

func TestParseArgsRecognizesPaneOverride(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-p", "%54", "-B", "alpha"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if got, want := cfg.paneID, "%54"; got != want {
		t.Fatalf("config.paneID = %q, want %q", got, want)
	}
	if !cfg.bmode {
		t.Fatalf("config.bmode = false, want true")
	}
}
