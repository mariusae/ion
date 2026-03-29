package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clientsession "ion/internal/client/session"
	"ion/internal/proto/wire"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type fakeInvocationSession struct {
	view    wire.BufferView
	scripts []string
	err     error
}

func (s *fakeInvocationSession) CurrentView() (wire.BufferView, error) {
	return s.view, s.err
}

func (s *fakeInvocationSession) Execute(script string) (bool, error) {
	s.scripts = append(s.scripts, script)
	return true, s.err
}

type fakeInvocationController struct {
	session      *fakeInvocationSession
	takeCalls    []uint64
	returnCalls  []uint64
	cancelled    bool
	finishID     uint64
	finishErr    string
	finishStdout string
	finishStderr string
}

func (c *fakeInvocationController) Take(sessionID uint64) error {
	c.takeCalls = append(c.takeCalls, sessionID)
	return nil
}

func (c *fakeInvocationController) Return(sessionID uint64) error {
	c.returnCalls = append(c.returnCalls, sessionID)
	return nil
}

func (c *fakeInvocationController) FinishInvocation(id uint64, errText, stdout, stderr string) error {
	c.finishID = id
	c.finishErr = errText
	c.finishStdout = stdout
	c.finishStderr = stderr
	return nil
}

func (c *fakeInvocationController) WaitInvocationCancel(id uint64) (bool, error) {
	c.finishID = id
	return c.cancelled, nil
}

func (c *fakeInvocationController) Session(sessionID uint64) invocationSession {
	_ = sessionID
	return c.session
}

func TestRunInvocationDescribeReportsCurrentView(t *testing.T) {
	t.Parallel()

	ctrl := &fakeInvocationController{
		session: &fakeInvocationSession{
			view: wire.BufferView{Name: "/tmp/start.txt"},
		},
	}
	inv := wire.Invocation{ID: 7, SessionID: 11, Script: ":demolsp:describe"}
	if err := runInvocation(ctrl, "/tmp/work", inv); err != nil {
		t.Fatalf("runInvocation() error = %v", err)
	}
	if got, want := ctrl.finishStdout, "demolsp symbol demo start.txt -> README.md:3:1\n"; got != want {
		t.Fatalf("finish stdout = %q, want %q", got, want)
	}
	if got, want := ctrl.takeCalls, []uint64{11}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Take() calls = %#v, want %#v", got, want)
	}
	if got, want := ctrl.returnCalls, []uint64{11}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Return() calls = %#v, want %#v", got, want)
	}
}

func TestRunInvocationGotoExecutesTargetOpen(t *testing.T) {
	t.Parallel()

	ctrl := &fakeInvocationController{
		session: &fakeInvocationSession{},
	}
	root := "/tmp/work"
	inv := wire.Invocation{ID: 7, SessionID: 11, Script: ":demolsp:goto"}
	if err := runInvocation(ctrl, root, inv); err != nil {
		t.Fatalf("runInvocation() error = %v", err)
	}
	wantScript := ":ion:push " + filepath.Join(root, "README.md") + ":3\n"
	if got, want := ctrl.session.scripts, []string{wantScript}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Execute() scripts = %#v, want %#v", got, want)
	}
	if got, want := ctrl.finishStdout, "demolsp goto README.md:3:1\n"; got != want {
		t.Fatalf("finish stdout = %q, want %q", got, want)
	}
}

func TestRunInvocationSlowExecutesLongRunningCommand(t *testing.T) {
	t.Parallel()

	ctrl := &fakeInvocationController{
		session:   &fakeInvocationSession{},
		cancelled: true,
	}
	inv := wire.Invocation{ID: 7, SessionID: 11, Script: ":demolsp:slow"}
	if err := runInvocation(ctrl, "/tmp/work", inv); err != nil {
		t.Fatalf("runInvocation() error = %v", err)
	}
	if got := ctrl.session.scripts; len(got) != 0 {
		t.Fatalf("Execute() scripts = %#v, want none", got)
	}
	if got, want := ctrl.finishStdout, "demolsp slow cancelled\n"; got != want {
		t.Fatalf("finish stdout = %q, want %q", got, want)
	}
	if got := ctrl.takeCalls; len(got) != 0 {
		t.Fatalf("Take() calls = %#v, want none", got)
	}
}

