package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"ion/internal/client/download"
	clientsession "ion/internal/client/session"
	"ion/internal/client/term"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type config struct {
	download bool
	files    []string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "ion: %v\n", err)
		return 2
	}

	if cfg.download {
		if err := runDownload(cfg, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ion: %v\n", err)
			return 1
		}
		return 0
	}

	if err := runTerm(cfg, stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "ion: %v\n", err)
		return 1
	}
	return 0
}

func parseArgs(args []string) (config, error) {
	var cfg config

	fs := flag.NewFlagSet("ion", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&cfg.download, "d", false, "run in command-line download mode")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	cfg.files = fs.Args()
	return cfg, nil
}

func runDownload(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return withLocalServer(stdout, stderr, func(client *clientsession.Client) error {
		return download.Run(cfg.files, stdin, stderr, client)
	})
}

func runTerm(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	capture := term.NewOutputCapture(stdout, stderr)
	return withLocalServer(capture.Stdout(), capture.Stderr(), func(client *clientsession.Client) error {
		return term.Run(cfg.files, stdin, stdout, stderr, client, capture)
	})
}

func withLocalServer(stdout, stderr io.Writer, runClient func(*clientsession.Client) error) error {
	ws := workspace.New()
	server := transport.New(ws)

	socketPath, cleanup, err := makeSocketPath()
	if err != nil {
		return err
	}
	defer cleanup()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()

	client, err := clientsession.DialUnix(socketPath, stdout, stderr)
	if err != nil {
		_ = listener.Close()
		<-serverErr
		return err
	}

	clientErr := runClient(client)
	closeErr := client.Close()
	listenerErr := listener.Close()
	serveErr := <-serverErr

	if clientErr != nil {
		return clientErr
	}
	if closeErr != nil {
		return closeErr
	}
	if listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) {
		return listenerErr
	}
	if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) {
		return serveErr
	}
	return nil
}

func makeSocketPath() (string, func(), error) {
	dir := "/tmp"
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "ion-*.sock")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Clean(f.Name())
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	return path, func() {
		_ = os.Remove(path)
	}, nil
}
