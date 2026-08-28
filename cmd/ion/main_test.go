package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	clientsession "ion/internal/client/session"
	"ion/internal/proto/wire"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

func TestParseArgsRecognizesEditorAttachMode(t *testing.T) {
	t.Parallel()

	cfg, err := parseArgs([]string{"-E", "message.txt"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !cfg.editor || !cfg.attach {
		t.Fatalf("config = %#v, want editor attach mode", cfg)
	}
	if got, want := cfg.files, []string{"message.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}

func TestParseArgsEditorAttachRequiresFile(t *testing.T) {
	t.Parallel()

	if _, err := parseArgs([]string{"-E"}); err == nil || err.Error() != "-E requires at least one file" {
		t.Fatalf("parseArgs(-E) error = %v, want missing file error", err)
	}
}

func TestNewlyOpenedBufferIDsExcludesExistingBuffers(t *testing.T) {
	t.Parallel()

	before := []wire.BufferView{{ID: 4}, {ID: 9}}
	after := []wire.BufferView{{ID: 4}, {ID: 7}, {ID: 9}, {ID: 12}}
	if got, want := newlyOpenedBufferIDs(before, after), []int{7, 12}; !reflect.DeepEqual(got, want) {
		t.Fatalf("newlyOpenedBufferIDs() = %#v, want %#v", got, want)
	}
}

func TestCleanupEditorBuffersRemovesOnlyNewBuffers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	existing := filepath.Join(root, "existing.txt")
	opened := filepath.Join(root, "opened.txt")
	for _, path := range []string{existing, opened} {
		if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	server := transport.New(workspace.New())
	socketPath, cleanup, err := makeSocketPath()
	if err != nil {
		t.Fatalf("makeSocketPath() error = %v", err)
	}
	defer cleanup()

	err = withServerSocket(server, socketPath, io.Discard, io.Discard, func(client *clientsession.Client) error {
		if err := client.Bootstrap([]string{existing}); err != nil {
			return err
		}
		before, err := client.BufferSnapshots()
		if err != nil {
			return err
		}
		if _, err := client.OpenFiles([]string{opened}); err != nil {
			return err
		}
		after, err := client.BufferSnapshots()
		if err != nil {
			return err
		}
		ids := newlyOpenedBufferIDs(before, after)
		if got, want := len(ids), 1; got != want {
			return fmt.Errorf("new editor buffer count = %d, want %d", got, want)
		}
		if _, err := client.Replace(0, 0, "unsaved"); err != nil {
			return err
		}
		if err := cleanupEditorBuffers(socketPath, ids); err != nil {
			return err
		}
		remaining, err := client.BufferSnapshots()
		if err != nil {
			return err
		}
		if got, want := len(remaining), 1; got != want {
			return fmt.Errorf("remaining buffer count = %d, want %d", got, want)
		}
		if got, want := remaining[0].Path, existing; got != want {
			return fmt.Errorf("remaining path = %q, want %q", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWithServerSocketClientsInterruptCancelsCurrentSession(t *testing.T) {
	t.Parallel()

	server := transport.New(workspace.New())
	socketPath, cleanup, err := makeSocketPath()
	if err != nil {
		t.Fatalf("makeSocketPath() error = %v", err)
	}
	defer cleanup()

	var stdout bytes.Buffer
	err = withServerSocketClients(server, socketPath, &stdout, io.Discard, func(client *clientsession.Client, interruptSession *clientsession.Session) error {
		service, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
		if err != nil {
			return err
		}
		defer service.Close()
		if err := service.RegisterNamespace("demolsp"); err != nil {
			return err
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

		if err := client.Bootstrap(nil); err != nil {
			return err
		}

		execDone := make(chan error, 1)
		go func() {
			_, err := client.Execute(":demolsp:slow\n")
			execDone <- err
		}()

		select {
		case <-started:
		case err := <-serviceErr:
			return fmt.Errorf("service loop error: %w", err)
		case <-time.After(2 * time.Second):
			return fmt.Errorf("service did not receive slow invocation")
		}

		if err := interruptSession.Cancel(); err != nil {
			return err
		}

		select {
		case err := <-execDone:
			if err != nil {
				return fmt.Errorf("client.Execute(:demolsp:slow) error: %w", err)
			}
		case <-time.After(2 * time.Second):
			return fmt.Errorf("client.Execute(:demolsp:slow) did not finish after cancel")
		}

		select {
		case err := <-serviceErr:
			if err != nil {
				return fmt.Errorf("service loop error: %w", err)
			}
		case <-time.After(2 * time.Second):
			return fmt.Errorf("service loop did not finish after cancel")
		}

		if got := stdout.String(); !strings.Contains(got, "demolsp slow cancelled\n") {
			return fmt.Errorf("stdout after slow cancel = %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
