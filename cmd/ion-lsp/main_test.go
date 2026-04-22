package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	clientsession "ion/internal/client/session"
	"ion/internal/proto/wire"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

func TestParseArgsParsesServersAndMatches(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{
		"-socket", "/tmp/ion.sock",
		"-server=go:gopls serve",
		"-server=rust:rust-analyzer",
		"-match=\\.go$:go",
		"-match=\\.rs$:rust",
	})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if got, want := cfg.servers["go"], "gopls serve"; got != want {
		t.Fatalf("go server = %q, want %q", got, want)
	}
	if got, want := cfg.servers["rust"], "rust-analyzer"; got != want {
		t.Fatalf("rust server = %q, want %q", got, want)
	}
	if got, want := len(cfg.matches), 2; got < want {
		t.Fatalf("len(matches) = %d, want at least %d", got, want)
	}
	last := cfg.matches[len(cfg.matches)-2:]
	if got, want := last[0].server, "go"; got != want {
		t.Fatalf("second-last match server = %q, want %q", got, want)
	}
	if got, want := last[1].server, "rust"; got != want {
		t.Fatalf("last match server = %q, want %q", got, want)
	}
}

func TestParseArgsIncludesDefaultServersAndMatches(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-socket", "/tmp/ion.sock"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	for name, want := range map[string]string{
		"go":     "gopls serve",
		"rust":   "rust-analyzer",
		"python": "uvx --from python-lsp-server pylsp",
		"clang":  "clangd",
	} {
		if got := cfg.servers[name]; got != want {
			t.Fatalf("server %q = %q, want %q", name, got, want)
		}
	}
	if got, want := len(cfg.matches), 4; got != want {
		t.Fatalf("len(matches) = %d, want %d", got, want)
	}
}

func TestParseArgsUserServerOverridesDefault(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{
		"-socket", "/tmp/ion.sock",
		"-server=go:custom-gopls --stdio",
	})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if got, want := cfg.servers["go"], "custom-gopls --stdio"; got != want {
		t.Fatalf("go server = %q, want %q", got, want)
	}
}

func TestMatchViewUsesLastMatchingRule(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := &lspManager{
		root: root,
		matches: []matchRule{
			{pattern: `\.h$`, re: regexp.MustCompile(`\.h$`), server: "clang"},
			{pattern: `special\.h$`, re: regexp.MustCompile(`special\.h$`), server: "go"},
		},
		servers: map[string]*lspServer{
			"clang": {name: "clang"},
			"go":    {name: "go"},
		},
	}

	path := filepath.Join(root, "pkg", "special.h")
	gotPath, gotServer, ok := manager.matchView(wire.BufferView{Name: path})
	if !ok {
		t.Fatal("matchView() ok = false, want true")
	}
	if got, want := gotPath, path; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if got, want := gotServer.name, "go"; got != want {
		t.Fatalf("server = %q, want %q", got, want)
	}
}

func TestDecodeLocationTargetLocationList(t *testing.T) {
	t.Parallel()

	target, err := decodeLocationTarget([]byte(`[{"uri":"file:///tmp/demo.go","range":{"start":{"line":2,"character":5},"end":{"line":2,"character":8}}}]`))
	if err != nil {
		t.Fatalf("decodeLocationTarget() error = %v", err)
	}
	if got, want := target.Path, "/tmp/demo.go"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if got, want := target.Line, 3; got != want {
		t.Fatalf("Line = %d, want %d", got, want)
	}
}

func TestFormatHoverResultMarkedString(t *testing.T) {
	t.Parallel()

	got := formatHoverResult([]byte(`{"contents":{"language":"go","value":"func Demo()"}}`))
	if !strings.Contains(got, "func Demo()") {
		t.Fatalf("formatHoverResult() = %q, want hover contents", got)
	}
}

func TestPositionForOffsetUsesUTF16Units(t *testing.T) {
	t.Parallel()

	pos := positionForOffset("a😀b", 2)
	if got, want := pos.Line, 0; got != want {
		t.Fatalf("Line = %d, want %d", got, want)
	}
	if got, want := pos.Character, 3; got != want {
		t.Fatalf("Character = %d, want %d", got, want)
	}
}

