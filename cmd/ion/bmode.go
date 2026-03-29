package main

import (
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
	"ion/internal/server/workspace"
)

type tmuxContext struct {
	SessionID    string
	WindowID     string
	PaneID       string
	TargetPaneID string
}

func (c tmuxContext) Key() string {
	return "tmux-session:" + c.SessionID
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
	OpenTarget(path, address string) (wire.BufferView, error)
	SetAddress(expr string) (wire.BufferView, error)
	ListSessions() ([]wire.SessionSummary, error)
	Take(id uint64) error
	Return(id uint64) error
	ExecuteSession(id uint64, script string) (bool, error)
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

func (c *wireBModeClient) OpenTarget(path, address string) (wire.BufferView, error) {
	return c.client.OpenTarget(path, address)
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

func (c *wireBModeClient) ListSessions() ([]wire.SessionSummary, error) {
	return c.client.ListSessions()
}

func (c *wireBModeClient) Take(id uint64) error {
	return c.client.Take(id)
}

func (c *wireBModeClient) Return(id uint64) error {
	return c.client.Return(id)
}

func (c *wireBModeClient) ExecuteSession(id uint64, script string) (bool, error) {
	return c.client.Session(id).Execute(script)
}

type bModeRuntime struct {
	getenv     func(string) string
	getwd      func() (string, error)
	executable func() (string, error)
	tempDir    func() string
	spawn      func(config, string) error
	dial       func(path string) (bModeClient, error)
	tmux       func(args ...string) (string, error)
	runAttach  func(cfg config, stdin io.Reader, stdout, stderr io.Writer) error
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
		tmux:      runTmuxCommand,
		runAttach: runAttachMode,
		runTerm:   runTermWithTargets,
	}
}

func runBMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return runBModeWith(cfg, stdin, stdout, stderr, defaultBModeRuntime())
}

func runNewPaneMode(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	return runNewPaneModeWith(cfg, stdin, stdout, stderr, defaultBModeRuntime())
}

func runBModeWith(cfg config, stdin io.Reader, stdout, stderr io.Writer, rt bModeRuntime) error {
	ctx, ok, err := detectTmuxContext(rt, cfg.paneID)
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
		if len(effectiveCfg.files) == 0 {
			return focusResidentPane(paths, ctx, rt.tmux)
		}
		sessionID, ok := selectBModeSession(client)
		if !ok {
			return splitAttachPane(ctx, effectiveCfg, rt)
		}
		if err := client.Take(sessionID); err != nil {
			return splitAttachPane(ctx, effectiveCfg, rt)
		}
		defer func() {
			_ = client.Return(sessionID)
		}()
		if _, err := client.ExecuteSession(sessionID, buildBModeScript(effectiveCfg.files)); err != nil {
			return err
		}
		return focusResidentPane(paths, ctx, rt.tmux)
	}
	if !isRetryableResidentDialError(err) {
		return err
	}
	if _, err := ensureResidentServer(cfg, residentRuntimeFromBMode(rt)); err != nil {
		return err
	}
	return splitAttachPane(ctx, effectiveCfg, rt)
}

func runNewPaneModeWith(cfg config, stdin io.Reader, stdout, stderr io.Writer, rt bModeRuntime) error {
	ctx, ok, err := detectTmuxContext(rt, cfg.paneID)
	if err != nil {
		return err
	}
	if !ok {
		if rt.runAttach == nil {
			return runAttachMode(cfg, stdin, stdout, stderr)
		}
		return rt.runAttach(cfg, stdin, stdout, stderr)
	}
	effectiveCfg := cfg
	if rt.getwd != nil {
		wd, err := rt.getwd()
		if err != nil {
			return err
		}
		effectiveCfg.files = resolveBModeTargets(cfg.files, wd)
	}
	if _, err := ensureResidentServer(cfg, residentRuntimeFromBMode(rt)); err != nil {
		return err
	}
	return splitAttachPane(ctx, effectiveCfg, rt)
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
	return runAttachMode(cfg, stdin, stdout, stderr)
}

func detectTmuxContext(rt bModeRuntime, paneOverride string) (tmuxContext, bool, error) {
	if rt.getenv("TMUX") == "" {
		return tmuxContext{}, false, nil
	}
	paneID := strings.TrimSpace(rt.getenv("TMUX_PANE"))
	targetPaneID := strings.TrimSpace(paneOverride)
	if targetPaneID == "" {
		targetPaneID = paneID
	}
	if targetPaneID == "" {
		return tmuxContext{}, false, fmt.Errorf("TMUX_PANE not set")
	}
	sessionID, err := tmuxDisplay(rt.tmux, targetPaneID, "#{session_id}")
	if err != nil {
		return tmuxContext{}, false, err
	}
	windowID, err := tmuxDisplay(rt.tmux, targetPaneID, "#{window_id}")
	if err != nil {
		return tmuxContext{}, false, err
	}
	return tmuxContext{
		SessionID:    sessionID,
		WindowID:     windowID,
		PaneID:       paneID,
		TargetPaneID: targetPaneID,
	}, true, nil
}

