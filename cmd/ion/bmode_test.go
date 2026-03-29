package main

import (
	"io"
	"net"
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
	sessionList    []wire.SessionSummary
	takeCalls      []uint64
	returnCalls    []uint64
	executeCalls   []struct {
		id     uint64
		script string
	}
	nextID int
}

func (c *fakeBModeClient) Bootstrap(files []string) error {
	c.bootstrapCalls = append(c.bootstrapCalls, append([]string(nil), files...))
	if len(files) == 0 && len(c.menuFiles) == 0 {
		c.nextID++
		c.menuFiles = append(c.menuFiles, wire.MenuFile{ID: c.nextID, Current: true})
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
	return wire.BufferView{}, nil
}

func (c *fakeBModeClient) OpenFiles(files []string) (wire.BufferView, error) {
	c.openCalls = append(c.openCalls, append([]string(nil), files...))
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

func (c *fakeBModeClient) ListSessions() ([]wire.SessionSummary, error) {
	return append([]wire.SessionSummary(nil), c.sessionList...), nil
}

func (c *fakeBModeClient) Take(id uint64) error {
	c.takeCalls = append(c.takeCalls, id)
	return nil
}

func (c *fakeBModeClient) Return(id uint64) error {
	c.returnCalls = append(c.returnCalls, id)
	return nil
}

func (c *fakeBModeClient) ExecuteSession(id uint64, script string) (bool, error) {
	c.executeCalls = append(c.executeCalls, struct {
		id     uint64
		script string
	}{id: id, script: script})
	return true, nil
}

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
	case "select-window", "select-pane", "send-keys":
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

func TestRunNewPaneModeFallsBackToAttachOutsideTmux(t *testing.T) {
	t.Parallel()

	called := false
	rt := bModeRuntime{
		getenv:  func(string) string { return "" },
		tempDir: t.TempDir,
		runAttach: func(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
			if got, want := cfg.files, []string{"alpha"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("runAttach files = %#v, want %#v", got, want)
			}
			called = true
			return nil
		},
	}

	if err := runNewPaneModeWith(config{nmode: true, files: []string{"alpha"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runNewPaneModeWith() error = %v", err)
	}
	if !called {
		t.Fatal("runAttach was not called outside tmux")
	}
}

func TestBootstrapAttachTargetsInitializesEmptySession(t *testing.T) {
	t.Parallel()

	client := &fakeBModeClient{}
	if err := bootstrapAttachTargets(client, nil); err != nil {
		t.Fatalf("bootstrapAttachTargets() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
}

func TestBootstrapAttachTargetsLoadsMissingFilesOnly(t *testing.T) {
	t.Parallel()

	client := &fakeBModeClient{
		menuFiles: []wire.MenuFile{{ID: 1, Name: "/tmp/already.txt", Current: true}},
	}
	targets := []clienttarget.Target{
		{Path: "/tmp/already.txt"},
		{Path: "/tmp/new.txt"},
	}
	if err := bootstrapAttachTargets(client, targets); err != nil {
		t.Fatalf("bootstrapAttachTargets() error = %v", err)
	}
	if got, want := client.bootstrapCalls, [][]string{{"/tmp/new.txt"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bootstrap() calls = %#v, want %#v", got, want)
	}
}

func TestRunBModeExecutesBCommandInResidentSession(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	tmux := &fakeTmux{
		sessionID: "$1",
		windowID:  "@2",
		paneWindows: map[string]string{
			"%9": "@7",
		},
		splitPane: "%11",
	}
	client := &fakeBModeClient{
		sessionList: []wire.SessionSummary{{ID: 77, CurrentFile: "/tmp/work/todo.txt"}},
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
		tempDir:    func() string { return tempDir },
		dial:       func(string) (bModeClient, error) { return client, nil },
		tmux:       tmux.run,
		runTerm:    runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "tmux-session:$1")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, files: []string{"a.txt", "b.txt:12"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	wantCalls := [][]string{
		{"display-message", "-p", "#{session_id}"},
		{"display-message", "-p", "-t", "%4", "#{window_id}"},
		{"display-message", "-p", "-t", "%9", "#{window_id}"},
		{"select-window", "-t", "@7"},
		{"select-pane", "-t", "%9"},
	}
	if got := tmux.calls; !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", got, wantCalls)
	}
	if got, want := client.takeCalls, []uint64{77}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Take() calls = %#v, want %#v", got, want)
	}
	if got, want := client.returnCalls, []uint64{77}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Return() calls = %#v, want %#v", got, want)
	}
	wantExec := []struct {
		id     uint64
		script string
	}{{id: 77, script: "B /tmp/work/a.txt /tmp/work/b.txt:12\n"}}
	if got := client.executeCalls; !reflect.DeepEqual(got, wantExec) {
		t.Fatalf("ExecuteSession() calls = %#v, want %#v", got, wantExec)
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
		tempDir:    func() string { return tempDir },
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
		dial: func(path string) (bModeClient, error) {
			want := tmuxWindowPaths(tempDir, "tmux-session:$1").socketPath
			if path != want {
				t.Fatalf("dial path = %q, want %q", path, want)
			}
			return &fakeBModeClient{
				sessionList: []wire.SessionSummary{{ID: 77}},
			}, nil
		},
		tmux:    tmux.run,
		runTerm: runTermWithTargets,
	}
	paths := tmuxWindowPaths(tempDir, "tmux-session:$1")
	if err := os.MkdirAll(filepath.Dir(paths.panePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := writeResidentPaneID(paths.panePath, "%9"); err != nil {
		t.Fatalf("writeResidentPaneID() error = %v", err)
	}

	if err := runBModeWith(config{bmode: true, paneID: "%54", files: []string{"a.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	if got, want := tmux.calls[0], []string{"display-message", "-p", "#{session_id}"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first tmux call = %#v, want %#v", got, want)
	}
}

func TestRunBModeSplitsNewAttachPaneWhenNoResidentExists(t *testing.T) {
	t.Parallel()

	tempDir := shortTempDir(t)
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
		tempDir:    func() string { return tempDir },
		spawn:      testResidentSpawn(t),
		dial:       func(string) (bModeClient, error) { return nil, os.ErrNotExist },
		tmux:       func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm:    runTermWithTargets,
	}

	if err := runBModeWith(config{bmode: true, autoindent: true, files: []string{"a.txt", "b b.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	for _, call := range tmux.calls {
		if len(call) >= 9 && call[0] == "split-window" {
			if got, want := call[1:8], []string{"-t", "%4", "-c", "/tmp/work", "-P", "-F", "#{pane_id}"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("split-window args = %#v, want %#v", got, want)
			}
			if got := call[8]; !strings.Contains(got, "exec '/tmp/bin/ion' -A -- '/tmp/work/a.txt' '/tmp/work/b b.txt'") {
				t.Fatalf("split-window command = %q, want attach exec", got)
			}
			return
		}
	}
	t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
}

func TestRunNewPaneModeAlwaysSplitsNewAttachPane(t *testing.T) {
	t.Parallel()

	tempDir := shortTempDir(t)
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
		tempDir:    func() string { return tempDir },
		spawn:      testResidentSpawn(t),
		tmux:       func(args ...string) (string, error) { return tmux.run(args...) },
	}

	if err := runNewPaneModeWith(config{nmode: true, autoindent: true, files: []string{"a.txt", "b b.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runNewPaneModeWith() error = %v", err)
	}
	for _, call := range tmux.calls {
		if len(call) >= 9 && call[0] == "split-window" {
			if got, want := call[1:8], []string{"-t", "%4", "-c", "/tmp/work", "-P", "-F", "#{pane_id}"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("split-window args = %#v, want %#v", got, want)
			}
			if got := call[8]; !strings.Contains(got, "exec '/tmp/bin/ion' -A -- '/tmp/work/a.txt' '/tmp/work/b b.txt'") {
				t.Fatalf("split-window command = %q, want attach exec", got)
			}
			return
		}
	}
	t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
}

func TestBuildAttachCommandPropagatesAutoIndentSetting(t *testing.T) {
	t.Parallel()

	got := buildAttachCommand("/tmp/bin/ion", config{autoindent: false, files: []string{"/tmp/work/a.txt"}})
	want := "exec '/tmp/bin/ion' -A -no-autoindent -- '/tmp/work/a.txt'"
	if got != want {
		t.Fatalf("buildAttachCommand() = %q, want %q", got, want)
	}
}

func TestRunBModeSplitPassesAutoIndentFlagWhenDisabled(t *testing.T) {
	t.Parallel()

	tempDir := shortTempDir(t)
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
		tempDir:    func() string { return tempDir },
		spawn:      testResidentSpawn(t),
		dial:       func(string) (bModeClient, error) { return nil, os.ErrNotExist },
		tmux:       func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm:    runTermWithTargets,
	}

	if err := runBModeWith(config{bmode: true, autoindent: false, files: []string{"a.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	for _, call := range tmux.calls {
		if len(call) >= 9 && call[0] == "split-window" {
			if !strings.Contains(call[8], "exec '/tmp/bin/ion' -A -no-autoindent -- '/tmp/work/a.txt'") {
				t.Fatalf("split-window command = %q, want -no-autoindent in attach exec", call[8])
			}
			return
		}
	}
	t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
}

func TestRunBModeSplitUsesPaneOverride(t *testing.T) {
	t.Parallel()

	tempDir := shortTempDir(t)
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
		tempDir:    func() string { return tempDir },
		spawn:      testResidentSpawn(t),
		dial:       func(string) (bModeClient, error) { return nil, os.ErrNotExist },
		tmux:       func(args ...string) (string, error) { return tmux.run(args...) },
		runTerm:    runTermWithTargets,
	}

	if err := runBModeWith(config{bmode: true, paneID: "%54", files: []string{"a.txt"}}, nil, io.Discard, io.Discard, rt); err != nil {
		t.Fatalf("runBModeWith() error = %v", err)
	}
	for _, call := range tmux.calls {
		if len(call) >= 9 && call[0] == "split-window" {
			if got, want := call[1:8], []string{"-t", "%54", "-c", "/tmp/work", "-P", "-F", "#{pane_id}"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("split-window args = %#v, want %#v", got, want)
			}
			return
		}
	}
	t.Fatalf("tmux calls = %#v, want split-window call", tmux.calls)
}

func TestBuildBModeScriptPreservesWhitespaceTargets(t *testing.T) {
	t.Parallel()

	if got, want := buildBModeScript([]string{"/tmp/work/a b.txt"}), "B /tmp/work/a b.txt\n"; got != want {
		t.Fatalf("buildBModeScript() = %q, want %q", got, want)
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
	if !cfg.bmode || cfg.download || cfg.bserve || cfg.cmode || cfg.nmode || cfg.serve {
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
}

func TestParseArgsRecognizesNewPaneMode(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-N", "alpha"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.nmode {
		t.Fatalf("config.nmode = false, want true")
	}
	if got, want := cfg.files, []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}

func TestParseArgsRecognizesCommandMode(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-C", "Q"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.cmode {
		t.Fatalf("config.cmode = false, want true")
	}
	if got, want := cfg.files, []string{"Q"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}

func TestParseArgsInfersCommandModeForFullyQualifiedCommand(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{":sess:list"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.cmode {
		t.Fatalf("config.cmode = false, want true")
	}
	if got, want := cfg.files, []string{":sess:list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}

func TestParseArgsNormalizesDoubleColonCommandAlias(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"::Q"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.cmode {
		t.Fatalf("config.cmode = false, want true")
	}
	if got, want := cfg.files, []string{":ion:Q"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}

func TestParseArgsAllowsResidentDownloadMode(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-C", "-d"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.cmode || !cfg.download {
		t.Fatalf("config = %#v, want cmode+download", cfg)
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

func TestParseArgsRecognizesPaneOverrideForNewPaneMode(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-p", "%54", "-N", "alpha"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if got, want := cfg.paneID, "%54"; got != want {
		t.Fatalf("config.paneID = %q, want %q", got, want)
	}
	if !cfg.nmode {
		t.Fatalf("config.nmode = false, want true")
	}
}

func testResidentSpawn(t *testing.T) func(config, string) error {
	t.Helper()

	return func(cfg config, socketPath string) error {
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
			return err
		}
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			return err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		t.Cleanup(func() {
			_ = listener.Close()
			<-done
			_ = os.Remove(socketPath)
		})
		return nil
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()

	base := "/tmp"
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "ion-b-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}
