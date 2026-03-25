package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	clientsession "ion/internal/client/session"
	clienttarget "ion/internal/client/target"
	"ion/internal/client/term"
	"ion/internal/proto/wire"
	"ion/internal/server/transport"
	"ion/internal/server/workspace"
)

type tmuxContext struct {
	WindowID string
	PaneID   string
}

func (c tmuxContext) Key() string {
	return c.WindowID
}

type bModePaths struct {
	socketPath string
	panePath   string
}

type bModeClient interface {
	Bootstrap(files []string) error
	MenuFiles() ([]wire.MenuFile, error)
	FocusFile(id int) (wire.BufferView, error)
	OpenFiles(files []string) (wire.BufferView, error)
	SetAddress(expr string) (wire.BufferView, error)
	Close() error
}

type wireBModeClient struct {
	client *clientsession.Client
}

func (c *wireBModeClient) Bootstrap(files []string) error {
	return c.client.Bootstrap(files)
}

func (c *wireBModeClient) OpenFiles(files []string) (wire.BufferView, error) {
	return c.client.OpenFiles(files)
}

func (c *wireBModeClient) MenuFiles() ([]wire.MenuFile, error) {
	return c.client.MenuFiles()
}

func (c *wireBModeClient) FocusFile(id int) (wire.BufferView, error) {
	return c.client.FocusFile(id)
}

func (c *wireBModeClient) SetAddress(expr string) (wire.BufferView, error) {
	return c.client.SetAddress(expr)
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
		runTerm: runTermWithTargets,
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
	effectiveCfg := cfg
	if rt.getwd != nil {
		wd, err := rt.getwd()
		if err != nil {
			return err
		}
		effectiveCfg.files = resolveBModeTargets(cfg.files, wd)
	}
	paths := tmuxWindowPaths(rt.tempDir(), ctx.Key())

	client, err := rt.dial(paths.socketPath)
	if err == nil {
		defer client.Close()
		if len(effectiveCfg.files) > 0 {
			targets := clienttarget.ParseAll(effectiveCfg.files)
			if err := bootstrapMissingTargets(client, targets); err != nil {
				return err
			}
			if _, err := clienttarget.Open(client, effectiveCfg.files); err != nil {
				return err
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
	cmd := buildBServeCommand(exe, effectiveCfg)
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

func resolveBModeTargets(args []string, cwd string) []string {
	targets := clienttarget.ParseAll(args)
	resolved := make([]string, 0, len(targets))
	for _, target := range targets {
		path := target.Path
		if path != "" && !filepath.IsAbs(path) {
			path = filepath.Clean(filepath.Join(cwd, path))
		}
		resolved = append(resolved, formatResolvedTargetArg(clienttarget.Target{
			Path:    path,
			Address: target.Address,
		}))
	}
	return resolved
}

func formatResolvedTargetArg(target clienttarget.Target) string {
	if target.Address == "" {
		return target.Path
	}
	switch target.Address[0] {
	case '/', '?':
		return target.Path + ":" + target.Address
	}
	line, colPlusOne, ok := formatResolvedNumericAddress(target.Address)
	if ok {
		if colPlusOne > 0 {
			return fmt.Sprintf("%s:%d:%d", target.Path, line, colPlusOne)
		}
		return fmt.Sprintf("%s:%d", target.Path, line)
	}
	return target.Path + ":" + target.Address
}

func formatResolvedNumericAddress(addr string) (line int, colPlusOne int, ok bool) {
	lineText, colText, hasCol := strings.Cut(addr, "+#")
	line, err := strconv.Atoi(lineText)
	if err != nil {
		return 0, 0, false
	}
	if !hasCol {
		return line, 0, true
	}
	colOffset, err := strconv.Atoi(colText)
	if err != nil {
		return 0, 0, false
	}
	return line, colOffset + 1, true
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
	defer cleanupBModePaths(paths)

	capture := term.NewOutputCapture(stdout, stderr)
	ws := workspace.NewWithAutoIndent(cfg.autoindent)
	refresh := make(chan struct{}, 1)
	server := transport.NewWithNotifier(ws, func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	})
	return withServerSocket(server, paths.socketPath, capture.Stdout(), capture.Stderr(), func(client *clientsession.Client) error {
		targets := clienttarget.ParseAll(cfg.files)
		if err := bootstrapTargetSession(client, targets); err != nil {
			return err
		}
		if _, err := clienttarget.Open(client, cfg.files); err != nil {
			return err
		}
		return term.RunBootstrapped(stdin, stdout, stderr, client, capture, term.Options{
			AutoIndent: cfg.autoindent,
			Refresh:    refresh,
		})
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
	windowID, err := tmuxDisplay(rt.tmux, paneID, "#{window_id}")
	if err != nil {
		return tmuxContext{}, false, err
	}
	return tmuxContext{
		WindowID: windowID,
		PaneID:   paneID,
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
	}
}

func cleanupBModePaths(paths bModePaths) {
	_ = os.Remove(paths.socketPath)
	_ = os.Remove(paths.panePath)
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

func buildBServeCommand(exe string, cfg config) string {
	args := []string{shellQuote(exe)}
	if !cfg.autoindent {
		args = append(args, "-A")
	}
	args = append(args, "-b-serve", "--")
	for _, file := range cfg.files {
		args = append(args, shellQuote(file))
	}
	return "exec " + strings.Join(args, " ")
}

func runTermWithTargets(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	targets := clienttarget.ParseAll(cfg.files)
	capture := term.NewOutputCapture(stdout, stderr)
	ws := workspace.NewWithAutoIndent(cfg.autoindent)
	return withLocalServer(ws, capture.Stdout(), capture.Stderr(), func(client *clientsession.Client) error {
		if err := bootstrapTargetSession(client, targets); err != nil {
			return err
		}
		if _, err := clienttarget.Open(client, cfg.files); err != nil {
			return err
		}
		return term.RunBootstrapped(stdin, stdout, stderr, client, capture, term.Options{AutoIndent: cfg.autoindent})
	})
}

func bootstrapTargetSession(client bModeClient, targets []clienttarget.Target) error {
	paths := uniqueTargetPaths(targets)
	if len(paths) == 0 {
		return client.Bootstrap(nil)
	}
	return client.Bootstrap(paths)
}

func bootstrapMissingTargets(client bModeClient, targets []clienttarget.Target) error {
	menu, err := client.MenuFiles()
	if err != nil {
		return err
	}
	loaded := make(map[string]struct{}, len(menu))
	for _, file := range menu {
		if file.Name == "" {
			continue
		}
		loaded[file.Name] = struct{}{}
	}
	var missing []string
	for _, path := range uniqueTargetPaths(targets) {
		if _, ok := loaded[path]; ok {
			continue
		}
		missing = append(missing, path)
	}
	if len(missing) == 0 {
		return nil
	}
	return client.Bootstrap(missing)
}

func uniqueTargetPaths(targets []clienttarget.Target) []string {
	paths := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.Path == "" {
			continue
		}
		if _, ok := seen[target.Path]; ok {
			continue
		}
		seen[target.Path] = struct{}{}
		paths = append(paths, target.Path)
	}
	return paths
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
