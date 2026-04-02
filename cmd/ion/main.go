package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"ion/internal/client/download"
	clientsession "ion/internal/client/session"
	"ion/internal/client/term"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type config struct {
	download   bool
	attach     bool
	nmode      bool
	bmode      bool
	cmode      bool
	bserve     bool
	serve      bool
	rage       bool
	autoindent bool
	paneID     string
	socketPath string
	files      []string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stdout, helpText())
			return 0
		}
		fmt.Fprintf(stderr, "ion: %v\n", err)
		return 2
	}

	if cfg.cmode {
		if cfg.download {
			if err := runResidentDownloadMode(cfg, stdin, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "ion: %v\n", err)
				return 1
			}
			return 0
		}
		if err := runCommandMode(cfg, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ion: %v\n", err)
			return 1
		}
		return 0
	}

	if cfg.download {
		if err := runDownload(cfg, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ion: %v\n", err)
			return 1
		}
		return 0
	}

	if cfg.attach {
		if err := runAttachMode(cfg, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ion: %v\n", err)
			return 1
		}
		return 0
	}

	if cfg.nmode {
		if err := runNewPaneMode(cfg, stdin, stdout, stderr); err != nil {
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

	if cfg.serve {
		if err := runServe(cfg, stdin, stdout, stderr); err != nil {
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
	cfg := config{autoindent: true}
	var disableAutoIndent bool

	fs := flag.NewFlagSet("ion", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&cfg.download, "d", false, "run in command-line download mode")
	fs.BoolVar(&cfg.attach, "A", false, "attach to a resident shared server")
	fs.BoolVar(&cfg.nmode, "N", false, "create a new tmux pane attached to a resident shared server")
	fs.BoolVar(&cfg.cmode, "C", false, "connect to a resident server and execute one command")
	fs.BoolVar(&disableAutoIndent, "no-autoindent", false, "turn off autoindent mode")
	fs.BoolVar(&cfg.bmode, "B", false, "reuse one ion terminal pane per tmux window")
	fs.StringVar(&cfg.paneID, "p", "", "override the tmux pane id used for -B lookup")
	fs.BoolVar(&cfg.bserve, "b-serve", false, "internal: serve one tmux-window bmode pane")
	fs.BoolVar(&cfg.serve, "serve", false, "internal: serve one resident daemon")
	fs.StringVar(&cfg.socketPath, "socket", "", "internal: socket path for resident daemon")
	fs.BoolVar(&cfg.rage, "rage", false, "print terminal theme detection diagnostics")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	cfg.autoindent = !disableAutoIndent
	cfg.files = fs.Args()
	if !cfg.attach && !cfg.nmode && !cfg.bmode && !cfg.cmode && !cfg.bserve && !cfg.serve && !cfg.rage && len(cfg.files) > 0 && looksLikeCommandScript(cfg.files[0]) {
		cfg.cmode = true
	}
	if len(cfg.files) > 0 && looksLikeCommandScript(cfg.files[0]) {
		cfg.files[0] = normalizeIonNamespaceAlias(cfg.files[0])
	}
	if cfg.download && cfg.bmode {
		return config{}, fmt.Errorf("-B and -d cannot be combined")
	}
	if cfg.download && cfg.attach {
		return config{}, fmt.Errorf("-A and -d cannot be combined")
	}
	if cfg.download && cfg.nmode {
		return config{}, fmt.Errorf("-N and -d cannot be combined")
	}
	if cfg.download && cfg.bserve {
		return config{}, fmt.Errorf("-b-serve and -d cannot be combined")
	}
	if cfg.download && cfg.serve {
		return config{}, fmt.Errorf("-serve and -d cannot be combined")
	}
	if cfg.rage && cfg.download {
		return config{}, fmt.Errorf("-d and -rage cannot be combined")
	}
	if cfg.rage && cfg.attach {
		return config{}, fmt.Errorf("-A and -rage cannot be combined")
	}
	if cfg.rage && cfg.nmode {
		return config{}, fmt.Errorf("-N and -rage cannot be combined")
	}
	if cfg.rage && cfg.cmode {
		return config{}, fmt.Errorf("-C and -rage cannot be combined")
	}
	if cfg.rage && cfg.bmode {
		return config{}, fmt.Errorf("-B and -rage cannot be combined")
	}
	if cfg.rage && cfg.bserve {
		return config{}, fmt.Errorf("-b-serve and -rage cannot be combined")
	}
	if cfg.attach && cfg.bmode {
		return config{}, fmt.Errorf("-A and -B cannot be combined")
	}
	if cfg.attach && cfg.nmode {
		return config{}, fmt.Errorf("-A and -N cannot be combined")
	}
	if cfg.attach && cfg.cmode {
		return config{}, fmt.Errorf("-A and -C cannot be combined")
	}
	if cfg.attach && cfg.bserve {
		return config{}, fmt.Errorf("-A and -b-serve cannot be combined")
	}
	if cfg.attach && cfg.serve {
		return config{}, fmt.Errorf("-A and -serve cannot be combined")
	}
	if cfg.attach && cfg.paneID != "" {
		return config{}, fmt.Errorf("-p requires -B or -N")
	}
	if cfg.nmode && cfg.bmode {
		return config{}, fmt.Errorf("-B and -N cannot be combined")
	}
	if cfg.nmode && cfg.cmode {
		return config{}, fmt.Errorf("-C and -N cannot be combined")
	}
	if cfg.nmode && cfg.bserve {
		return config{}, fmt.Errorf("-N and -b-serve cannot be combined")
	}
	if cfg.nmode && cfg.serve {
		return config{}, fmt.Errorf("-N and -serve cannot be combined")
	}
	if cfg.cmode && cfg.bmode {
		return config{}, fmt.Errorf("-B and -C cannot be combined")
	}
	if cfg.cmode && cfg.bserve {
		return config{}, fmt.Errorf("-C and -b-serve cannot be combined")
	}
	if cfg.cmode && cfg.serve {
		return config{}, fmt.Errorf("-C and -serve cannot be combined")
	}
	if cfg.cmode && cfg.paneID != "" {
		return config{}, fmt.Errorf("-p requires -B or -N")
	}
	if cfg.serve && cfg.bmode {
		return config{}, fmt.Errorf("-B and -serve cannot be combined")
	}
	if cfg.serve && cfg.bserve {
		return config{}, fmt.Errorf("-b-serve and -serve cannot be combined")
	}
	if cfg.serve && cfg.paneID != "" {
		return config{}, fmt.Errorf("-p requires -B or -N")
	}
	if cfg.serve && cfg.socketPath == "" {
		return config{}, fmt.Errorf("-serve requires -socket")
	}
	if cfg.rage && len(cfg.files) > 0 {
		return config{}, fmt.Errorf("-rage does not take file arguments")
	}
	if cfg.cmode && !cfg.download && len(cfg.files) == 0 {
		return config{}, fmt.Errorf("-C requires a command")
	}
	if cfg.serve && len(cfg.files) > 0 {
		return config{}, fmt.Errorf("-serve does not take file arguments")
	}
	return cfg, nil
}

func runDownload(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return withLocalServer(workspace.NewWithAutoIndent(cfg.autoindent), stdout, stderr, func(client *clientsession.Client) error {
		return download.Run(cfg.files, stdin, stderr, client)
	})
}

func runTerm(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return runTermWithTargets(cfg, stdin, stdout, stderr)
}

func runRage(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = cfg
	_ = stderr
	return term.WriteThemeDiagnostics(stdin, stdout)
}

func helpText() string {
	return "" +
		"usage: ion [options] [files]\n" +
		"       ion <fully-qualified-command>\n" +
		"\n" +
		"modes:\n" +
		"  -d        command-line download mode\n" +
		"  -A        attach to resident server\n" +
		"  -N        open a new tmux pane attached to resident server\n" +
		"  -B        reuse the last active session in the resident server\n" +
		"  -C        resident command mode; with -d, run resident download mode\n" +
		"  -rage     print terminal theme diagnostics\n" +
		"  -help     show this help\n" +
		"\n" +
		"notes:\n" +
		"  :: is a synonym for :ion:\n" +
		"  terminal HUD-only commands live under :term:\n" +
		"  ion :sess:list     is the same as ion -C :sess:list\n" +
		"  ion ::Q            is the same as ion -C :ion:Q\n"
}

func looksLikeCommandScript(arg string) bool {
	return strings.HasPrefix(strings.TrimSpace(arg), ":")
}

func normalizeIonNamespaceAlias(script string) string {
	trimmed := strings.TrimLeft(script, " \t")
	if !strings.HasPrefix(trimmed, "::") {
		return script
	}
	prefixLen := len(script) - len(trimmed)
	return script[:prefixLen] + ":ion:" + trimmed[2:]
}

func withLocalServer(ws *workspace.Workspace, stdout, stderr io.Writer, runClient func(*clientsession.Client) error) error {
	server := transport.New(ws)
	socketPath, cleanup, err := makeSocketPath()
	if err != nil {
		return err
	}
	defer cleanup()
	ws.SetShellEnv([]string{"ION_SOCKET=" + socketPath})
	return withServerSocket(server, socketPath, stdout, stderr, runClient)
}

func withLocalServerClients(ws *workspace.Workspace, stdout, stderr io.Writer, runClient func(*clientsession.Client, *clientsession.Session) error) error {
	server := transport.New(ws)
	socketPath, cleanup, err := makeSocketPath()
	if err != nil {
		return err
	}
	defer cleanup()
	ws.SetShellEnv([]string{"ION_SOCKET=" + socketPath})
	return withServerSocketClients(server, socketPath, stdout, stderr, runClient)
}

func withServerSocket(server *transport.Server, socketPath string, stdout, stderr io.Writer, runClient func(*clientsession.Client) error) error {
	return withServerSocketClients(server, socketPath, stdout, stderr, func(client *clientsession.Client, _ *clientsession.Session) error {
		return runClient(client)
	})
}

func withServerSocketClients(server *transport.Server, socketPath string, stdout, stderr io.Writer, runClient func(*clientsession.Client, *clientsession.Session) error) error {
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
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		_ = listener.Close()
		<-serverErr
		return err
	}
	interruptClient, err := clientsession.DialUnixAs(socketPath, client.ID(), io.Discard, io.Discard)
	if err != nil {
		_ = client.Close()
		_ = listener.Close()
		<-serverErr
		return err
	}

	clientErr := runClient(client, interruptClient.Session(session.ID()))
	closeErr := client.Close()
	interruptCloseErr := interruptClient.Close()
	listenerErr := listener.Close()
	serveErr := <-serverErr

	if clientErr != nil {
		return clientErr
	}
	if closeErr != nil {
		return closeErr
	}
	if interruptCloseErr != nil {
		return interruptCloseErr
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
