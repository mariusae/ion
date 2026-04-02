package transport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clientsession "ion/internal/client/session"
	"ion/internal/proto/wire"
	"ion/internal/server/workspace"
)

func TestServeAcceptsConcurrentConnections(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	client1, err := clientsession.DialUnix(socketPath, io.Discard, os.Stderr)
	if err != nil {
		t.Fatalf("DialUnix(client1) error = %v", err)
	}
	defer client1.Close()
	if err := client1.Bootstrap(nil); err != nil {
		t.Fatalf("client1.Bootstrap() error = %v", err)
	}

	client2, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(client2) error = %v", err)
	}
	defer client2.Close()

	ready := make(chan error, 1)
	go func() {
		ready <- client2.Bootstrap([]string{"alpha.txt"})
	}()

	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("client2.Bootstrap() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client2.Bootstrap() blocked while client1 stayed connected")
	}
}

func TestServerNotifierSkipsCurrentViewButFiresOnStateChanges(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	notified := make(chan struct{}, 8)
	server := NewWithNotifier(workspace.New(), func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	client, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer client.Close()

	if err := client.Bootstrap(nil); err != nil {
		t.Fatalf("Bootstrap(nil) error = %v", err)
	}
	select {
	case <-notified:
		t.Fatal("Bootstrap(nil) triggered notifier, want no wake for no-op bootstrap")
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := client.CurrentView(); err != nil {
		t.Fatalf("CurrentView() error = %v", err)
	}
	select {
	case <-notified:
		t.Fatal("CurrentView() triggered notifier, want no wake for resident refresh reads")
	case <-time.After(100 * time.Millisecond):
	}

	if err := client.Bootstrap([]string{"alpha.txt"}); err != nil {
		t.Fatalf("Bootstrap(alpha.txt) error = %v", err)
	}
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("Bootstrap(alpha.txt) did not trigger notifier")
	}
}

func TestServerQuitCommandStopsServeLoop(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()

	client, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer client.Close()
	if err := client.Bootstrap(nil); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	cont, err := client.Execute(":ion:Q\n")
	if err != nil {
		t.Fatalf("Execute(:ion:Q) error = %v", err)
	}
	if cont {
		t.Fatal("Execute(:ion:Q) = continue, want stop")
	}

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after :ion:Q")
	}
}

func TestServerQuitCommandClosesExistingClients(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()

	owner, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(owner) error = %v", err)
	}
	defer owner.Close()
	if err := owner.Bootstrap(nil); err != nil {
		t.Fatalf("owner.Bootstrap() error = %v", err)
	}

	other, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(other) error = %v", err)
	}
	defer other.Close()
	if err := other.Bootstrap(nil); err != nil {
		t.Fatalf("other.Bootstrap() error = %v", err)
	}

	if _, err := owner.Execute(":ion:Q\n"); err != nil {
		t.Fatalf("owner.Execute(:ion:Q) error = %v", err)
	}

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after :ion:Q")
	}

	if _, err := other.CurrentView(); err == nil {
		t.Fatal("other.CurrentView() error = nil, want closed connection after :ion:Q")
	}
}

func TestServerQuitAliasStopsServeLoop(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()

	client, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer client.Close()
	if err := client.Bootstrap(nil); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	cont, err := client.Execute("Q\n")
	if err != nil {
		t.Fatalf("Execute(Q) error = %v", err)
	}
	if cont {
		t.Fatal("Execute(Q) = continue, want stop")
	}

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after Q")
	}
}