func TestRunForegroundRejectsDuplicateNamespaceRegistration(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-demolsp-*.sock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	socketPath := f.Name()
	if err := f.Close(); err != nil {
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
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer readyR.Close()

	foregroundDone := make(chan error, 1)
	go func() {
		foregroundDone <- runForeground(socketPath, readyW)
	}()

	status, err := bufio.NewReader(readyR).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString() error = %v", err)
	}
	if got, want := strings.TrimSpace(status), "ok"; got != want {
		t.Fatalf("startup status = %q, want %q", got, want)
	}

	err = runForeground(socketPath, nil)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("second runForeground() error = %v, want already registered", err)
	}

	_ = server.Shutdown()
	select {
	case err := <-foregroundDone:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("foreground run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground run did not exit after shutdown")
	}
}

func TestRunForegroundSlowInvocationCancelFinishesCaller(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	readmePath := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	f, err := os.CreateTemp("", "ion-demolsp-*.sock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	socketPath := f.Name()
	if err := f.Close(); err != nil {
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
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer readyR.Close()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir(workDir) error = %v", err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	foregroundDone := make(chan error, 1)
	go func() {
		foregroundDone <- runForeground(socketPath, readyW)
	}()

	status, err := bufio.NewReader(readyR).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString() error = %v", err)
	}
	if got, want := strings.TrimSpace(status), "ok"; got != want {
		t.Fatalf("startup status = %q, want %q", got, want)
	}

	var stdout bytes.Buffer
	caller, err := clientsession.DialUnix(socketPath, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(caller) error = %v", err)
	}
	defer caller.Close()
	if err := caller.Bootstrap(nil); err != nil {
		t.Fatalf("caller.Bootstrap() error = %v", err)
	}
	session := caller.CurrentSession()
	if session == nil {
		t.Fatal("caller.CurrentSession() = nil")
	}

	interruptClient, err := clientsession.DialUnixAs(socketPath, caller.ID(), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnixAs(interrupt) error = %v", err)
	}
	defer interruptClient.Close()
	interruptSession := interruptClient.Session(session.ID())

	execDone := make(chan error, 1)
	go func() {
		_, err := caller.Execute(":demolsp:slow\n")
		execDone <- err
	}()

	time.Sleep(150 * time.Millisecond)

	if err := interruptSession.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	select {
	case err := <-execDone:
		if err != nil {
			t.Fatalf("caller.Execute(:demolsp:slow) error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller.Execute(:demolsp:slow) did not finish after cancel")
	}

	if got := stdout.String(); !strings.Contains(got, "demolsp slow cancelled\n") {
		t.Fatalf("stdout after slow cancel = %q", got)
	}

	_ = server.Shutdown()
	select {
	case err := <-foregroundDone:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("foreground run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground run did not exit after shutdown")
	}
}

func TestDemoProviderDocIncludesCommandHelp(t *testing.T) {
	t.Parallel()

	doc := demoProviderDoc()
	if got, want := doc.Namespace, demoNamespace; got != want {
		t.Fatalf("Namespace = %q, want %q", got, want)
	}
	if got, want := len(doc.Commands), 3; got != want {
		t.Fatalf("len(Commands) = %d, want %d", got, want)
	}
	for _, cmd := range doc.Commands {
		if strings.TrimSpace(cmd.Name) == "" || strings.TrimSpace(cmd.Summary) == "" || strings.TrimSpace(cmd.Help) == "" {
			t.Fatalf("command doc = %#v, want name/summary/help populated", cmd)
		}
	}
}
