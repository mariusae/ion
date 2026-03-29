package transport

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

	clientsession "ion/internal/client/session"
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
				_, err := service.Session(inv.SessionID).Execute("B " + readme + ":3\n")
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

	select {
	case err := <-serviceErr:
		if err != nil {
			t.Fatalf("service loop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service loop did not finish")
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