func TestProviderDocIncludesStatusAndLog(t *testing.T) {
	t.Parallel()

	doc := providerDoc()
	if got, want := len(doc.Commands), 8; got != want {
		t.Fatalf("len(providerDoc().Commands) = %d, want %d", got, want)
	}
	names := make([]string, 0, len(doc.Commands))
	for _, cmd := range doc.Commands {
		names = append(names, cmd.Name)
	}
	if got := strings.Join(names, ","); got != "goto,show,gototype,usage,symbol,fmt,status,log" {
		t.Fatalf("providerDoc command names = %q", got)
	}
}

func TestSplitInvocationScript(t *testing.T) {
	t.Parallel()

	command, args := splitInvocationScript(":lsp:symbol fmt.Println")
	if got, want := command, ":lsp:symbol"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if got, want := args, "fmt.Println"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}

	command, args = splitInvocationScript(":lsp:goto")
	if got, want := command, ":lsp:goto"; got != want {
		t.Fatalf("bare command = %q, want %q", got, want)
	}
	if got, want := args, ""; got != want {
		t.Fatalf("bare args = %q, want empty", got)
	}
}

func TestDecodeLocationTargetsLocationList(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[{"uri":"file:///tmp/demo.go","range":{"start":{"line":2,"character":5},"end":{"line":2,"character":8}}},{"uri":"file:///tmp/other.go","range":{"start":{"line":4,"character":1},"end":{"line":4,"character":2}}}]`)
	targets, err := decodeLocationTargets(raw)
	if err != nil {
		t.Fatalf("decodeLocationTargets() error = %v", err)
	}
	if got, want := len(targets), 2; got != want {
		t.Fatalf("len(targets) = %d, want %d", got, want)
	}
	if got, want := targets[0].Path, "/tmp/demo.go"; got != want {
		t.Fatalf("targets[0].Path = %q, want %q", got, want)
	}
	if got, want := targets[1].Line, 5; got != want {
		t.Fatalf("targets[1].Line = %d, want %d", got, want)
	}
}

func TestOffsetForLineColumn(t *testing.T) {
	t.Parallel()

	if got, ok := offsetForLineColumn("one\ntwo\nthree\n", 2, 1); !ok || got != 4 {
		t.Fatalf("offsetForLineColumn(line 2 col 1) = (%d, %t), want (4, true)", got, ok)
	}
	if got, ok := offsetForLineColumn("one\ntwo\nthree\n", 3, 3); !ok || got != 10 {
		t.Fatalf("offsetForLineColumn(line 3 col 3) = (%d, %t), want (10, true)", got, ok)
	}
}

func TestOffsetForLSPPositionUsesUTF16Units(t *testing.T) {
	t.Parallel()

	if got, ok := offsetForLSPPosition("a😀b\n", lspPosition{Line: 0, Character: 3}); !ok || got != 2 {
		t.Fatalf("offsetForLSPPosition(utf16 char 3) = (%d, %t), want (2, true)", got, ok)
	}
}

func TestApplyTextEdits(t *testing.T) {
	t.Parallel()

	got, err := applyTextEdits("package main\nfunc main(){println(\"x\")}\n", []lspTextEdit{
		{
			Range: lspRange{
				Start: lspPosition{Line: 1, Character: 11},
				End:   lspPosition{Line: 1, Character: 11},
			},
			NewText: " ",
		},
		{
			Range: lspRange{
				Start: lspPosition{Line: 1, Character: 12},
				End:   lspPosition{Line: 1, Character: 12},
			},
			NewText: " ",
		},
	})
	if err != nil {
		t.Fatalf("applyTextEdits() error = %v", err)
	}
	if want := "package main\nfunc main() { println(\"x\")}\n"; got != want {
		t.Fatalf("applyTextEdits() = %q, want %q", got, want)
	}
}

func TestFormatUsageResult(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "demo.go")
	text := "package main\n\nfunc demo() {\n\tdemo()\n}\n"
	manager := &lspManager{root: root}
	server := &lspServer{
		docs: map[string]documentState{
			pathToURI(path): {version: 1, text: text},
		},
	}
	got := formatUsageResult(manager, wire.BufferView{Name: path, Text: text}, server, []locationTarget{{
		Path:   path,
		Line:   4,
		Column: 2,
	}})
	if want := "demo.go:4:2: demo()\n"; got != want {
		t.Fatalf("formatUsageResult() = %q, want %q", got, want)
	}
}

func TestDecodeWorkspaceSymbolTargetsSymbolInformation(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[{"name":"Demo","containerName":"pkg","location":{"uri":"file:///tmp/demo.go","range":{"start":{"line":2,"character":5},"end":{"line":2,"character":8}}}}]`)
	targets, err := decodeWorkspaceSymbolTargets(raw)
	if err != nil {
		t.Fatalf("decodeWorkspaceSymbolTargets() error = %v", err)
	}
	if got, want := len(targets), 1; got != want {
		t.Fatalf("len(targets) = %d, want %d", got, want)
	}
	if got, want := targets[0].Name, "Demo"; got != want {
		t.Fatalf("targets[0].Name = %q, want %q", got, want)
	}
	if got, want := targets[0].ContainerName, "pkg"; got != want {
		t.Fatalf("targets[0].ContainerName = %q, want %q", got, want)
	}
	if !targets[0].HasLocation {
		t.Fatal("targets[0].HasLocation = false, want true")
	}
	if got, want := targets[0].Target.Path, "/tmp/demo.go"; got != want {
		t.Fatalf("targets[0].Target.Path = %q, want %q", got, want)
	}
	if got, want := targets[0].Target.Line, 3; got != want {
		t.Fatalf("targets[0].Target.Line = %d, want %d", got, want)
	}
}

