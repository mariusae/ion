package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	clientsession "ion/internal/client/session"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

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
