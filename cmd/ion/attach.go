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

	"ion/internal/client/download"
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

	service := wrapInteractiveClient(cfg, rt, paths, client)
	if err := bootstrapAttachTargets(&wireBModeClient{client: client}, targets); err != nil {
		return err
	}
	if len(targets) > 0 {
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

func bootstrapAttachTargets(client bModeClient, targets []clienttarget.Target) error {
	if len(targets) == 0 {
		return client.Bootstrap(nil)
	}
	return bootstrapMissingTargets(client, targets)
}

func runCommandMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	return runCommandModeWith(cfg, stdout, stderr, defaultResidentRuntime())
}

func runResidentDownloadMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return runResidentDownloadModeWith(cfg, stdin, stdout, stderr, defaultResidentRuntime())
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
	script := normalizeIonNamespaceAlias(strings.Join(cfg.files, " "))
	if !strings.HasSuffix(script, "\n") {
		script += "\n"
	}
	_, err = client.Execute(script)
	return err
}

func runResidentDownloadModeWith(cfg config, stdin io.Reader, stdout, stderr io.Writer, rt residentRuntime) error {
	paths, err := ensureResidentServer(cfg, rt)
	if err != nil {
		return err
	}
	client, err := clientsession.DialUnix(paths.socketPath, stdout, stderr)
	if err != nil {
		return err
	}
	defer client.Close()
	return download.Run(cfg.files, stdin, stderr, client)
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

func residentAttachKey(cfg config, rt residentRuntime) (string, error) {
	if rt.getenv != nil && rt.getenv("TMUX") != "" {
		targetPaneID := strings.TrimSpace(cfg.paneID)
		windowID, err := tmuxDisplay(rt.tmux, targetPaneID, "#{window_id}")
		if err != nil {
			return "", err
		}
		if windowID != "" {
			return "tmux-window:" + windowID, nil
		}
	}
	wd, err := rt.getwd()
	if err != nil {
		return "", err
	}
	return "cwd:" + filepath.Clean(wd), nil
}

func residentPathsForRuntime(cfg config, rt residentRuntime) (residentPaths, error) {
	key, err := residentAttachKey(cfg, rt)
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
	paths, err := residentPathsForRuntime(cfg, rt)
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
	interruptSession := interruptClient.Session(session.ID())
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
	return client, interruptSession.Cancel, refresh, cleanup, nil
}

func wrapInteractiveClient(cfg config, rt residentRuntime, paths residentPaths, client *clientsession.Client) wire.TermService {
	paneID := ""
	if rt.getenv != nil && rt.getenv("TMUX") != "" {
		paneID = strings.TrimSpace(rt.getenv("TMUX_PANE"))
	}
	if paneID == "" || paths.panePath == "" {
		return client
	}
	return &paneTrackingClient{
		client:   client,
		cfg:      cfg,
		rt:       rt,
		socket:   paths.socketPath,
		panePath: paths.panePath,
		paneID:   paneID,
	}
}

type paneTrackingClient struct {
	client   *clientsession.Client
	cfg      config
	rt       residentRuntime
	socket   string
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

func (c *paneTrackingClient) MenuSnapshot() (wire.MenuSnapshot, error) {
	return c.client.MenuSnapshot()
}

func (c *paneTrackingClient) NamespaceDocs() ([]wire.NamespaceProviderDoc, error) {
	return c.client.NamespaceDocs()
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

func (c *paneTrackingClient) OpenNewPane(files []string) error {
	if c == nil {
		return fmt.Errorf("missing resident client")
	}
	rt := bModeRuntimeFromResident(c.rt)
	ctx, ok, err := detectTmuxContext(rt, c.paneID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("command :term:new requires tmux")
	}
	cfg := c.cfg
	cfg.files = append([]string(nil), files...)
	if rt.getwd != nil {
		wd, err := rt.getwd()
		if err != nil {
			return err
		}
		cfg.files = resolveBModeTargets(cfg.files, wd)
	}
	return splitAttachPane(ctx, cfg, rt)
}

func (c *paneTrackingClient) PlumbOther(token string) error {
	if c == nil {
		return fmt.Errorf("missing resident client")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	currentID := uint64(0)
	if session := c.client.CurrentSession(); session != nil {
		currentID = session.ID()
	}
	summaries, err := c.client.ListSessions()
	if err != nil {
		return err
	}
	sessionID, ok := selectAlternateResidentSession(summaries, currentID)
	if !ok {
		return c.OpenNewPane([]string{token})
	}
	aux, err := clientsession.DialUnixAs(c.socket, c.client.ID(), io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defer aux.Close()
	if err := aux.Take(sessionID); err != nil {
		return err
	}
	_, execErr := aux.Session(sessionID).Execute(":ion:push " + token + "\n")
	returnErr := aux.Return(sessionID)
	if execErr != nil {
		return execErr
	}
	return returnErr
}

func selectAlternateResidentSession(summaries []wire.SessionSummary, currentID uint64) (uint64, bool) {
	for _, summary := range summaries {
		if summary.ID == 0 || summary.ID == currentID || summary.Taken {
			continue
		}
		return summary.ID, true
	}
	return 0, false
}

func bModeRuntimeFromResident(rt residentRuntime) bModeRuntime {
	return bModeRuntime{
		getenv:     rt.getenv,
		getwd:      rt.getwd,
		executable: rt.executable,
		tempDir:    rt.tempDir,
		spawn:      rt.spawn,
		tmux:       rt.tmux,
	}
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
