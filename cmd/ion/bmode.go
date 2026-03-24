package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	clientsession "ion/internal/client/session"
	"ion/internal/client/term"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type tmuxContext struct {
	SessionID string
	WindowID  string
	PaneID    string
}

func (c tmuxContext) Key() string {
	return c.SessionID + "." + c.WindowID
}

type bModePaths struct {
	socketPath string
	panePath   string
	pidPath    string
}

type bModeClient interface {
	OpenFiles(files []string) error
	Close() error
}

type wireBModeClient struct {
	client *clientsession.Client
}

func (c *wireBModeClient) OpenFiles(files []string) error {
	_, err := c.client.OpenFiles(files)
	return err
}

func (c *wireBModeClient) Close() error {
	return c.client.Close()
}

type bModeRuntime struct {
	getenv     func(string) string
	getwd      func() (string, error)
	executable func() (string, error)
	tempDir    func() string
	dial       func(path string) (bModeClient, error)
	tmux       func(args ...string) (string, error)
	notify     func(paths bModePaths) error
	runTerm    func(cfg config, stdin io.Reader, stdout, stderr io.Writer) error
}

func defaultBModeRuntime() bModeRuntime {
	return bModeRuntime{
		getenv:     os.Getenv,
		getwd:      os.Getwd,
		executable: os.Executable,
		tempDir:    os.TempDir,
		dial: func(path string) (bModeClient, error) {
			client, err := clientsession.DialUnix(path, io.Discard, io.Discard)
			if err != nil {
				return nil, err
			}
			return &wireBModeClient{client: client}, nil
		},
		tmux:    runTmuxCommand,
		notify:  notifyResidentProcess,
		runTerm: runTerm,
	}
}

func runBMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return runBModeWith(cfg, stdin, stdout, stderr, defaultBModeRuntime())
}

func runBModeWith(cfg config, stdin io.Reader, stdout, stderr io.Writer, rt bModeRuntime) error {
	ctx, ok, err := detectTmuxContext(rt)
	if err != nil {
		return err
	}
	if !ok {
		return rt.runTerm(cfg, stdin, stdout, stderr)
	}
	paths := tmuxWindowPaths(rt.tempDir(), ctx.Key())

	client, err := rt.dial(paths.socketPath)
	if err == nil {
		defer client.Close()
		if len(cfg.files) > 0 {
			if err := client.OpenFiles(cfg.files); err != nil {
				return err
			}
			if rt.notify != nil {
				if err := rt.notify(paths); err != nil {
					return err
				}
			}
		}
		return focusResidentPane(paths, ctx, rt.tmux)
	}
	cleanupBModePaths(paths)

	exe, err := rt.executable()
	if err != nil {
		return err
	}
	wd, err := rt.getwd()
	if err != nil {
		return err
	}
	cmd := buildBServeCommand(exe, cfg.files)
	paneID, err := rt.tmux("split-window", "-c", wd, "-P", "-F", "#{pane_id}", cmd)
	if err != nil {
		return err
	}
	paneID = strings.TrimSpace(paneID)
	if paneID != "" {
		if _, err := rt.tmux("select-pane", "-t", paneID); err != nil {
			return err
		}
	}
	return nil
}

func runBServe(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	rt := defaultBModeRuntime()
	ctx, ok, err := detectTmuxContext(rt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("-b-serve requires tmux")
	}
	paths := tmuxWindowPaths(rt.tempDir(), ctx.Key())
	cleanupBModePaths(paths)
	if err := os.MkdirAll(filepath.Dir(paths.socketPath), 0o700); err != nil {
		return err
	}
	if err := writeResidentPaneID(paths.panePath, ctx.PaneID); err != nil {
		return err
	}
	if err := writeResidentPID(paths.pidPath, os.Getpid()); err != nil {
		return err
	}
	defer cleanupBModePaths(paths)

	capture := term.NewOutputCapture(stdout, stderr)
	ws := workspace.New()
	server := transport.New(ws)
	return withServerSocket(server, paths.socketPath, capture.Stdout(), capture.Stderr(), func(client *clientsession.Client) error {
		return term.Run(cfg.files, stdin, stdout, stderr, client, capture)
	})
}

func detectTmuxContext(rt bModeRuntime) (tmuxContext, bool, error) {
	if rt.getenv("TMUX") == "" {
		return tmuxContext{}, false, nil
	}
	paneID := strings.TrimSpace(rt.getenv("TMUX_PANE"))
	if paneID == "" {
		return tmuxContext{}, false, fmt.Errorf("TMUX_PANE not set")
	}
	sessionID, err := tmuxDisplay(rt.tmux, paneID, "#{session_id}")
	if err != nil {
		return tmuxContext{}, false, err
	}
	windowID, err := tmuxDisplay(rt.tmux, paneID, "#{window_id}")
	if err != nil {
		return tmuxContext{}, false, err
	}
	return tmuxContext{
		SessionID: sessionID,
		WindowID:  windowID,
		PaneID:    paneID,
	}, true, nil
}

func tmuxDisplay(tmux func(args ...string) (string, error), target, format string) (string, error) {
	out, err := tmux("display-message", "-p", "-t", target, format)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func tmuxWindowPaths(tempDir, key string) bModePaths {
	sum := sha256.Sum256([]byte(key))
	base := fmt.Sprintf("ion-b-%x", sum[:8])
	dir := filepath.Join(tempDir, "ion")
	return bModePaths{
		socketPath: filepath.Join(dir, base+".sock"),
		panePath:   filepath.Join(dir, base+".pane"),
		pidPath:    filepath.Join(dir, base+".pid"),
	}
}

func cleanupBModePaths(paths bModePaths) {
	_ = os.Remove(paths.socketPath)
	_ = os.Remove(paths.panePath)
	_ = os.Remove(paths.pidPath)
}

func writeResidentPaneID(path, paneID string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pane-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.WriteString(tmp, strings.TrimSpace(paneID)+"\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func readResidentPaneID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeResidentPID(path string, pid int) error {
	return writeTextFile(path, fmt.Sprintf("%d\n", pid))
}

func readResidentPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		return 0, err
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid resident pid %d", pid)
	}
	return pid, nil
}

func writeTextFile(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.WriteString(tmp, content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func focusResidentPane(paths bModePaths, ctx tmuxContext, tmux func(args ...string) (string, error)) error {
	paneID, err := readResidentPaneID(paths.panePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := tmux("select-window", "-t", ctx.WindowID); err != nil {
		return err
	}
	if paneID == "" {
		return nil
	}
	_, err = tmux("select-pane", "-t", paneID)
	return err
}

func notifyResidentProcess(paths bModePaths) error {
	pid, err := readResidentPID(paths.pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGUSR1); err != nil {
		return err
	}
	return nil
}

func buildBServeCommand(exe string, files []string) string {
	args := []string{shellQuote(exe), "-b-serve", "--"}
	for _, file := range files {
		args = append(args, shellQuote(file))
	}
	return "exec " + strings.Join(args, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runTmuxCommand(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}