func TestServerDelegatesNamespaceGotoAndDescribe(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	start := filepath.Join(root, "start.txt")
	readme := filepath.Join(root, "README.md")
	if err := os.WriteFile(start, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(start.txt) error = %v", err)
	}
	if err := os.WriteFile(readme, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	service, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(service) error = %v", err)
	}
	defer service.Close()
	if err := service.RegisterNamespace("demolsp"); err != nil {
		t.Fatalf("RegisterNamespace() error = %v", err)
	}

	serviceErr := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			inv, err := service.WaitInvocation()
			if err != nil {
				serviceErr <- err
				return
			}
			switch inv.Script {
			case ":demolsp:describe":
				if err := service.Take(inv.SessionID); err != nil {
					serviceErr <- err
					return
				}
				view, err := service.Session(inv.SessionID).CurrentView()
				if retErr := service.Return(inv.SessionID); err == nil && retErr != nil {
					err = retErr
				}
				msg := "demolsp symbol demo " + filepath.Base(view.Name) + " -> README.md:3:1\n"
				if finishErr := service.FinishInvocation(inv.ID, errorText(err), msg, ""); finishErr != nil {
					serviceErr <- finishErr
					return
				}
			case ":demolsp:goto":
				if err := service.Take(inv.SessionID); err != nil {
					serviceErr <- err
					return
				}
				_, err := service.Session(inv.SessionID).Execute(":ion:push " + readme + ":3\n")
				if retErr := service.Return(inv.SessionID); err == nil && retErr != nil {
					err = retErr
				}
				if finishErr := service.FinishInvocation(inv.ID, errorText(err), "demolsp goto README.md:3:1\n", ""); finishErr != nil {
					serviceErr <- finishErr
					return
				}
			default:
				serviceErr <- errors.New("unexpected invocation script: " + inv.Script)
				return
			}
		}
		serviceErr <- nil
	}()

	var stdout bytes.Buffer
	caller, err := clientsession.DialUnix(socketPath, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(caller) error = %v", err)
	}
	defer caller.Close()
	if err := caller.Bootstrap([]string{start, readme}); err != nil {
		t.Fatalf("caller.Bootstrap() error = %v", err)
	}
	if _, err := caller.OpenTarget(start, ""); err != nil {
		t.Fatalf("caller.OpenTarget(start) error = %v", err)
	}

	if _, err := caller.Execute(":demolsp:describe\n"); err != nil {
		t.Fatalf("caller.Execute(:demolsp:describe) error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "demolsp symbol demo start.txt -> README.md:3:1\n") {
		t.Fatalf("stdout after describe = %q", got)
	}

	if _, err := caller.Execute(":demolsp:goto\n"); err != nil {
		t.Fatalf("caller.Execute(:demolsp:goto) error = %v", err)
	}
	view, err := caller.CurrentView()
	if err != nil {
		t.Fatalf("caller.CurrentView() error = %v", err)
	}
	if got, want := view.Name, readme; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
	selected := string([]rune(view.Text)[view.DotStart:view.DotEnd])
	if got, want := selected, "three\n"; got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
	if got := stdout.String(); !strings.Contains(got, "demolsp goto README.md:3:1\n") {
		t.Fatalf("stdout after goto = %q", got)
	}
	stack, err := caller.NavigationStack()
	if err != nil {
		t.Fatalf("caller.NavigationStack() error = %v", err)
	}
	if got, want := len(stack.Entries), 2; got != want {
		t.Fatalf("len(stack.Entries) = %d, want %d (%#v)", got, want, stack.Entries)
	}
	if got, want := stack.Current, 1; got != want {
		t.Fatalf("stack.Current = %d, want %d", got, want)
	}
	if got, want := stack.Entries[1].Label, readme+":#8,#14"; got != want {
		t.Fatalf("stack.Entries[1] = %q, want %q", got, want)
	}

	select {
	case err := <-serviceErr:
		if err != nil {
			t.Fatalf("service loop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service loop did not finish")
	}
}

func TestServerInterruptCancelsDelegatedSlowInvocation(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	service, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(service) error = %v", err)
	}
	defer service.Close()
	if err := service.RegisterNamespace("demolsp"); err != nil {
		t.Fatalf("RegisterNamespace() error = %v", err)
	}

	started := make(chan struct{}, 1)
	serviceErr := make(chan error, 1)
	go func() {
		inv, err := service.WaitInvocation()
		if err != nil {
			serviceErr <- err
			return
		}
		if got, want := inv.Script, ":demolsp:slow"; got != want {
			serviceErr <- fmt.Errorf("inv.Script = %q, want %q", got, want)
			return
		}
		started <- struct{}{}
		canceled, err := service.WaitInvocationCancel(inv.ID)
		if err != nil {
			serviceErr <- err
			return
		}
		if !canceled {
			serviceErr <- fmt.Errorf("WaitInvocationCancel(%d) = false, want true", inv.ID)
			return
		}
		serviceErr <- service.FinishInvocation(inv.ID, "", "demolsp slow cancelled\n", "")
	}()

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

	select {
	case <-started:
	case err := <-serviceErr:
		t.Fatalf("service loop error = %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("service did not receive slow invocation")
	}

	checker, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(checker) error = %v", err)
	}
	if err := checker.Bootstrap(nil); err != nil {
		t.Fatalf("checker.Bootstrap() error = %v", err)
	}
	if _, err := checker.CurrentView(); err != nil {
		t.Fatalf("checker.CurrentView() error = %v", err)
	}
	_ = checker.Close()

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

	select {
	case err := <-serviceErr:
		if err != nil {
			t.Fatalf("service loop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service loop did not finish after cancel")
	}

	if got := stdout.String(); !strings.Contains(got, "demolsp slow cancelled\n") {
		t.Fatalf("stdout after slow cancel = %q", got)
	}
}

func TestServerNamespaceHelpCommands(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	service, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(service) error = %v", err)
	}
	defer service.Close()
	if err := service.RegisterNamespaceProvider(wire.NamespaceProviderDoc{
		Namespace: "demolsp",
		Summary:   "demo LSP commands",
		Help:      "Synthetic LSP-like commands for smoke testing.",
		Commands: []wire.NamespaceCommandDoc{
			{
				Name:    "describe",
				Summary: "report the demo symbol target",
				Help:    "Reads the current view name and reports a synthetic symbol target.",
			},
			{
				Name:    "slow",
				Summary: "wait until interrupted",
				Help:    "Blocks until the caller interrupts the delegated invocation.",
			},
		},
	}); err != nil {
		t.Fatalf("RegisterNamespaceProvider() error = %v", err)
	}

	var stderr bytes.Buffer
	caller, err := clientsession.DialUnix(socketPath, io.Discard, &stderr)
	if err != nil {
		t.Fatalf("DialUnix(caller) error = %v", err)
	}
	defer caller.Close()

	docs, err := caller.NamespaceDocs()
	if err != nil {
		t.Fatalf("NamespaceDocs() error = %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("NamespaceDocs() returned no providers")
	}
	foundDemo := false
	foundTerm := false
	for _, doc := range docs {
		if doc.Namespace == "term" {
			foundTerm = true
		}
		if doc.Namespace != "demolsp" {
			continue
		}
		foundDemo = true
		if got, want := doc.Summary, "demo LSP commands"; got != want {
			t.Fatalf("demolsp summary = %q, want %q", got, want)
		}
	}
	if !foundDemo {
		t.Fatalf("NamespaceDocs() missing demolsp provider: %#v", docs)
	}
	if !foundTerm {
		t.Fatalf("NamespaceDocs() missing term provider: %#v", docs)
	}

	if _, err := caller.Execute(":ns:list\n"); err != nil {
		t.Fatalf("Execute(:ns:list) error = %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "demolsp\tdemo LSP commands\n") {
		t.Fatalf("stderr after :ns:list = %q", got)
	}

	stderr.Reset()
	if _, err := caller.Execute(":ns:show demolsp\n"); err != nil {
		t.Fatalf("Execute(:ns:show demolsp) error = %v", err)
	}
	gotShow := stderr.String()
	if !strings.Contains(gotShow, "demolsp\tdemo LSP commands\n") || !strings.Contains(gotShow, ":demolsp:describe\treport the demo symbol target\n") || !strings.Contains(gotShow, ":demolsp:slow\twait until interrupted\n") {
		t.Fatalf("stderr after :ns:show = %q", gotShow)
	}

	stderr.Reset()
	if _, err := caller.Execute(":help :ns\n"); err != nil {
		t.Fatalf("Execute(:help :ns) error = %v", err)
	}
	gotNSHelp := stderr.String()
	if !strings.Contains(gotNSHelp, ":ns\n") || !strings.Contains(gotNSHelp, "Summary: namespace discovery commands\n") || !strings.Contains(gotNSHelp, "Built-in commands for discovering registered namespaces and their documented commands.\n") || !strings.Contains(gotNSHelp, ":ns:list\tlist registered namespaces\n") || !strings.Contains(gotNSHelp, ":ns:show\tlist commands in one namespace\n") {
		t.Fatalf("stderr after :help :ns = %q", gotNSHelp)
	}

	stderr.Reset()
	if _, err := caller.Execute(":help :term\n"); err != nil {
		t.Fatalf("Execute(:help :term) error = %v", err)
	}
	gotTermHelp := stderr.String()
	if !strings.Contains(gotTermHelp, ":term\n") || !strings.Contains(gotTermHelp, "Summary: terminal HUD commands\n") || !strings.Contains(gotTermHelp, "Commands implemented locally by the interactive terminal HUD.") || !strings.Contains(gotTermHelp, ":term:write\tsave the current buffer\n") {
		t.Fatalf("stderr after :help :term = %q", gotTermHelp)
	}

	stderr.Reset()
	if _, err := caller.Execute(":help :term:write\n"); err != nil {
		t.Fatalf("Execute(:help :term:write) error = %v", err)
	}
	gotTermCommandHelp := stderr.String()
	if !strings.Contains(gotTermCommandHelp, ":term:write\n") || !strings.Contains(gotTermCommandHelp, "Summary: save the current buffer\n") {
		t.Fatalf("stderr after :help :term:write = %q", gotTermCommandHelp)
	}

	stderr.Reset()
	if _, err := caller.Execute(":help :demolsp\n"); err != nil {
		t.Fatalf("Execute(:help :demolsp) error = %v", err)
	}
	gotProviderHelp := stderr.String()
	if !strings.Contains(gotProviderHelp, ":demolsp\n") || !strings.Contains(gotProviderHelp, "Summary: demo LSP commands\n") || !strings.Contains(gotProviderHelp, "Synthetic LSP-like commands for smoke testing.\n") || !strings.Contains(gotProviderHelp, ":demolsp:describe\treport the demo symbol target\n") || !strings.Contains(gotProviderHelp, ":demolsp:slow\twait until interrupted\n") {
		t.Fatalf("stderr after :help :demolsp = %q", gotProviderHelp)
	}

	stderr.Reset()
	if _, err := caller.Execute(":help :demolsp:slow\n"); err != nil {
		t.Fatalf("Execute(:help :demolsp:slow) error = %v", err)
	}
	gotHelp := stderr.String()
	if !strings.Contains(gotHelp, ":demolsp:slow\n") || !strings.Contains(gotHelp, "Summary: wait until interrupted\n") || !strings.Contains(gotHelp, "Blocks until the caller interrupts the delegated invocation.\n") {
		t.Fatalf("stderr after :help = %q", gotHelp)
	}
}

func TestServerReportsUnknownNamespacedCommandToken(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	caller, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(caller) error = %v", err)
	}
	defer caller.Close()
	if err := caller.Bootstrap(nil); err != nil {
		t.Fatalf("caller.Bootstrap() error = %v", err)
	}

	if _, err := caller.Execute(":client\n"); err == nil || err.Error() != "unknown command `:client'" {
		t.Fatalf("Execute(:client) error = %v, want %q", err, "unknown command `:client'")
	}
}

func TestServerBuiltinNamespacedCommandDoesNotRequireTrailingNewline(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	var stderr bytes.Buffer
	caller, err := clientsession.DialUnix(socketPath, io.Discard, &stderr)
	if err != nil {
		t.Fatalf("DialUnix(caller) error = %v", err)
	}
	defer caller.Close()
	if err := caller.Bootstrap(nil); err != nil {
		t.Fatalf("caller.Bootstrap() error = %v", err)
	}

	if _, err := caller.Execute(":ns:list"); err != nil {
		t.Fatalf("Execute(:ns:list) error = %v", err)
	}
}

func TestServerMenuAddAndDeleteCommandsAffectSharedMenuSnapshot(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	caller, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(caller) error = %v", err)
	}
	defer caller.Close()
	if err := caller.Bootstrap(nil); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	if _, err := caller.Execute(":ion:menuadd :lsp:goto \"symbol\"\n"); err != nil {
		t.Fatalf("Execute(:ion:menuadd) error = %v", err)
	}
	snapshot, err := caller.MenuSnapshot()
	if err != nil {
		t.Fatalf("MenuSnapshot() error = %v", err)
	}
	if got, want := len(snapshot.Commands), 1; got != want {
		t.Fatalf("len(snapshot.Commands) = %d, want %d", got, want)
	}
	if got, want := snapshot.Commands[0].Command, ":lsp:goto"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if got, want := snapshot.Commands[0].Label, "symbol"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}

	if _, err := caller.Execute(":ion:menuadd :lsp:show\n"); err != nil {
		t.Fatalf("Execute(:ion:menuadd default label) error = %v", err)
	}
	snapshot, err = caller.MenuSnapshot()
	if err != nil {
		t.Fatalf("MenuSnapshot() second error = %v", err)
	}
	if got, want := len(snapshot.Commands), 2; got != want {
		t.Fatalf("len(snapshot.Commands) after second add = %d, want %d", got, want)
	}
	if got, want := snapshot.Commands[1].Label, ":lsp:show"; got != want {
		t.Fatalf("default label = %q, want %q", got, want)
	}

	if _, err := caller.Execute(":ion:menudel :lsp:goto\n"); err != nil {
		t.Fatalf("Execute(:ion:menudel) error = %v", err)
	}
	snapshot, err = caller.MenuSnapshot()
	if err != nil {
		t.Fatalf("MenuSnapshot() after delete error = %v", err)
	}
	if got, want := len(snapshot.Commands), 1; got != want {
		t.Fatalf("len(snapshot.Commands) after delete = %d, want %d", got, want)
	}
	if got, want := snapshot.Commands[0].Command, ":lsp:show"; got != want {
		t.Fatalf("remaining command = %q, want %q", got, want)
	}
}

