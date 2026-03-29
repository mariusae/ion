package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"

	clientsession "ion/internal/client/session"
	clienttarget "ion/internal/client/target"
	"ion/internal/client/term"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type attachRuntime struct {
	getenv  func(string) string
	getwd   func() (string, error)
	tempDir func() string
	tmux    func(args ...string) (string, error)
}

func defaultAttachRuntime() attachRuntime {
	return attachRuntime{
		getenv:  os.Getenv,
		getwd:   os.Getwd,
		tempDir: os.TempDir,
		tmux:    runTmuxCommand,
	}
}

func runAttachMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return runAttachModeWith(cfg, stdin, stdout, stderr, defaultAttachRuntime())
}

func runAttachModeWith(cfg config, stdin io.Reader, stdout, stderr io.Writer, rt attachRuntime) error {
	socketPath, err := residentAttachSocketPath(rt)
	if err != nil {
		return err
	}

	targets := clienttarget.ParseAll(cfg.files)
	capture := term.NewOutputCapture(stdout, stderr)
	ws := workspace.NewWithAutoIndent(cfg.autoindent)

	return withResidentServerSocketClients(ws, socketPath, capture.Stdout(), capture.Stderr(), func(client *clientsession.Client, interruptClient *clientsession.Client, refresh <-chan struct{}, created bool) error {
		if created {
			if err := bootstrapTargetSession(&wireBModeClient{client: client}, targets); err != nil {
				return err
			}
		} else if len(targets) > 0 {
			if err := bootstrapMissingTargets(&wireBModeClient{client: client}, targets); err != nil {
				return err
			}
		}
		if len(cfg.files) > 0 {
			if _, err := clienttarget.Open(client, cfg.files); err != nil {
				return err
			}
		}
		return term.RunBootstrapped(stdin, stdout, stderr, client, capture, term.Options{
			AutoIndent: cfg.autoindent,
			Refresh:    refresh,
			Interrupt:  interruptClient.Interrupt,
		})
	})
}

func residentAttachSocketPath(rt attachRuntime) (string, error) {
	key, err := residentAttachKey(rt)
	if err != nil {
		return "", err
	}
	return residentSocketPath(rt.tempDir(), "ion-a", key), nil
}

func residentAttachKey(rt attachRuntime) (string, error) {
	if rt.getenv != nil && rt.getenv("TMUX") != "" {
		sessionID, err := tmuxDisplay(rt.tmux, "", "#{session_id}")
		if err != nil {
			return "", err
		}
		if sessionID != "" {
			return "tmux-session:" + sessionID, nil
		}
	}
	wd, err := rt.getwd()
	if err != nil {
		return "", err
	}
	return "cwd:" + filepath.Clean(wd), nil
}

func residentSocketPath(tempDir, prefix, key string) string {
	return filepath.Join(tempDir, "ion", residentSocketBase(prefix, key)+".sock")
}

func residentSocketBase(prefix, key string) string {
	return hashedPathBase(prefix, key)
}

func withResidentServerSocketClients(ws *workspace.Workspace, socketPath string, stdout, stderr io.Writer, runClient func(*clientsession.Client, *clientsession.Client, <-chan struct{}, bool) error) error {
	client, interruptClient, err := dialSocketClients(socketPath, stdout, stderr)
	if err == nil {
		closeErr := runClient(client, interruptClient, nil, false)
		clientCloseErr := client.Close()
		interruptCloseErr := interruptClient.Close()
		if closeErr != nil {
			return closeErr
		}
		if clientCloseErr != nil {
			return clientCloseErr
		}
		return interruptCloseErr
	}
	if !isRetryableResidentDialError(err) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		client, interruptClient, dialErr := dialSocketClients(socketPath, stdout, stderr)
		if dialErr == nil {
			closeErr := runClient(client, interruptClient, nil, false)
			clientCloseErr := client.Close()
			interruptCloseErr := interruptClient.Close()
			if closeErr != nil {
				return closeErr
			}
			if clientCloseErr != nil {
				return clientCloseErr
			}
			return interruptCloseErr
		}
		if !isRetryableResidentDialError(dialErr) {
			return dialErr
		}
		if removeErr := os.Remove(socketPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		listener, err = net.Listen("unix", socketPath)
	}
	if err != nil {
		return err
	}

	refresh := make(chan struct{}, 1)
	server := transport.NewWithNotifier(ws, func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	})
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()

	client, interruptClient, err = dialSocketClients(socketPath, stdout, stderr)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		<-serverErr
		return err
	}

	clientErr := runClient(client, interruptClient, refresh, true)
	clientCloseErr := client.Close()
	interruptCloseErr := interruptClient.Close()
	listenerErr := listener.Close()
	removeErr := os.Remove(socketPath)
	serveErr := <-serverErr

	if clientErr != nil {
		return clientErr
	}
	if clientCloseErr != nil {
		return clientCloseErr
	}
	if interruptCloseErr != nil {
		return interruptCloseErr
	}
	if listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) {
		return listenerErr
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) {
		return serveErr
	}
	return nil
}

func dialSocketClients(socketPath string, stdout, stderr io.Writer) (*clientsession.Client, *clientsession.Client, error) {
	client, err := clientsession.DialUnix(socketPath, stdout, stderr)
	if err != nil {
		return nil, nil, err
	}
	interruptClient, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return client, interruptClient, nil
}

func isRetryableResidentDialError(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOTSOCK)
}

func hashedPathBase(prefix, key string) string {
	sum := sha256Sum(key)
	return fmt.Sprintf("%s-%x", prefix, sum[:8])
}

func sha256Sum(key string) [32]byte {
	return sha256.Sum256([]byte(key))
}
