package transport

import (
	"io"
	"net"
	"os"
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
