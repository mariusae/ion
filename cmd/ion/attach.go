package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	clientsession "ion/internal/client/session"
	clienttarget "ion/internal/client/target"
	"ion/internal/client/term"
	"ion/internal/proto/wire"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type residentRuntime struct {
	getenv     func(string) string
	getwd      func() (string, error)
	tempDir    func() string
	tmux       func(args ...string) (string, error)
	executable func() (string, error)
	spawn      func(config, string) error
}

type residentPaths struct {
	socketPath string
	panePath   string
}

const residentPathVersionPrefix = "ion-r3"

func defaultResidentRuntime() residentRuntime {
	return residentRuntime{
		getenv:     os.Getenv,
		getwd:      os.Getwd,
		tempDir:    os.TempDir,
		tmux:       runTmuxCommand,
		executable: os.Executable,
	}
}

func runAttachMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return runAttachModeWith(cfg, stdin, stdout, stderr, defaultResidentRuntime())
}

func runAttachModeWith(cfg config, stdin io.Reader, stdout, stderr io.Writer, rt residentRuntime) error {
	paths, err := ensureResidentServer(cfg, rt)
	if err != nil {
		return err
	}

	targets := clienttarget.ParseAll(cfg.files)
	capture := term.NewOutputCapture(stdout, stderr)
	client, interrupt, refresh, stopRefresh, err := dialSocketClients(paths.socketPath, capture.Stdout(), capture.Stderr())
	if err != nil {
		return err
	}
	defer stopRefresh()
	defer client.Close()

	service := wrapInteractiveClient(rt, paths, client)
	if len(targets) > 0 {
		if err := bootstrapMissingTargets(&wireBModeClient{client: client}, targets); err != nil {
			return err
		}
		if _, err := clienttarget.Open(service, cfg.files); err != nil {
			return err
		}
	}

	return term.RunBootstrapped(stdin, stdout, stderr, service, capture, term.Options{
		AutoIndent: cfg.autoindent,
		Refresh:    refresh,
		Interrupt:  interrupt,
	})
}

func runCommandMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	return runCommandModeWith(cfg, stdout, stderr, defaultResidentRuntime())
}

func runCommandModeWith(cfg config, stdout, stderr io.Writer, rt residentRuntime) error {
	paths, err := ensureResidentServer(cfg, rt)
	if err != nil {
		return err
	}
	client, err := clientsession.DialUnix(paths.socketPath, stdout, stderr)
	if err != nil {
		return err
	}
	defer client.Close()
	script := strings.Join(cfg.files, " ")
	if !strings.HasSuffix(script, "\n") {
		script += "\n"
	}
	_, err = client.Execute(script)
	return err
}

func runServe(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	_ = stdout
	_ = stderr

	ws := workspace.NewWithAutoIndent(cfg.autoindent)
	ws.SetShellEnv([]string{"ION_SOCKET=" + cfg.socketPath})
	server := transport.New(ws)
	if err := os.MkdirAll(filepath.Dir(cfg.socketPath), 0o700); err != nil {
		return err
	}
	if err := os.Remove(cfg.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", cfg.socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(cfg.socketPath)
	}()
	return server.Serve(listener)
}