func tmuxDisplay(tmux func(args ...string) (string, error), target, format string) (string, error) {
	args := []string{"display-message", "-p"}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, format)
	out, err := tmux(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func tmuxWindowPaths(tempDir, key string) residentPaths {
	base := hashedPathBase(residentPathVersionPrefix, key)
	dir := filepath.Join(tempDir, "ion")
	return residentPaths{
		socketPath: filepath.Join(dir, base+".sock"),
		panePath:   filepath.Join(dir, base+".pane"),
	}
}

func cleanupBModePaths(paths residentPaths) {
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

func focusResidentPane(paths residentPaths, ctx tmuxContext, tmux func(args ...string) (string, error)) error {
	paneID, err := readResidentPaneID(paths.panePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if paneID == "" {
		if _, err := tmux("select-window", "-t", ctx.WindowID); err != nil {
			return err
		}
		return nil
	}
	windowID, err := tmuxDisplay(tmux, paneID, "#{window_id}")
	if err != nil {
		return err
	}
	if windowID == "" {
		windowID = ctx.WindowID
	}
	if _, err := tmux("select-window", "-t", windowID); err != nil {
		return err
	}
	_, err = tmux("select-pane", "-t", paneID)
	return err
}

func buildAttachCommand(exe string, cfg config) string {
	args := []string{shellQuote(exe)}
	args = append(args, "-A")
	if !cfg.autoindent {
		args = append(args, "-no-autoindent")
	}
	args = append(args, "--")
	for _, file := range cfg.files {
		args = append(args, shellQuote(file))
	}
	return "exec " + strings.Join(args, " ")
}

func buildBModeScript(files []string) string {
	if len(files) == 0 {
		return "\n"
	}
	return "B " + strings.Join(files, " ") + "\n"
}

func selectBModeSession(client bModeClient) (uint64, bool) {
	if client == nil {
		return 0, false
	}
	sessions, err := client.ListSessions()
	if err != nil {
		return 0, false
	}
	for _, session := range sessions {
		if session.Taken {
			continue
		}
		if session.ID == 0 {
			continue
		}
		return session.ID, true
	}
	return 0, false
}

func splitAttachPane(ctx tmuxContext, cfg config, rt bModeRuntime) error {
	exe, err := rt.executable()
	if err != nil {
		return err
	}
	wd, err := rt.getwd()
	if err != nil {
		return err
	}
	cmd := buildAttachCommand(exe, cfg)
	paneID, err := rt.tmux("split-window", "-t", ctx.TargetPaneID, "-c", wd, "-P", "-F", "#{pane_id}", cmd)
	if err != nil {
		return err
	}
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}
	_, err = rt.tmux("select-pane", "-t", paneID)
	return err
}

func residentRuntimeFromBMode(rt bModeRuntime) residentRuntime {
	return residentRuntime{
		getenv:     rt.getenv,
		getwd:      rt.getwd,
		tempDir:    rt.tempDir,
		tmux:       rt.tmux,
		executable: rt.executable,
		spawn:      rt.spawn,
	}
}

func runTermWithTargets(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	targets := clienttarget.ParseAll(cfg.files)
	capture := term.NewOutputCapture(stdout, stderr)
	ws := workspace.NewWithAutoIndent(cfg.autoindent)
	if shouldPreloadAddressedStartup(targets) {
		if err := preloadTargetWorkspace(ws, targets, capture.Stdout(), capture.Stderr()); err != nil {
			return err
		}
	}
	return withLocalServerClients(ws, capture.Stdout(), capture.Stderr(), func(client *clientsession.Client, interruptClient *clientsession.Client) error {
		if !shouldPreloadAddressedStartup(targets) {
			if err := bootstrapTargetSession(&wireBModeClient{client: client}, targets); err != nil {
				return err
			}
		}
		if _, err := clienttarget.Open(client, cfg.files); err != nil {
			return err
		}
		return term.RunBootstrapped(stdin, stdout, stderr, client, capture, term.Options{
			AutoIndent: cfg.autoindent,
			Interrupt:  interruptClient.Interrupt,
		})
	})
}

func bootstrapTargetSession(client bModeClient, targets []clienttarget.Target) error {
	paths := uniqueTargetPaths(targets)
	if len(paths) == 0 {
		return client.Bootstrap(nil)
	}
	return client.Bootstrap(paths)
}

func shouldPreloadAddressedStartup(targets []clienttarget.Target) bool {
	if len(targets) == 0 {
		return false
	}
	return targets[len(targets)-1].Address != ""
}

func preloadTargetWorkspace(ws *workspace.Workspace, targets []clienttarget.Target, stdout, stderr io.Writer) error {
	if ws == nil {
		return nil
	}
	return ws.Bootstrap(ws.NewSessionState(), uniqueTargetPaths(targets), stdout, stderr)
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
