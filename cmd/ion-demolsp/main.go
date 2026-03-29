package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	clientsession "ion/internal/client/session"
	"ion/internal/proto/wire"
)

const demoNamespace = "demolsp"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "ion-demolsp: %v\n", err)
		return 2
	}
	if !cfg.foreground {
		if err := daemonize(cfg.socketPath); err != nil {
			fmt.Fprintf(stderr, "ion-demolsp: %v\n", err)
			return 1
		}
		return 0
	}
	if err := runForeground(cfg.socketPath); err != nil {
		fmt.Fprintf(stderr, "ion-demolsp: %v\n", err)
		return 1
	}
	return 0
}

type config struct {
	socketPath string
	foreground bool
}

func parseArgs(args []string) (config, error) {
	cfg := config{}
	fs := flag.NewFlagSet("ion-demolsp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.socketPath, "socket", "", "ion server socket path")
	fs.BoolVar(&cfg.foreground, "foreground", false, "run in the foreground")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.socketPath == "" {
		cfg.socketPath = strings.TrimSpace(os.Getenv("ION_SOCKET"))
	}
	if cfg.socketPath == "" {
		return config{}, fmt.Errorf("missing ion socket; run from ion or pass -socket")
	}
	if len(fs.Args()) > 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return cfg, nil
}

func daemonize(socketPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "-foreground", "-socket", socketPath)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func runForeground(socketPath string) error {
	client, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.RegisterNamespace(demoNamespace); err != nil {
		if strings.Contains(err.Error(), "already registered") {
			return nil
		}
		return err
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}

	for {
		inv, err := client.WaitInvocation()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := handleInvocation(client, root, inv); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func handleInvocation(client *clientsession.Client, root string, inv wire.Invocation) error {
	script := strings.TrimSpace(inv.Script)
	finishErr := ""
	finishStdout := ""
	finishStderr := ""

	took := false
	if err := client.Take(inv.SessionID); err != nil {
		return client.FinishInvocation(inv.ID, err.Error(), "", "")
	}
	took = true
	defer func() {
		if took {
			_ = client.Return(inv.SessionID)
		}
	}()

	switch script {
	case ":demolsp:describe":
		view, err := client.Session(inv.SessionID).CurrentView()
		if err != nil {
			finishErr = err.Error()
			break
		}
		finishStdout = fmt.Sprintf("demolsp symbol demo %s -> README.md:3:1\n", filepath.Base(view.Name))
	case ":demolsp:goto":
		target := filepath.Join(root, "README.md")
		if _, err := client.Session(inv.SessionID).Execute("B " + target + ":3\n"); err != nil {
			finishErr = err.Error()
			break
		}
		finishStdout = "demolsp goto README.md:3:1\n"
	default:
		finishErr = fmt.Sprintf("unknown demo lsp command %q", script)
	}

	if err := client.Return(inv.SessionID); err != nil && finishErr == "" {
		finishErr = err.Error()
	}
	took = false
	return client.FinishInvocation(inv.ID, finishErr, finishStdout, finishStderr)
}
