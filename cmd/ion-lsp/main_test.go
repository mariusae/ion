package main

import (
	"bufio"
	"bytes"
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
	if got, want := len(cfg.matches), 2; got != want {
		t.Fatalf("len(matches) = %d, want %d", got, want)
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

func TestOffsetForLineColumn(t *testing.T) {
	t.Parallel()

	if got, ok := offsetForLineColumn("one\ntwo\nthree\n", 2, 1); !ok || got != 4 {
		t.Fatalf("offsetForLineColumn(line 2 col 1) = (%d, %t), want (4, true)", got, ok)
	}
	if got, ok := offsetForLineColumn("one\ntwo\nthree\n", 3, 3); !ok || got != 10 {
		t.Fatalf("offsetForLineColumn(line 3 col 3) = (%d, %t), want (10, true)", got, ok)
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
