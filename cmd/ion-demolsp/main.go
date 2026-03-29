package main

import (
	"bufio"
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

func demoProviderDoc() wire.NamespaceProviderDoc {
	return wire.NamespaceProviderDoc{
		Namespace: demoNamespace,
		Summary:   "demo LSP commands",
		Help:      "Synthetic LSP-like commands for testing delegated namespace execution, navigation, and cancellation.",
		Commands: []wire.NamespaceCommandDoc{
			{
				Name:    "describe",
				Summary: "report the demo symbol target",
				Help:    "Reads the current view name from the caller's session and prints a synthetic symbol resolution pointing at README.md:3:1. Takes no arguments.",
			},
			{
				Name:    "goto",
				Summary: "jump to the demo target",
				Help:    "Opens README.md at line 3 in the caller's session and reports the resolved demo target. Takes no arguments.",
			},
			{
				Name:    "slow",
				Summary: "wait until interrupted",
				Help:    "Blocks until the caller interrupts the delegated invocation. Takes no arguments and is useful for testing cancellation wiring.",
			},
		},
	}
}

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
	ready, err := readyWriter(cfg.readyFD)
	if err != nil {
		fmt.Fprintf(stderr, "ion-demolsp: %v\n", err)
		return 1
	}
	if err := runForeground(cfg.socketPath, ready); err != nil {
		fmt.Fprintf(stderr, "ion-demolsp: %v\n", err)
		return 1
	}
	return 0
}

type config struct {
	socketPath string
	foreground bool
	readyFD    int
}

func parseArgs(args []string) (config, error) {
	cfg := config{}
	fs := flag.NewFlagSet("ion-demolsp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.socketPath, "socket", "", "ion server socket path")
	fs.BoolVar(&cfg.foreground, "foreground", false, "run in the foreground")
	fs.IntVar(&cfg.readyFD, "ready-fd", -1, "internal: daemon startup pipe fd")
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
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyR.Close()
	defer readyW.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "-foreground", "-socket", socketPath, "-ready-fd", "3")
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.ExtraFiles = []*os.File{readyW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = readyW.Close()
	status, err := bufio.NewReader(readyR).ReadString('\n')
	if err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("startup failed")
	}
	switch strings.TrimSpace(status) {
	case "ok":
		return cmd.Process.Release()
	default:
		_ = cmd.Wait()
		return errors.New(strings.TrimSpace(status))
	}
}

func runForeground(socketPath string, ready io.WriteCloser) (err error) {
	readySignaled := false
	defer func() {
		if !readySignaled {
			signalReady(ready, err)
		}
	}()
	client, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.RegisterNamespaceProvider(demoProviderDoc()); err != nil {
		return err
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	signalReady(ready, nil)
	readySignaled = true

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

func signalReady(w io.WriteCloser, err error) {
	if w == nil {
		return
	}
	msg := "ok\n"
	if err != nil {
		msg = err.Error() + "\n"
	}
	_, _ = io.WriteString(w, msg)
	_ = w.Close()
}

func readyWriter(fd int) (io.WriteCloser, error) {
	if fd < 0 {
		return nil, nil
	}
	return os.NewFile(uintptr(fd), "ion-demolsp-ready"), nil
}

type invocationSession interface {
	CurrentView() (wire.BufferView, error)
	Execute(script string) (bool, error)
}

type invocationController interface {
	Take(sessionID uint64) error
	Return(sessionID uint64) error
	FinishInvocation(id uint64, errText, stdout, stderr string) error
	WaitInvocationCancel(id uint64) (bool, error)
	Session(sessionID uint64) invocationSession
}

type wireInvocationController struct {
	client *clientsession.Client
}

func (c wireInvocationController) Take(sessionID uint64) error {
	return c.client.Take(sessionID)
}

func (c wireInvocationController) Return(sessionID uint64) error {
	return c.client.Return(sessionID)
}

func (c wireInvocationController) FinishInvocation(id uint64, errText, stdout, stderr string) error {
	return c.client.FinishInvocation(id, errText, stdout, stderr)
}

func (c wireInvocationController) WaitInvocationCancel(id uint64) (bool, error) {
	return c.client.WaitInvocationCancel(id)
}

func (c wireInvocationController) Session(sessionID uint64) invocationSession {
	return wireInvocationSession{session: c.client.Session(sessionID)}
}

type wireInvocationSession struct {
	session *clientsession.Session
}

func (s wireInvocationSession) CurrentView() (wire.BufferView, error) {
	return s.session.CurrentView()
}

func (s wireInvocationSession) Execute(script string) (bool, error) {
	return s.session.Execute(script)
}

func handleInvocation(client *clientsession.Client, root string, inv wire.Invocation) error {
	return runInvocation(wireInvocationController{client: client}, root, inv)
}

func runInvocation(client invocationController, root string, inv wire.Invocation) error {
	script := strings.TrimSpace(inv.Script)
	finishErr := ""
	finishStdout := ""
	finishStderr := ""

	if script == ":demolsp:slow" {
		canceled, err := client.WaitInvocationCancel(inv.ID)
		if err != nil {
			return err
		}
		if canceled {
			finishStdout = "demolsp slow cancelled\n"
		} else {
			finishErr = "demolsp slow ended unexpectedly"
		}
		return client.FinishInvocation(inv.ID, finishErr, finishStdout, finishStderr)
	}

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

	session := client.Session(inv.SessionID)
	switch script {
	case ":demolsp:describe":
		view, err := session.CurrentView()
		if err != nil {
			finishErr = err.Error()
			break
		}
		finishStdout = fmt.Sprintf("demolsp symbol demo %s -> README.md:3:1\n", filepath.Base(view.Name))
	case ":demolsp:goto":
		target := filepath.Join(root, "README.md")
		if _, err := session.Execute("B " + target + ":3\n"); err != nil {
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
