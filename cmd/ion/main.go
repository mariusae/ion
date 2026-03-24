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
	bmode    bool
	bserve   bool
	rage     bool
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

	if cfg.bserve {
		if err := runBServe(cfg, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ion: %v\n", err)
			return 1
		}
		return 0
	}

	if cfg.bmode {
		if err := runBMode(cfg, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ion: %v\n", err)
			return 1
		}
		return 0
	}

	if cfg.rage {
		if err := runRage(cfg, stdin, stdout, stderr); err != nil {
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
	fs.BoolVar(&cfg.bmode, "B", false, "reuse one ion terminal pane per tmux window")
	fs.BoolVar(&cfg.bserve, "b-serve", false, "internal: serve one tmux-window bmode pane")
	fs.BoolVar(&cfg.rage, "rage", false, "print terminal theme detection diagnostics")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.download && cfg.bmode {
		return config{}, fmt.Errorf("-B and -d cannot be combined")
	}
	if cfg.download && cfg.bserve {
		return config{}, fmt.Errorf("-b-serve and -d cannot be combined")
	}
	if cfg.rage && cfg.download {
		return config{}, fmt.Errorf("-d and -rage cannot be combined")
	}
	if cfg.rage && cfg.bmode {
		return config{}, fmt.Errorf("-B and -rage cannot be combined")
	}
	if cfg.rage && cfg.bserve {
		return config{}, fmt.Errorf("-b-serve and -rage cannot be combined")
	}
	cfg.files = fs.Args()
	if cfg.rage && len(cfg.files) > 0 {
		return config{}, fmt.Errorf("-rage does not take file arguments")
	}
	return cfg, nil
}

func runDownload(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return withLocalServer(workspace.New(), stdout, stderr, func(client *clientsession.Client) error {
		return download.Run(cfg.files, stdin, stderr, client)
	})
}

func runTerm(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	capture := term.NewOutputCapture(stdout, stderr)
	ws := workspace.New()
	return withLocalServer(ws, capture.Stdout(), capture.Stderr(), func(client *clientsession.Client) error {
		return term.Run(cfg.files, stdin, stdout, stderr, client, capture)
	})
}

func runRage(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = cfg
	_ = stderr
	return term.WriteThemeDiagnostics(stdin, stdout)
}

func withLocalServer(ws *workspace.Workspace, stdout, stderr io.Writer, runClient func(*clientsession.Client) error) error {
	server := transport.New(ws)
	socketPath, cleanup, err := makeSocketPath()
	if err != nil {
		return err
	}
	defer cleanup()
	return withServerSocket(server, socketPath, stdout, stderr, runClient)
}

func withServerSocket(server *transport.Server, socketPath string, stdout, stderr io.Writer, runClient func(*clientsession.Client) error) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
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