func TestServerCloseSessionRemovesOwnedSession(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	client, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	summaries, err := client.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() before close error = %v", err)
	}
	if got, want := len(summaries), 1; got != want {
		t.Fatalf("len(ListSessions before close) = %d, want %d", got, want)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	summaries, err = client.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() after close error = %v", err)
	}
	if got, want := len(summaries), 0; got != want {
		t.Fatalf("len(ListSessions after close) = %d, want %d", got, want)
	}

	if _, err := client.CurrentView(); err != nil {
		t.Fatalf("CurrentView() after closing default session error = %v", err)
	}
	summaries, err = client.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() after reopening default session error = %v", err)
	}
	if got, want := len(summaries), 1; got != want {
		t.Fatalf("len(ListSessions after reopening default session) = %d, want %d", got, want)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestPrimaryClientCloseRemovesOwnedSessionsEvenWithAuxiliaryConnection(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp("", "ion-transport-*.sock")
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

	server := New(workspace.New())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve() did not return")
		}
	}()

	owner, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnix(owner) error = %v", err)
	}
	if err := owner.Bootstrap(nil); err != nil {
		t.Fatalf("owner.Bootstrap() error = %v", err)
	}
	session := owner.CurrentSession()
	if session == nil {
		t.Fatal("owner.CurrentSession() = nil")
	}

	aux, err := clientsession.DialUnixAs(socketPath, owner.ID(), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("DialUnixAs(aux) error = %v", err)
	}
	defer aux.Close()
	if _, err := aux.ListSessions(); err != nil {
		t.Fatalf("aux.ListSessions() error = %v", err)
	}

	if err := owner.Close(); err != nil {
		t.Fatalf("owner.Close() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		checker, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
		if err != nil {
			t.Fatalf("DialUnix(checker) error = %v", err)
		}
		sessions, listErr := checker.ListSessions()
		_ = checker.Close()
		if listErr != nil {
			t.Fatalf("checker.ListSessions() error = %v", listErr)
		}
		found := false
		for _, summary := range sessions {
			if summary.ID == session.ID() {
				found = true
				break
			}
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %d still present after owner close: %#v", session.ID(), sessions)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := aux.Session(session.ID()).CurrentView(); err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("aux.CurrentView() error = %v, want unknown session", err)
	}
}