func residentAttachKey(rt residentRuntime) (string, error) {
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

func residentPathsForRuntime(rt residentRuntime) (residentPaths, error) {
	key, err := residentAttachKey(rt)
	if err != nil {
		return residentPaths{}, err
	}
	base := hashedPathBase(residentPathVersionPrefix, key)
	dir := filepath.Join(rt.tempDir(), "ion")
	return residentPaths{
		socketPath: filepath.Join(dir, base+".sock"),
		panePath:   filepath.Join(dir, base+".pane"),
	}, nil
}

func ensureResidentServer(cfg config, rt residentRuntime) (residentPaths, error) {
	paths, err := residentPathsForRuntime(rt)
	if err != nil {
		return residentPaths{}, err
	}
	if err := probeResidentServer(paths.socketPath); err == nil {
		return paths, nil
	} else if !isRetryableResidentDialError(err) {
		return residentPaths{}, err
	}
	if err := spawnResidentServer(cfg, rt, paths.socketPath); err != nil {
		return residentPaths{}, err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := probeResidentServer(paths.socketPath)
		if err == nil {
			return paths, nil
		}
		if time.Now().After(deadline) {
			return residentPaths{}, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func probeResidentServer(socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return err
	}
	return conn.Close()
}

func spawnResidentServer(cfg config, rt residentRuntime, socketPath string) error {
	if rt.spawn != nil {
		return rt.spawn(cfg, socketPath)
	}
	exe, err := rt.executable()
	if err != nil {
		return err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	args := []string{"-serve", "-socket", socketPath}
	if !cfg.autoindent {
		args = append(args, "-no-autoindent")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func dialSocketClients(socketPath string, stdout, stderr io.Writer) (*clientsession.Client, func() error, <-chan struct{}, func(), error) {
	client, err := clientsession.DialUnix(socketPath, stdout, stderr)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, nil, nil, nil, err
	}
	interruptClient, err := clientsession.DialUnixAs(socketPath, client.ID(), io.Discard, io.Discard)
	if err != nil {
		_ = client.Close()
		return nil, nil, nil, nil, err
	}
	stop := make(chan struct{})
	refresh := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				select {
				case refresh <- struct{}{}:
				default:
				}
			}
		}
	}()
	cleanup := func() {
		close(stop)
		_ = interruptClient.Close()
	}
	return client, session.Cancel, refresh, cleanup, nil
}

func wrapInteractiveClient(rt residentRuntime, paths residentPaths, client *clientsession.Client) wire.TermService {
	paneID := ""
	if rt.getenv != nil && rt.getenv("TMUX") != "" {
		paneID = strings.TrimSpace(rt.getenv("TMUX_PANE"))
	}
	if paneID == "" || paths.panePath == "" {
		return client
	}
	return &paneTrackingClient{
		client:   client,
		panePath: paths.panePath,
		paneID:   paneID,
	}
}

type paneTrackingClient struct {
	client   *clientsession.Client
	panePath string
	paneID   string
}

func (c *paneTrackingClient) markActive() {
	if c == nil || c.panePath == "" || c.paneID == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(c.panePath), 0o700)
	_ = writeResidentPaneID(c.panePath, c.paneID)
}

func (c *paneTrackingClient) Bootstrap(files []string) error {
	c.markActive()
	return c.client.Bootstrap(files)
}

func (c *paneTrackingClient) Execute(script string) (bool, error) {
	c.markActive()
	return c.client.Execute(script)
}

func (c *paneTrackingClient) CurrentView() (wire.BufferView, error) {
	return c.client.CurrentView()
}

func (c *paneTrackingClient) OpenFiles(files []string) (wire.BufferView, error) {
	c.markActive()
	return c.client.OpenFiles(files)
}

func (c *paneTrackingClient) OpenTarget(path, address string) (wire.BufferView, error) {
	c.markActive()
	return c.client.OpenTarget(path, address)
}

func (c *paneTrackingClient) MenuFiles() ([]wire.MenuFile, error) {
	return c.client.MenuFiles()
}

func (c *paneTrackingClient) NavigationStack() (wire.NavigationStack, error) {
	return c.client.NavigationStack()
}

func (c *paneTrackingClient) FocusFile(id int) (wire.BufferView, error) {
	c.markActive()
	return c.client.FocusFile(id)
}

func (c *paneTrackingClient) SetAddress(expr string) (wire.BufferView, error) {
	c.markActive()
	return c.client.SetAddress(expr)
}

func (c *paneTrackingClient) SetDot(start, end int) (wire.BufferView, error) {
	c.markActive()
	return c.client.SetDot(start, end)
}

func (c *paneTrackingClient) Replace(start, end int, text string) (wire.BufferView, error) {
	c.markActive()
	return c.client.Replace(start, end, text)
}

func (c *paneTrackingClient) Undo() (wire.BufferView, error) {
	c.markActive()
	return c.client.Undo()
}

func (c *paneTrackingClient) Save() (string, error) {
	c.markActive()
	return c.client.Save()
}

func isRetryableResidentDialError(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOTSOCK)
}

func hashedPathBase(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%s-%x", prefix, sum[:8])
}