func TestDecodeWorkspaceSymbolTargetsWorkspaceSymbolURIOnly(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[{"name":"Demo","location":{"uri":"file:///tmp/demo.go"}}]`)
	targets, err := decodeWorkspaceSymbolTargets(raw)
	if err != nil {
		t.Fatalf("decodeWorkspaceSymbolTargets() error = %v", err)
	}
	if got, want := len(targets), 1; got != want {
		t.Fatalf("len(targets) = %d, want %d", got, want)
	}
	if !targets[0].HasLocation {
		t.Fatal("targets[0].HasLocation = false, want true")
	}
	if got, want := targets[0].Target.Path, "/tmp/demo.go"; got != want {
		t.Fatalf("targets[0].Target.Path = %q, want %q", got, want)
	}
	if got, want := targets[0].Target.Line, 0; got != want {
		t.Fatalf("targets[0].Target.Line = %d, want %d for uri-only location", got, want)
	}
}

func TestFormatWorkspaceSymbolResult(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	got := formatWorkspaceSymbolResult(root, []workspaceSymbolTarget{
		{
			Name:          "Demo",
			ContainerName: "pkg",
			Target: locationTarget{
				Path:   filepath.Join(root, "demo.go"),
				Line:   3,
				Column: 6,
			},
			HasLocation: true,
		},
		{
			Name: "DemoNoRange",
			Target: locationTarget{
				Path: filepath.Join(root, "other.go"),
			},
			HasLocation: true,
		},
		{
			Name: "NameOnly",
		},
	})

	if want := "demo.go:3:6: pkg.Demo\nother.go: DemoNoRange\nNameOnly\n"; got != want {
		t.Fatalf("formatWorkspaceSymbolResult() = %q, want %q", got, want)
	}
}

func TestTargetAddressUsesServerDocumentText(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "demo.go")
	text := "one\ntwo\nthree\n"
	manager := &lspManager{root: root}
	server := &lspServer{
		docs: map[string]documentState{
			pathToURI(path): {version: 1, text: text},
		},
	}

	got, ok := manager.targetAddress(wire.BufferView{Name: filepath.Join(root, "other.go")}, server, locationTarget{
		Path:   path,
		Line:   2,
		Column: 3,
	})
	if !ok {
		t.Fatal("targetAddress() returned ok=false, want true")
	}
	if want := "#6"; got != want {
		t.Fatalf("targetAddress() = %q, want %q", got, want)
	}
}

func TestLSPServerStatusReport(t *testing.T) {
	t.Parallel()

	server := &lspServer{
		name:        "go",
		command:     "gopls serve",
		root:        "/tmp/demo",
		cmd:         &exec.Cmd{},
		initialized: true,
		lastStatus:  "ready",
		docs: map[string]documentState{
			"file:///tmp/demo/a.go": {version: 1, text: "package main\n"},
			"file:///tmp/demo/b.go": {version: 1, text: "package main\n"},
		},
	}

	got := server.StatusReport()
	for _, want := range []string{
		"lsp[go]\n",
		"state: running\n",
		"command: gopls serve\n",
		"root: /tmp/demo\n",
		"documents: 2\n",
		"status: ready\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("StatusReport() = %q, want substring %q", got, want)
		}
	}
}

func TestLSPServerLogReport(t *testing.T) {
	t.Parallel()

	server := &lspServer{
		name: "go",
		cmd:  &exec.Cmd{},
		logs: []serverLogEntry{
			{
				At:      time.Date(2026, time.March, 29, 12, 0, 0, 0, time.UTC),
				Source:  "stderr",
				Message: "first line",
			},
			{
				At:      time.Date(2026, time.March, 29, 12, 0, 1, 0, time.UTC),
				Source:  "window/logMessage",
				Message: "second line",
			},
		},
	}

	got := server.LogReport()
	for _, want := range []string{
		"lsp[go] log\n",
		"state: disconnected\n",
		"2026-03-29T12:00:00Z [stderr] first line\n",
		"2026-03-29T12:00:01Z [window/logMessage] second line\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("LogReport() = %q, want substring %q", got, want)
		}
	}
}

func TestLSPServerRequestContextCancelSendsCancelNotification(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	defer reader.Close()

	server := &lspServer{
		name:        "go",
		initialized: true,
		stdin:       writer,
		pending:     make(map[int64]chan rpcEnvelope),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		_, err := server.RequestContext(ctx, "textDocument/definition", map[string]any{
			"textDocument": map[string]any{"uri": "file:///tmp/demo.go"},
			"position":     lspPosition{Line: 1, Character: 2},
		}, 30*time.Second)
		resultCh <- err
	}()

	bufReader := bufio.NewReader(reader)
	request, err := readRPCEnvelope(bufReader)
	if err != nil {
		t.Fatalf("readRPCEnvelope(request) error = %v", err)
	}
	if got, want := request.Method, "textDocument/definition"; got != want {
		t.Fatalf("request method = %q, want %q", got, want)
	}
	requestID := append(json.RawMessage(nil), request.ID...)

	cancel()

	cancelEnv, err := readRPCEnvelope(bufReader)
	if err != nil {
		t.Fatalf("readRPCEnvelope(cancel) error = %v", err)
	}
	if got, want := cancelEnv.Method, "$/cancelRequest"; got != want {
		t.Fatalf("cancel method = %q, want %q", got, want)
	}
	var cancelParams struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(cancelEnv.Params, &cancelParams); err != nil {
		t.Fatalf("json.Unmarshal(cancel params) error = %v", err)
	}
	if got, want := mustRawJSON(cancelParams.ID), requestID; string(got) != string(want) {
		t.Fatalf("cancel id = %s, want %s", got, want)
	}

	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RequestContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestContext() did not return after cancel")
	}
}

func TestRunForegroundCancelSlowGotoCancelsLSPRequest(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "demo.go")
	sourceText := "package main\n\nfunc target() {}\n\nfunc call() {\n\ttarget()\n}\n"
	if err := os.WriteFile(sourcePath, []byte(sourceText), 0o644); err != nil {
		t.Fatalf("WriteFile(sourcePath) error = %v", err)
	}

	socketFile, err := os.CreateTemp("", "ion-lsp-cancel-*.sock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	socketPath := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove(socketPath) error = %v", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer os.Remove(socketPath)

	server := transport.New(workspace.New())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Shutdown()
		_ = listener.Close()
	}()

	startFile := filepath.Join(root, "slowlsp-started")
	cancelFile := filepath.Join(root, "slowlsp-canceled")
	helperPath := buildSlowLSPHelper(t, root, startFile, cancelFile)

	cfg := config{
		socketPath: socketPath,
		foreground: true,
		servers: map[string]string{
			"go": fmt.Sprintf("%q", helperPath),
		},
		matches: []matchRule{{
			pattern: `\.go$`,
			re:      regexp.MustCompile(`\.go$`),
			server:  "go",
		}},
	}

	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer readyR.Close()

	foregroundDone := make(chan error, 1)
	go func() {
		foregroundDone <- runForeground(cfg, readyW)
	}()

	status, err := bufio.NewReader(readyR).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString() error = %v", err)
	}
	if got, want := strings.TrimSpace(status), "ok"; got != want {
		t.Fatalf("startup status = %q, want %q", got, want)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	caller, err := clientsession.DialUnix(socketPath, &stdout, &stderr)
	if err != nil {
		t.Fatalf("DialUnix(caller) error = %v", err)
	}
	defer caller.Close()

	if err := caller.Bootstrap([]string{sourcePath}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if err := waitForMenuCommands(caller, len(lspMenuCommands), 5*time.Second); err != nil {
		t.Fatal(err)
	}

	view, err := caller.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() before goto error = %v", err)
	}
	offset := strings.LastIndex(view.Text, "target()")
	if offset < 0 {
		t.Fatal("did not find target() call in test source")
	}
	if _, err := caller.SetDot(offset, offset); err != nil {
		t.Fatalf("SetDot(target call) error = %v", err)
	}

	session := caller.CurrentSession()
	if session == nil {
		t.Fatal("CurrentSession() = nil, want active session")
	}
	interruptClient, err := clientsession.DialUnixAs(socketPath, caller.ID(), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnixAs(interrupt) error = %v", err)
	}
	defer interruptClient.Close()
	interruptSession := interruptClient.Session(session.ID())
	if interruptSession == nil {
		t.Fatal("interrupt session = nil, want session handle")
	}

	stdout.Reset()
	stderr.Reset()
	execDone := make(chan error, 1)
	go func() {
		_, err := caller.Execute(":lsp:goto\n")
		execDone <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(startFile); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("Stat(startFile) error = %v", err)
		}
		select {
		case err := <-execDone:
			t.Fatalf("Execute(:lsp:goto) returned before slow request start: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\nstdout=%s\nstderr=%s", startFile, stdout.String(), stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := interruptSession.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	select {
	case err := <-execDone:
		if err != nil {
			t.Fatalf("Execute(:lsp:goto) error = %v\nstderr=%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute(:lsp:goto) did not return after cancel")
	}

	if err := waitForFile(cancelFile, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	view, err = caller.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after cancel error = %v", err)
	}
	if got, want := view.Name, sourcePath; got != want {
		t.Fatalf("view.Name after cancel = %q, want %q", got, want)
	}
	if got, want := stdout.String(), ""; got != want {
		t.Fatalf("stdout after cancel = %q, want empty output", got)
	}
	if got, want := stderr.String(), ""; got != want {
		t.Fatalf("stderr after cancel = %q, want empty output", got)
	}

	_ = server.Shutdown()
	select {
	case err := <-foregroundDone:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("runForeground() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runForeground() did not exit")
	}
	select {
	case err := <-serverDone:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not return")
	}
}

func TestRunForegroundWithGoplsSmoke(t *testing.T) {
	if os.Getenv("ION_LSP_SMOKE") == "" {
		t.Skip("set ION_LSP_SMOKE=1 to run the live gopls smoke test")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Abs(repo root) error = %v", err)
	}
	socketFile, err := os.CreateTemp("", "ion-lsp-*.sock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	socketPath := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove() error = %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer os.Remove(socketPath)

	server := transport.New(workspace.New())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Shutdown()
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	cfg := config{
		socketPath: socketPath,
		foreground: true,
		servers: map[string]string{
			"go": "gopls serve",
		},
		matches: []matchRule{{
			pattern: `\.go$`,
			re:      regexp.MustCompile(`\.go$`),
			server:  "go",
		}},
	}

	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer readyR.Close()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir(root) error = %v", err)
	}
	defer func() {
		_ = os.Chdir(oldWD)
	}()

	foregroundDone := make(chan error, 1)
	go func() {
		foregroundDone <- runForeground(cfg, readyW)
	}()

	status, err := bufio.NewReader(readyR).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString() error = %v", err)
	}
	if got, want := strings.TrimSpace(status), "ok"; got != want {
		t.Fatalf("startup status = %q, want %q", got, want)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	caller, err := clientsession.DialUnix(socketPath, &stdout, &stderr)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer caller.Close()

	mainGo := filepath.Join(root, "cmd/ion/main.go")
	attachGo := filepath.Join(root, "cmd/ion/attach.go")
	if err := caller.Bootstrap([]string{mainGo, attachGo}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if err := waitForMenuCommands(caller, len(lspMenuCommands), 5*time.Second); err != nil {
		t.Fatal(err)
	}

	if _, err := caller.SetAddress("/runServe/"); err != nil {
		t.Fatalf("SetAddress(/runServe/) error = %v", err)
	}
	if _, err := caller.Execute(":lsp:goto\n"); err != nil {
		t.Fatalf("Execute(:lsp:goto) error = %v\nstderr=%s", err, stderr.String())
	}
	view, err := caller.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after goto error = %v", err)
	}
	if got, want := view.Name, attachGo; got != want {
		t.Fatalf("view.Name after goto = %q, want %q", got, want)
	}
	assertNavigationMatchesView(t, caller, view)
	if got := stdout.String(); got != "" {
		t.Fatalf("goto stdout = %q, want empty output", got)
	}

	if _, err := caller.OpenTarget(mainGo, ""); err != nil {
		t.Fatalf("OpenTarget(main.go) error = %v", err)
	}
	if _, err := caller.SetAddress("/runServe/"); err != nil {
		t.Fatalf("SetAddress(/runServe/) before hover error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if _, err := caller.Execute(":lsp:show\n"); err != nil {
		t.Fatalf("Execute(:lsp:show) error = %v\nstderr=%s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "runServe") {
		t.Fatalf("hover stdout = %q, want runServe hover text", got)
	}

	if _, err := caller.OpenTarget(mainGo, ""); err != nil {
		t.Fatalf("OpenTarget(main.go) before gototype error = %v", err)
	}
	view, err = caller.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() before gototype error = %v", err)
	}
	offset := strings.Index(view.Text, "cfg, err := parseArgs")
	if offset < 0 {
		t.Fatal("did not find cfg assignment in main.go")
	}
	if _, err := caller.SetDot(offset, offset); err != nil {
		t.Fatalf("SetDot(cfg assignment) error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if _, err := caller.Execute(":lsp:gototype\n"); err != nil {
		t.Fatalf("Execute(:lsp:gototype) error = %v\nstderr=%s", err, stderr.String())
	}
	view, err = caller.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after gototype error = %v", err)
	}
	if got, want := view.Name, mainGo; got != want {
		t.Fatalf("view.Name after gototype = %q, want %q", got, want)
	}
	assertNavigationMatchesView(t, caller, view)
	if got := stdout.String(); got != "" {
		t.Fatalf("gototype stdout = %q, want empty output", got)
	}

	if _, err := caller.OpenTarget(mainGo, ""); err != nil {
		t.Fatalf("OpenTarget(main.go) before usage error = %v", err)
	}
	if _, err := caller.SetAddress("/runServe/"); err != nil {
		t.Fatalf("SetAddress(/runServe/) before usage error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if _, err := caller.Execute(":lsp:usage\n"); err != nil {
		t.Fatalf("Execute(:lsp:usage) error = %v\nstderr=%s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "cmd/ion/main.go:") {
		t.Fatalf("usage stdout = %q, want location list", got)
	}

	fmtPath := filepath.Join(root, "zz_ion_lsp_fmt_test.go")
	if err := os.WriteFile(fmtPath, []byte("package main\nfunc bad( ){println(\"x\")}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(fmtPath) error = %v", err)
	}
	defer os.Remove(fmtPath)
	if _, err := caller.OpenTarget(fmtPath, ""); err != nil {
		t.Fatalf("OpenTarget(fmtPath) error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if _, err := caller.Execute(":lsp:fmt\n"); err != nil {
		t.Fatalf("Execute(:lsp:fmt) error = %v\nstderr=%s", err, stderr.String())
	}
	view, err = caller.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after fmt error = %v", err)
	}
	if got, want := view.Text, "package main\n\nfunc bad() { println(\"x\") }\n"; got != want {
		t.Fatalf("view.Text after fmt = %q, want %q", got, want)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("fmt stdout = %q, want empty output", got)
	}

	_ = server.Shutdown()
	select {
	case err := <-foregroundDone:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("runForeground() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runForeground() did not exit")
	}
}

func waitForMenuCommands(client *clientsession.Client, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := client.MenuSnapshot()
		if err != nil {
			return err
		}
		if len(snapshot.Commands) >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for lsp menu items")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertNavigationMatchesView(t *testing.T, client *clientsession.Client, view wire.BufferView) {
	t.Helper()

	nav, err := client.NavigationStack()
	if err != nil {
		t.Fatalf("NavigationStack() error = %v", err)
	}
	if nav.Current < 0 || nav.Current >= len(nav.Entries) {
		t.Fatalf("NavigationStack current index = %d, entries=%d", nav.Current, len(nav.Entries))
	}
	want := fmt.Sprintf("%s:#%d", view.Name, view.DotStart)
	if view.DotStart != view.DotEnd {
		want = fmt.Sprintf("%s:#%d,#%d", view.Name, view.DotStart, view.DotEnd)
	}
	if got := nav.Entries[nav.Current].Label; got != want {
		t.Fatalf("NavigationStack current label = %q, want %q", got, want)
	}
}

func buildSlowLSPHelper(t *testing.T, dir, startFile, cancelFile string) string {
	t.Helper()

	source := fmt.Sprintf(`package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type rpcEnvelope struct {
	JSONRPC string          %[1]sjson:"jsonrpc"%[1]s
	ID      json.RawMessage %[1]sjson:"id,omitempty"%[1]s
	Method  string          %[1]sjson:"method,omitempty"%[1]s
	Params  json.RawMessage %[1]sjson:"params,omitempty"%[1]s
	Result  json.RawMessage %[1]sjson:"result,omitempty"%[1]s
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	var pendingID int64
	for {
		env, err := readRPCEnvelope(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		switch env.Method {
		case "initialize":
			if err := writeRPCEnvelope(writer, rpcEnvelope{
				JSONRPC: "2.0",
				ID:      env.ID,
				Result: mustMarshalJSON(map[string]any{
					"capabilities": map[string]any{
						"definitionProvider": true,
					},
				}),
			}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		case "initialized", "textDocument/didOpen", "textDocument/didChange", "textDocument/didClose":
		case "shutdown":
			if err := writeRPCEnvelope(writer, rpcEnvelope{
				JSONRPC: "2.0",
				ID:      env.ID,
				Result:  mustMarshalJSON(nil),
			}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		case "exit":
			return
		case "textDocument/definition":
			id, ok := parseRPCID(env.ID)
			if !ok {
				fmt.Fprintln(os.Stderr, "missing request id for definition")
				os.Exit(1)
			}
			pendingID = id
			if err := os.WriteFile(%[2]q, []byte("started\n"), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		case "$/cancelRequest":
			var params struct {
				ID int64 %[1]sjson:"id"%[1]s
			}
			if err := json.Unmarshal(env.Params, &params); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			if pendingID != 0 && params.ID == pendingID {
				if err := os.WriteFile(%[3]q, []byte("canceled\n"), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				if err := writeRPCEnvelope(writer, rpcEnvelope{
					JSONRPC: "2.0",
					ID:      mustRawJSON(pendingID),
					Result: mustMarshalJSON([]map[string]any{{
						"uri": "file://" + filepath.Join(filepath.Dir(%[2]q), "ignored.go"),
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
							"end":   map[string]any{"line": 0, "character": 0},
						},
					}}),
				}); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				pendingID = 0
			}
		default:
			if len(env.ID) != 0 {
				if err := writeRPCEnvelope(writer, rpcEnvelope{
					JSONRPC: "2.0",
					ID:      env.ID,
					Result:  mustMarshalJSON(nil),
				}); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			}
		}
	}
}

func readRPCEnvelope(reader *bufio.Reader) (rpcEnvelope, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return rpcEnvelope{}, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			n, err := strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
			if err != nil {
				return rpcEnvelope{}, err
			}
			contentLength = n
		}
	}
	if contentLength <= 0 {
		return rpcEnvelope{}, fmt.Errorf("bad content length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return rpcEnvelope{}, err
	}
	var env rpcEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return rpcEnvelope{}, err
	}
	return env, nil
}

func writeRPCEnvelope(writer *bufio.Writer, env rpcEnvelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %%d\r\n\r\n", len(body)); err != nil {
		return err
	}
	if _, err := writer.Write(body); err != nil {
		return err
	}
	return writer.Flush()
}

func parseRPCID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, false
	}
	return id, true
}

func mustRawJSON(v int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(v, 10))
}

func mustMarshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
`, "`", startFile, cancelFile)

	sourcePath := filepath.Join(dir, "slowlsp_helper.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile(helper source) error = %v", err)
	}
	binaryPath := filepath.Join(dir, "slowlsp-helper")
	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build helper error = %v\n%s", err, output)
	}
	return binaryPath
}
