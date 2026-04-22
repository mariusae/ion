package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf16"

	clientsession "ion/internal/client/session"
	"ion/internal/proto/wire"
)

const lspNamespace = "lsp"
const maxServerLogEntries = 200

var lspMenuCommands = []wire.MenuCommand{
	{Command: ":lsp:goto", Label: "symbol", Shortcut: "g"},
	{Command: ":lsp:show", Label: "hover"},
	{Command: ":lsp:gototype", Label: "type"},
	{Command: ":lsp:usage", Label: "usage"},
	{Command: ":lsp:fmt", Label: "format", Shortcut: "f"},
}

func providerDoc() wire.NamespaceProviderDoc {
	return wire.NamespaceProviderDoc{
		Namespace: lspNamespace,
		Summary:   "Language Server Protocol commands",
		Help:      "Routes navigation, status, and hover requests through configured LSP servers. Servers are selected by the configured path match rules.",
		Commands: []wire.NamespaceCommandDoc{
			{
				Name:    "goto",
				Summary: "jump to the definition under dot",
				Help:    "Resolves the symbol at dot via textDocument/definition and opens the first target in the current ion session. Takes no arguments.",
			},
			{
				Name:    "show",
				Summary: "show hover information under dot",
				Help:    "Requests textDocument/hover for the symbol at dot and prints the rendered hover contents. Takes no arguments.",
			},
			{
				Name:    "gototype",
				Summary: "jump to the type definition under dot",
				Help:    "Resolves the type of the symbol at dot via textDocument/typeDefinition and opens the first target in the current ion session. Takes no arguments.",
			},
			{
				Name:    "usage",
				Summary: "list usages of the symbol under dot",
				Help:    "Requests textDocument/references for the symbol under dot and prints one usage per line as path:line:column with the matching source line. Takes no arguments.",
			},
			{
				Name:    "symbol",
				Args:    "<query>",
				Summary: "search workspace symbols by name",
				Help:    "Requests workspace/symbol from the matching LSP server and prints one result per line as path:line:column: symbol-name. The query argument is required.",
			},
			{
				Name:    "fmt",
				Summary: "format the current document",
				Help:    "Requests textDocument/formatting for the current buffer, applies the returned edits to the live ion session, and preserves dot as closely as possible. Takes no arguments.",
			},
			{
				Name:    "status",
				Summary: "show the current state of the matching LSP server",
				Help:    "Prints the current runtime state, configured command, synced document count, and latest status message for the LSP server selected for the current buffer. Takes no arguments.",
			},
			{
				Name:    "log",
				Summary: "show recent logs from the matching LSP server",
				Help:    "Prints the recent stderr and notification messages seen from the LSP server selected for the current buffer. Takes no arguments.",
			},
		},
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type config struct {
	socketPath string
	foreground bool
	readyFD    int
	servers    map[string]string
	matches    []matchRule
}

type matchRule struct {
	pattern string
	re      *regexp.Regexp
	server  string
}

var defaultLSPServers = map[string]string{
	"go":     "gopls serve",
	"rust":   "rust-analyzer",
	"python": "uvx --from python-lsp-server pylsp",
	"clang":  "clangd",
}

var defaultLSPMatchRules = []struct {
	pattern string
	server  string
}{
	{pattern: `\.go$`, server: "go"},
	{pattern: `\.rs$`, server: "rust"},
	{pattern: `\.pyi?$`, server: "python"},
	{pattern: `\.(c|cc|cpp|cxx|h|hh|hpp|hxx)$`, server: "clang"},
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "ion-lsp: %v\n", err)
		return 2
	}
	if !cfg.foreground {
		if err := daemonize(cfg, stderr); err != nil {
			fmt.Fprintf(stderr, "ion-lsp: %v\n", err)
			return 1
		}
		return 0
	}
	ready, err := readyWriter(cfg.readyFD)
	if err != nil {
		fmt.Fprintf(stderr, "ion-lsp: %v\n", err)
		return 1
	}
	if err := runForeground(cfg, ready); err != nil {
		fmt.Fprintf(stderr, "ion-lsp: %v\n", err)
		return 1
	}
	return 0
}

func parseArgs(args []string) (config, error) {
	cfg, err := defaultConfig()
	if err != nil {
		return config{}, err
	}
	fs := flag.NewFlagSet("ion-lsp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.socketPath, "socket", "", "ion server socket path")
	fs.BoolVar(&cfg.foreground, "foreground", false, "run in the foreground")
	fs.IntVar(&cfg.readyFD, "ready-fd", -1, "internal: daemon startup pipe fd")
	fs.Func("server", "name:command", func(value string) error {
		name, command, ok := splitNamedValue(value)
		if !ok {
			return fmt.Errorf("bad -server %q, want name:command", value)
		}
		name = normalizeServerName(name)
		if name == "" {
			return fmt.Errorf("bad -server %q", value)
		}
		cfg.servers[name] = strings.TrimSpace(command)
		return nil
	})
	fs.Func("match", "regexp:name", func(value string) error {
		pattern, server, ok := splitNamedValue(value)
		if !ok {
			return fmt.Errorf("bad -match %q, want regexp:name", value)
		}
		server = normalizeServerName(server)
		if server == "" {
			return fmt.Errorf("bad -match %q", value)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("bad -match regexp %q: %w", pattern, err)
		}
		cfg.matches = append(cfg.matches, matchRule{
			pattern: pattern,
			re:      re,
			server:  server,
		})
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if len(fs.Args()) != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if cfg.socketPath == "" {
		cfg.socketPath = strings.TrimSpace(os.Getenv("ION_SOCKET"))
	}
	if cfg.socketPath == "" {
		return config{}, fmt.Errorf("missing ion socket; run from ion or pass -socket")
	}
	for _, rule := range cfg.matches {
		if _, ok := cfg.servers[rule.server]; !ok {
			return config{}, fmt.Errorf("-match %q references unknown server %q", rule.pattern, rule.server)
		}
	}
	return cfg, nil
}

func defaultConfig() (config, error) {
	cfg := config{
		servers: make(map[string]string, len(defaultLSPServers)),
		matches: make([]matchRule, 0, len(defaultLSPMatchRules)),
	}
	for name, command := range defaultLSPServers {
		cfg.servers[name] = command
	}
	for _, rule := range defaultLSPMatchRules {
		re, err := regexp.Compile(rule.pattern)
		if err != nil {
			return config{}, err
		}
		cfg.matches = append(cfg.matches, matchRule{
			pattern: rule.pattern,
			re:      re,
			server:  rule.server,
		})
	}
	return cfg, nil
}

func splitNamedValue(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	i := strings.LastIndexByte(value, ':')
	if i <= 0 || i == len(value)-1 {
		return "", "", false
	}
	return strings.TrimSpace(value[:i]), strings.TrimSpace(value[i+1:]), true
}

func normalizeServerName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return ""
	}
	return name
}

func daemonize(cfg config, stderr io.Writer) error {
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

	cmdArgs := []string{"-foreground", "-socket", cfg.socketPath, "-ready-fd", "3"}
	for name, command := range cfg.servers {
		cmdArgs = append(cmdArgs, "-server", name+":"+command)
	}
	for _, rule := range cfg.matches {
		cmdArgs = append(cmdArgs, "-match", rule.pattern+":"+rule.server)
	}
	cmd := exec.Command(exe, cmdArgs...)
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
		return errors.New("startup failed")
	}
	status = strings.TrimSpace(status)
	if status == "ok" {
		return cmd.Process.Release()
	}
	_ = cmd.Wait()
	if status == "" {
		return errors.New("startup failed")
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, status+"\n")
	}
	return errors.New(status)
}

func readyWriter(fd int) (io.WriteCloser, error) {
	if fd < 0 {
		return nil, nil
	}
	return os.NewFile(uintptr(fd), "ion-lsp-ready"), nil
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

func runForeground(cfg config, ready io.WriteCloser) (err error) {
	readySignaled := false
	defer func() {
		if !readySignaled {
			signalReady(ready, err)
		}
	}()

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	client, err := clientsession.DialUnix(cfg.socketPath, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.RegisterNamespaceProvider(providerDoc()); err != nil {
		return err
	}
	aux, err := clientsession.DialUnixAs(cfg.socketPath, client.ID(), io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defer aux.Close()
	cancelClient, err := clientsession.DialUnixAs(cfg.socketPath, client.ID(), io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defer cancelClient.Close()

	manager := newLSPManager(root, cfg, func(message string) {
		publishRecentStatus(aux, message)
	})
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalReady(ready, nil)
	readySignaled = true
	go manager.syncLoop(ctx, aux)
	go installSharedMenuItems(cfg.socketPath, lspMenuCommands)
	defer removeSharedMenuItems(cfg.socketPath, lspMenuCommands)

	for {
		inv, err := client.WaitInvocation()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := handleInvocation(client, aux, cancelClient, manager, inv); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func publishRecentStatus(client *clientsession.Client, message string) {
	message = strings.TrimSpace(message)
	if client == nil || message == "" {
		return
	}
	sessions, err := client.ListSessions()
	if err != nil {
		return
	}
	for _, session := range sessions {
		if session.ID == 0 {
			continue
		}
		_ = client.SetSessionStatus(session.ID, message)
		return
	}
}

func installSharedMenuItems(socketPath string, items []wire.MenuCommand) {
	_ = withTemporaryMenuSession(socketPath, func(session *clientsession.Session) error {
		for _, item := range items {
			line := ":ion:menuadd " + strings.TrimSpace(item.Command)
			if label := strings.TrimSpace(item.Label); label != "" {
				line += " " + strconv.Quote(label)
			}
			if shortcut := strings.TrimSpace(item.Shortcut); shortcut != "" {
				line += " " + shortcut
			}
			if _, err := session.Execute(line + "\n"); err != nil {
				return err
			}
		}
		return nil
	})
}

func removeSharedMenuItems(socketPath string, items []wire.MenuCommand) {
	_ = withTemporaryMenuSession(socketPath, func(session *clientsession.Session) error {
		for _, item := range items {
			if _, err := session.Execute(":ion:menudel " + strings.TrimSpace(item.Command) + "\n"); err != nil {
				return err
			}
		}
		return nil
	})
}

func withTemporaryMenuSession(socketPath string, fn func(*clientsession.Session) error) error {
	if socketPath == "" || fn == nil {
		return nil
	}
	client, err := clientsession.DialUnix(socketPath, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	return fn(session)
}

type lspManager struct {
	root     string
	rootURI  string
	matches  []matchRule
	servers  map[string]*lspServer
	statusFn func(string)
}

func newLSPManager(root string, cfg config, statusFn func(string)) *lspManager {
	root = filepath.Clean(root)
	servers := make(map[string]*lspServer, len(cfg.servers))
	for name, command := range cfg.servers {
		serverName := name
		servers[name] = &lspServer{
			name:       serverName,
			languageID: serverName,
			command:    command,
			root:       root,
			rootURI:    pathToURI(root),
			statusFn: func(message string) {
				if statusFn == nil {
					return
				}
				statusFn("lsp[" + serverName + "] " + message)
			},
		}
	}
	return &lspManager{
		root:     root,
		rootURI:  pathToURI(root),
		matches:  append([]matchRule(nil), cfg.matches...),
		servers:  servers,
		statusFn: statusFn,
	}
}

func (m *lspManager) Close() {
	if m == nil {
		return
	}
	for _, server := range m.servers {
		server.Close()
	}
}

func (m *lspManager) syncLoop(ctx context.Context, client *clientsession.Client) {
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	_ = m.syncBuffers(client)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.syncBuffers(client)
		}
	}
}

func (m *lspManager) syncBuffers(client *clientsession.Client) error {
	if m == nil || client == nil {
		return nil
	}
	buffers, err := client.BufferSnapshots()
	if err != nil {
		return err
	}
	grouped := make(map[string][]wire.BufferView)
	for _, view := range buffers {
		path, server, ok := m.matchView(view)
		if !ok {
			continue
		}
		view.Path = path
		grouped[server.name] = append(grouped[server.name], view)
	}
	for name, server := range m.servers {
		if err := server.Sync(grouped[name]); err != nil {
			return err
		}
	}
	return nil
}

func (m *lspManager) matchView(view wire.BufferView) (string, *lspServer, bool) {
	if m == nil {
		return "", nil, false
	}
	path, err := resolvePath(m.root, viewPath(view))
	if err != nil || path == "" {
		return "", nil, false
	}
	var matched *lspServer
	for _, rule := range m.matches {
		if !rule.re.MatchString(path) {
			continue
		}
		server := m.servers[rule.server]
		if server == nil {
			return "", nil, false
		}
		matched = server
	}
	if matched == nil {
		return "", nil, false
	}
	return path, matched, true
}

func (m *lspManager) currentTarget(view wire.BufferView) (*lspServer, lspPosition, string, error) {
	path, server, err := m.serverForView(view)
	if err != nil {
		return nil, lspPosition{}, "", err
	}
	if err := server.EnsureView(wire.BufferView{
		ID:   view.ID,
		Name: view.Name,
		Path: path,
		Text: view.Text,
	}); err != nil {
		return nil, lspPosition{}, "", err
	}
	return server, positionForOffset(view.Text, view.DotStart), pathToURI(path), nil
}

func (m *lspManager) serverForView(view wire.BufferView) (string, *lspServer, error) {
	path, server, ok := m.matchView(view)
	if !ok {
		return "", nil, fmt.Errorf("no LSP server configured for %q", viewPath(view))
	}
	return path, server, nil
}

func handleInvocation(client, aux, cancelClient *clientsession.Client, manager *lspManager, inv wire.Invocation) error {
	script := strings.TrimSpace(inv.Script)
	command, args := splitInvocationScript(script)
	session := client.Session(inv.SessionID)
	if session == nil {
		return client.FinishInvocation(inv.ID, "missing session", "", "")
	}
	ctx := context.Background()
	if cancelClient != nil {
		ctx = canceledInvocationContext(cancelClient, inv.ID)
	}
	if err := manager.syncBuffers(aux); err != nil {
		_ = client.FinishInvocation(inv.ID, err.Error(), "", "")
		return nil
	}
	if err := client.Take(inv.SessionID); err != nil {
		return client.FinishInvocation(inv.ID, err.Error(), "", "")
	}
	defer func() {
		_ = client.Return(inv.SessionID)
	}()
	view, err := session.CurrentView()
	if err != nil {
		_ = client.FinishInvocation(inv.ID, err.Error(), "", "")
		return nil
	}
	switch command {
	case ":lsp:goto":
		return finishGoto(ctx, client, session, manager, inv.ID, view, "textDocument/definition", "definition")
	case ":lsp:gototype":
		return finishGoto(ctx, client, session, manager, inv.ID, view, "textDocument/typeDefinition", "type definition")
	case ":lsp:show":
		return finishHover(ctx, client, manager, inv.ID, view)
	case ":lsp:usage":
		return finishUsage(ctx, client, manager, inv.ID, view)
	case ":lsp:symbol":
		return finishWorkspaceSymbol(ctx, client, manager, inv.ID, view, args)
	case ":lsp:fmt":
		return finishFormat(ctx, client, manager, inv.ID, view)
	case ":lsp:status":
		return finishStatus(client, manager, inv.ID, view)
	case ":lsp:log":
		return finishLog(client, manager, inv.ID, view)
	default:
		return client.FinishInvocation(inv.ID, fmt.Sprintf("unknown command `%s'", script), "", "")
	}
}

func splitInvocationScript(script string) (string, string) {
	script = strings.TrimSpace(script)
	if script == "" {
		return "", ""
	}
	split := strings.IndexFunc(script, unicode.IsSpace)
	if split < 0 {
		return script, ""
	}
	return script[:split], strings.TrimSpace(script[split:])
}

func canceledInvocationContext(client *clientsession.Client, invocationID uint64) context.Context {
	if client == nil || invocationID == 0 {
		return context.Background()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		canceled, err := client.WaitInvocationCancel(invocationID)
		if err != nil || !canceled {
			return
		}
		cancel()
	}()
	return ctx
}

func finishGoto(ctx context.Context, client *clientsession.Client, session *clientsession.Session, manager *lspManager, invocationID uint64, view wire.BufferView, method, label string) error {
	server, pos, uri, err := manager.currentTarget(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	result, err := server.RequestContext(ctx, method, map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	}, 30*time.Second)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return client.FinishInvocation(invocationID, "", "", "")
		}
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if ctx.Err() != nil {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	target, err := decodeLocationTarget(result)
	if err != nil {
		return client.FinishInvocation(invocationID, fmt.Sprintf("no %s found", label), "", "")
	}
	if ctx.Err() != nil {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	if _, err := session.Execute(manager.pushCommand(view, server, target) + "\n"); err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	return client.FinishInvocation(invocationID, "", "", "")
}

func (m *lspManager) targetAddress(view wire.BufferView, server *lspServer, target locationTarget) (string, bool) {
	text, ok := m.targetText(view, server, target.Path)
	if !ok {
		return "", false
	}
	offset, ok := offsetForLineColumn(text, target.Line, target.Column)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("#%d", offset), true
}

func (m *lspManager) pushCommand(view wire.BufferView, server *lspServer, target locationTarget) string {
	spec := target.Path
	if address, ok := m.targetAddress(view, server, target); ok {
		spec += ":" + address
	} else {
		spec += ":" + strconv.Itoa(target.Line)
		if target.Column > 1 {
			spec += ":" + strconv.Itoa(target.Column)
		}
	}
	return ":ion:push " + spec
}

func (m *lspManager) targetText(view wire.BufferView, server *lspServer, path string) (string, bool) {
	if currentPath, err := resolvePath(m.root, viewPath(view)); err == nil && currentPath == path {
		return view.Text, true
	}
	if text, ok := server.DocumentText(path); ok {
		return text, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (s *lspServer) DocumentText(path string) (string, bool) {
	if s == nil || path == "" {
		return "", false
	}
	uri := pathToURI(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[uri]
	if !ok {
		return "", false
	}
	return doc.text, true
}

func finishHover(ctx context.Context, client *clientsession.Client, manager *lspManager, invocationID uint64, view wire.BufferView) error {
	server, pos, uri, err := manager.currentTarget(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	result, err := server.RequestContext(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	}, 30*time.Second)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return client.FinishInvocation(invocationID, "", "", "")
		}
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if ctx.Err() != nil {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	text := formatHoverResult(result)
	if strings.TrimSpace(text) == "" {
		return client.FinishInvocation(invocationID, "no hover found", "", "")
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return client.FinishInvocation(invocationID, "", text, "")
}

func finishUsage(ctx context.Context, client *clientsession.Client, manager *lspManager, invocationID uint64, view wire.BufferView) error {
	server, pos, uri, err := manager.currentTarget(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	result, err := server.RequestContext(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
		"context": map[string]any{
			"includeDeclaration": false,
		},
	}, 30*time.Second)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return client.FinishInvocation(invocationID, "", "", "")
		}
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if ctx.Err() != nil {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	targets, err := decodeLocationTargets(result)
	if err != nil || len(targets) == 0 {
		return client.FinishInvocation(invocationID, "no usages found", "", "")
	}
	text := formatUsageResult(manager, view, server, targets)
	if text == "" {
		return client.FinishInvocation(invocationID, "no usages found", "", "")
	}
	return client.FinishInvocation(invocationID, "", text, "")
}

func finishWorkspaceSymbol(ctx context.Context, client *clientsession.Client, manager *lspManager, invocationID uint64, view wire.BufferView, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return client.FinishInvocation(invocationID, "missing symbol query", "", "")
	}
	if manager == nil {
		return client.FinishInvocation(invocationID, "missing lsp manager", "", "")
	}
	_, server, err := manager.serverForView(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	result, err := server.RequestContext(ctx, "workspace/symbol", map[string]any{
		"query": query,
	}, 30*time.Second)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return client.FinishInvocation(invocationID, "", "", "")
		}
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if ctx.Err() != nil {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	symbols, err := decodeWorkspaceSymbolTargets(result)
	if err != nil || len(symbols) == 0 {
		return client.FinishInvocation(invocationID, "no symbols found", "", "")
	}
	text := formatWorkspaceSymbolResult(manager.root, symbols)
	if strings.TrimSpace(text) == "" {
		return client.FinishInvocation(invocationID, "no symbols found", "", "")
	}
	return client.FinishInvocation(invocationID, "", text, "")
}

func finishFormat(ctx context.Context, client *clientsession.Client, manager *lspManager, invocationID uint64, view wire.BufferView) error {
	path, server, err := manager.serverForView(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	canonicalView := view
	canonicalView.Name = path
	if err := server.EnsureView(canonicalView); err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	result, err := server.RequestContext(ctx, "textDocument/formatting", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"options": map[string]any{
			"tabSize":                4,
			"insertSpaces":           false,
			"trimTrailingWhitespace": true,
			"insertFinalNewline":     true,
			"trimFinalNewlines":      true,
		},
	}, 30*time.Second)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return client.FinishInvocation(invocationID, "", "", "")
		}
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if ctx.Err() != nil {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	edits, err := decodeTextEdits(result)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if len(edits) == 0 {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	formatted, err := applyTextEdits(view.Text, edits)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if formatted == view.Text {
		return client.FinishInvocation(invocationID, "", "", "")
	}
	startPos := positionForOffset(view.Text, view.DotStart)
	endPos := positionForOffset(view.Text, view.DotEnd)
	updatedView, err := client.Replace(0, len([]rune(view.Text)), formatted)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	start := clipOffset(len([]rune(formatted)), view.DotStart)
	if next, ok := offsetForLSPPosition(formatted, startPos); ok {
		start = next
	}
	end := clipOffset(len([]rune(formatted)), view.DotEnd)
	if next, ok := offsetForLSPPosition(formatted, endPos); ok {
		end = next
	}
	if start > end {
		start, end = end, start
	}
	updatedView, err = client.SetDot(start, end)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if err := server.EnsureView(wire.BufferView{
		ID:       updatedView.ID,
		Name:     updatedView.Name,
		Path:     path,
		Text:     updatedView.Text,
		DotStart: updatedView.DotStart,
		DotEnd:   updatedView.DotEnd,
	}); err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	return client.FinishInvocation(invocationID, "", "", "")
}

func finishStatus(client *clientsession.Client, manager *lspManager, invocationID uint64, view wire.BufferView) error {
	_, server, err := manager.serverForView(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	return client.FinishInvocation(invocationID, "", server.StatusReport(), "")
}

func finishLog(client *clientsession.Client, manager *lspManager, invocationID uint64, view wire.BufferView) error {
	_, server, err := manager.serverForView(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	return client.FinishInvocation(invocationID, "", server.LogReport(), "")
}

type lspServer struct {
	name       string
	languageID string
	command    string
	root       string
	rootURI    string
	statusFn   func(string)

	writeMu sync.Mutex
	mu      sync.Mutex

	cmd         *exec.Cmd
	stdin       io.WriteCloser
	pending     map[int64]chan rpcEnvelope
	nextID      int64
	docs        map[string]documentState
	initialized bool
	closed      bool
	lastStatus  string
	logs        []serverLogEntry
}

type documentState struct {
	version int
	text    string
}

type serverLogEntry struct {
	At      time.Time
	Source  string
	Message string
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

func (s *lspServer) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	cmd := s.cmd
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = s.Notify("exit", map[string]any{})
	if _, err := s.Request("shutdown", map[string]any{}, 2*time.Second); err != nil {
		_ = cmd.Process.Kill()
	}
	_, _ = cmd.Process.Wait()
	s.mu.Lock()
	s.cmd = nil
	s.stdin = nil
	s.initialized = false
	s.pending = nil
	s.docs = nil
	s.mu.Unlock()
}

func (s *lspServer) Sync(buffers []wire.BufferView) error {
	if s == nil {
		return nil
	}
	if len(buffers) == 0 {
		s.mu.Lock()
		started := s.initialized
		s.mu.Unlock()
		if !started {
			return nil
		}
	}
	if err := s.ensureStarted(); err != nil {
		return err
	}
	seen := make(map[string]wire.BufferView, len(buffers))
	for _, view := range buffers {
		if viewPath(view) == "" {
			continue
		}
		uri := pathToURI(viewPath(view))
		seen[uri] = view
		if err := s.syncDocument(uri, view); err != nil {
			return err
		}
	}
	return s.closeMissing(seen)
}

func (s *lspServer) EnsureView(view wire.BufferView) error {
	if s == nil || viewPath(view) == "" {
		return nil
	}
	if err := s.ensureStarted(); err != nil {
		return err
	}
	return s.syncDocument(pathToURI(viewPath(view)), view)
}

func (s *lspServer) ensureStarted() error {
	s.mu.Lock()
	if s.initialized {
		s.mu.Unlock()
		return nil
	}
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("%s server closed", s.name)
	}
	s.mu.Unlock()

	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-lc", s.command)
	cmd.Dir = s.root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = cmd.Process.Kill()
		return fmt.Errorf("%s server closed", s.name)
	}
	s.cmd = cmd
	s.stdin = stdin
	s.pending = make(map[int64]chan rpcEnvelope)
	s.docs = make(map[string]documentState)
	s.initialized = true
	s.mu.Unlock()

	go s.readLoop(stdout)
	go s.stderrLoop(stderr)

	_, err = s.Request("initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   s.rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"definition":     map[string]any{"dynamicRegistration": false, "linkSupport": true},
				"references":     map[string]any{"dynamicRegistration": false},
				"typeDefinition": map[string]any{"dynamicRegistration": false, "linkSupport": true},
				"formatting":     map[string]any{"dynamicRegistration": false},
				"hover":          map[string]any{"dynamicRegistration": false},
				"synchronization": map[string]any{
					"didSave": true,
				},
			},
			"experimental": map[string]any{
				"serverStatusNotification": true,
			},
			"window": map[string]any{
				"workDoneProgress": true,
			},
		},
		"workspaceFolders": []map[string]any{
			{"uri": s.rootURI, "name": filepath.Base(s.root)},
		},
	}, 60*time.Second)
	if err != nil {
		s.Close()
		return err
	}
	return s.Notify("initialized", map[string]any{})
}

func (s *lspServer) Request(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	return s.RequestContext(context.Background(), method, params, timeout)
}

func (s *lspServer) RequestContext(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := s.ensureStarted(); err != nil {
		return nil, err
	}
	ch := make(chan rpcEnvelope, 1)
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.send(rpcEnvelope{
		JSONRPC: "2.0",
		ID:      mustRawJSON(id),
		Method:  method,
		Params:  mustMarshalJSON(params),
	}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}

	select {
	case env, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s server disconnected", s.name)
		}
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", s.name, env.Error.Message)
		}
		return env.Result, nil
	case <-ctx.Done():
		if s.cancelPendingRequest(id) {
			_ = s.Notify("$/cancelRequest", map[string]any{"id": id})
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s %s timed out", s.name, method)
		}
		return nil, ctx.Err()
	}
}

func (s *lspServer) cancelPendingRequest(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return false
	}
	if _, ok := s.pending[id]; !ok {
		return false
	}
	delete(s.pending, id)
	return true
}

func (s *lspServer) Notify(method string, params any) error {
	if s == nil {
		return nil
	}
	return s.send(rpcEnvelope{
		JSONRPC: "2.0",
		Method:  method,
		Params:  mustMarshalJSON(params),
	})
}

func (s *lspServer) syncDocument(uri string, view wire.BufferView) error {
	s.mu.Lock()
	doc, ok := s.docs[uri]
	if !ok {
		s.docs[uri] = documentState{version: 1, text: view.Text}
		s.mu.Unlock()
		return s.Notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": s.languageID,
				"version":    1,
				"text":       view.Text,
			},
		})
	}
	if doc.text == view.Text {
		s.mu.Unlock()
		return nil
	}
	doc.version++
	doc.text = view.Text
	s.docs[uri] = doc
	version := doc.version
	s.mu.Unlock()
	return s.Notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     uri,
			"version": version,
		},
		"contentChanges": []map[string]any{
			{"text": view.Text},
		},
	})
}

func (s *lspServer) closeMissing(seen map[string]wire.BufferView) error {
	s.mu.Lock()
	missing := make([]string, 0)
	for uri := range s.docs {
		if _, ok := seen[uri]; ok {
			continue
		}
		missing = append(missing, uri)
		delete(s.docs, uri)
	}
	s.mu.Unlock()
	for _, uri := range missing {
		if err := s.Notify("textDocument/didClose", map[string]any{
			"textDocument": map[string]any{"uri": uri},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *lspServer) send(env rpcEnvelope) error {
	if s == nil {
		return nil
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	header := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)))
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	stdin := s.stdin
	s.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("%s server stdin closed", s.name)
	}
	if _, err := stdin.Write(header); err != nil {
		return err
	}
	_, err = stdin.Write(body)
	return err
}

func (s *lspServer) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		env, err := readRPCEnvelope(reader)
		if err != nil {
			s.failPending()
			return
		}
		if env.Method != "" {
			if len(env.ID) != 0 {
				_ = s.send(rpcEnvelope{
					JSONRPC: "2.0",
					ID:      env.ID,
					Result:  mustMarshalJSON(nil),
				})
				continue
			}
			s.handleNotification(env.Method, env.Params)
			continue
		}
		id, ok := parseRPCID(env.ID)
		if !ok {
			continue
		}
		s.mu.Lock()
		ch := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if ch != nil {
			ch <- env
			close(ch)
		}
	}
}

func (s *lspServer) stderrLoop(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.publishMessage("stderr", line)
	}
	if err := scanner.Err(); err != nil {
		s.appendLog("stderr", "read error: "+err.Error())
	}
}

func (s *lspServer) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "experimental/serverStatus", "window/logMessage", "$/progress":
		if message := formatServerNotification(method, params); message != "" {
			s.publishMessage(method, message)
		}
	}
}

func (s *lspServer) publishMessage(source, message string) {
	s.appendLog(source, message)
	s.publishStatus(message)
}

func (s *lspServer) appendLog(source, message string) {
	message = strings.TrimSpace(message)
	source = strings.TrimSpace(source)
	if s == nil || message == "" {
		return
	}
	s.mu.Lock()
	s.logs = append(s.logs, serverLogEntry{
		At:      time.Now(),
		Source:  source,
		Message: message,
	})
	if len(s.logs) > maxServerLogEntries {
		s.logs = append([]serverLogEntry(nil), s.logs[len(s.logs)-maxServerLogEntries:]...)
	}
	s.mu.Unlock()
}

func (s *lspServer) publishStatus(message string) {
	message = strings.TrimSpace(message)
	if s == nil || message == "" {
		return
	}
	s.mu.Lock()
	if message == s.lastStatus {
		s.mu.Unlock()
		return
	}
	s.lastStatus = message
	statusFn := s.statusFn
	s.mu.Unlock()
	if statusFn != nil {
		statusFn(message)
	}
}

func (s *lspServer) failPending() {
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[int64]chan rpcEnvelope)
	s.initialized = false
	s.stdin = nil
	s.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

func (s *lspServer) StatusReport() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	name := s.name
	command := s.command
	root := s.root
	status := s.lastStatus
	docCount := len(s.docs)
	state := s.runtimeStateLocked()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	s.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "lsp[%s]\n", name)
	fmt.Fprintf(&b, "state: %s\n", state)
	fmt.Fprintf(&b, "command: %s\n", command)
	fmt.Fprintf(&b, "root: %s\n", root)
	fmt.Fprintf(&b, "documents: %d\n", docCount)
	if pid > 0 {
		fmt.Fprintf(&b, "pid: %d\n", pid)
	}
	if status != "" {
		fmt.Fprintf(&b, "status: %s\n", status)
	}
	return b.String()
}

func (s *lspServer) LogReport() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	name := s.name
	state := s.runtimeStateLocked()
	logs := append([]serverLogEntry(nil), s.logs...)
	s.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "lsp[%s] log\n", name)
	fmt.Fprintf(&b, "state: %s\n", state)
	if len(logs) == 0 {
		b.WriteString("no log entries\n")
		return b.String()
	}
	for _, entry := range logs {
		fmt.Fprintf(&b, "%s [%s] %s\n", entry.At.Format(time.RFC3339), entry.Source, entry.Message)
	}
	return b.String()
}

func (s *lspServer) runtimeStateLocked() string {
	switch {
	case s.closed:
		return "closed"
	case s.cmd == nil:
		return "idle"
	case s.initialized:
		return "running"
	case s.stdin == nil:
		return "disconnected"
	default:
		return "starting"
	}
}

func readRPCEnvelope(reader *bufio.Reader) (rpcEnvelope, error) {
	headers := make(map[string]string)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return rpcEnvelope{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		headers[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
	}
	n, err := strconv.Atoi(headers["Content-Length"])
	if err != nil || n < 0 {
		return rpcEnvelope{}, fmt.Errorf("bad content length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(reader, body); err != nil {
		return rpcEnvelope{}, err
	}
	var env rpcEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return rpcEnvelope{}, err
	}
	return env, nil
}

func mustRawJSON(v int64) json.RawMessage {
	return json.RawMessage([]byte(strconv.FormatInt(v, 10)))
}

func mustMarshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(data)
}

func parseRPCID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err == nil {
		return id, true
	}
	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err == nil {
		return int64(asFloat), true
	}
	return 0, false
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type lspLocationLink struct {
	TargetURI            string   `json:"targetUri"`
	TargetSelectionRange lspRange `json:"targetSelectionRange"`
	TargetRange          lspRange `json:"targetRange"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspTextEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

type locationTarget struct {
	Path   string
	Line   int
	Column int
}

type workspaceSymbolTarget struct {
	Name          string
	ContainerName string
	Target        locationTarget
	HasLocation   bool
}

func (t locationTarget) DisplayPath(root string) string {
	rel, err := filepath.Rel(root, t.Path)
	if err != nil {
		return t.Path
	}
	if strings.HasPrefix(rel, "..") {
		return t.Path
	}
	return rel
}

func decodeLocationTarget(raw json.RawMessage) (locationTarget, error) {
	targets, err := decodeLocationTargets(raw)
	if err != nil {
		return locationTarget{}, err
	}
	return targets[0], nil
}

func decodeLocationTargets(raw json.RawMessage) ([]locationTarget, error) {
	var links []lspLocationLink
	if err := json.Unmarshal(raw, &links); err == nil {
		targets := make([]locationTarget, 0, len(links))
		for _, link := range links {
			if link.TargetURI == "" {
				continue
			}
			target, err := targetFromLocationLink(link)
			if err != nil {
				return nil, err
			}
			targets = append(targets, target)
		}
		if len(targets) > 0 {
			return targets, nil
		}
	}
	var link lspLocationLink
	if err := json.Unmarshal(raw, &link); err == nil && link.TargetURI != "" {
		target, err := targetFromLocationLink(link)
		if err != nil {
			return nil, err
		}
		return []locationTarget{target}, nil
	}
	var list []lspLocation
	if err := json.Unmarshal(raw, &list); err == nil {
		targets := make([]locationTarget, 0, len(list))
		for _, loc := range list {
			if loc.URI == "" {
				continue
			}
			target, err := targetFromLocation(loc)
			if err != nil {
				return nil, err
			}
			targets = append(targets, target)
		}
		if len(targets) > 0 {
			return targets, nil
		}
	}
	var one lspLocation
	if err := json.Unmarshal(raw, &one); err == nil && one.URI != "" {
		target, err := targetFromLocation(one)
		if err != nil {
			return nil, err
		}
		return []locationTarget{target}, nil
	}
	return nil, fmt.Errorf("no target")
}

func decodeTextEdits(raw json.RawMessage) ([]lspTextEdit, error) {
	var edits []lspTextEdit
	if err := json.Unmarshal(raw, &edits); err != nil {
		return nil, fmt.Errorf("bad text edits")
	}
	return edits, nil
}

func decodeWorkspaceSymbolTargets(raw json.RawMessage) ([]workspaceSymbolTarget, error) {
	type rawWorkspaceSymbol struct {
		Name          string          `json:"name"`
		ContainerName string          `json:"containerName"`
		Location      json.RawMessage `json:"location"`
	}
	var list []rawWorkspaceSymbol
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("bad workspace symbols")
	}
	out := make([]workspaceSymbolTarget, 0, len(list))
	for _, item := range list {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		target, ok, err := decodeWorkspaceSymbolLocation(item.Location)
		if err != nil {
			return nil, err
		}
		out = append(out, workspaceSymbolTarget{
			Name:          name,
			ContainerName: strings.TrimSpace(item.ContainerName),
			Target:        target,
			HasLocation:   ok,
		})
	}
	return out, nil
}

func decodeWorkspaceSymbolLocation(raw json.RawMessage) (locationTarget, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return locationTarget{}, false, nil
	}
	var withRange struct {
		URI   string    `json:"uri"`
		Range *lspRange `json:"range"`
	}
	if err := json.Unmarshal(raw, &withRange); err == nil && strings.TrimSpace(withRange.URI) != "" && withRange.Range != nil {
		target, err := targetFromLocation(lspLocation{URI: withRange.URI, Range: *withRange.Range})
		if err != nil {
			return locationTarget{}, false, err
		}
		return target, true, nil
	}
	var uriOnly struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &uriOnly); err == nil && strings.TrimSpace(uriOnly.URI) != "" {
		path, err := pathFromURI(uriOnly.URI)
		if err != nil {
			return locationTarget{}, false, err
		}
		return locationTarget{Path: path}, true, nil
	}
	return locationTarget{}, false, nil
}

func targetFromLocation(loc lspLocation) (locationTarget, error) {
	path, err := pathFromURI(loc.URI)
	if err != nil {
		return locationTarget{}, err
	}
	return buildLocationTarget(path, loc.Range.Start)
}

func targetFromLocationLink(link lspLocationLink) (locationTarget, error) {
	path, err := pathFromURI(link.TargetURI)
	if err != nil {
		return locationTarget{}, err
	}
	pos := link.TargetSelectionRange.Start
	if link.TargetSelectionRange == (lspRange{}) {
		pos = link.TargetRange.Start
	}
	return buildLocationTarget(path, pos)
}

func buildLocationTarget(path string, pos lspPosition) (locationTarget, error) {
	line := pos.Line + 1
	column := utf16ToRuneColumnForFile(path, pos.Line, pos.Character)
	return locationTarget{
		Path:   path,
		Line:   line,
		Column: column,
	}, nil
}

func utf16ToRuneColumnForFile(path string, targetLine, targetChar int) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return targetChar + 1
	}
	line := 0
	column := 0
	units := 0
	for _, r := range string(data) {
		if line == targetLine {
			if units >= targetChar {
				break
			}
			column++
			units += utf16Units(r)
			continue
		}
		if r == '\n' {
			line++
		}
	}
	if column <= 0 {
		return 1
	}
	return column
}

func positionForOffset(text string, offset int) lspPosition {
	runes := []rune(text)
	if offset < 0 {
		offset = 0
	}
	if offset > len(runes) {
		offset = len(runes)
	}
	line := 0
	character := 0
	for i := 0; i < offset; i++ {
		r := runes[i]
		if r == '\n' {
			line++
			character = 0
			continue
		}
		character += utf16Units(r)
	}
	return lspPosition{Line: line, Character: character}
}

func offsetForLineColumn(text string, line, column int) (int, bool) {
	if line < 1 || column < 1 {
		return 0, false
	}
	runes := []rune(text)
	currentLine := 1
	currentColumn := 1
	for i, r := range runes {
		if currentLine == line && currentColumn == column {
			return i, true
		}
		if r == '\n' {
			currentLine++
			currentColumn = 1
			if currentLine == line && column == 1 {
				return i + 1, true
			}
			continue
		}
		currentColumn++
	}
	if currentLine == line && currentColumn == column {
		return len(runes), true
	}
	return 0, false
}

func offsetForLSPPosition(text string, pos lspPosition) (int, bool) {
	if pos.Line < 0 || pos.Character < 0 {
		return 0, false
	}
	runes := []rune(text)
	line := 0
	character := 0
	for i, r := range runes {
		if line == pos.Line {
			if character == pos.Character {
				return i, true
			}
			if r == '\n' {
				return 0, false
			}
			next := character + utf16Units(r)
			if next > pos.Character {
				return i, true
			}
			character = next
			continue
		}
		if r == '\n' {
			line++
			character = 0
		}
	}
	if line == pos.Line && character == pos.Character {
		return len(runes), true
	}
	return 0, false
}

func clipOffset(n, offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > n {
		return n
	}
	return offset
}

func utf16Units(r rune) int {
	if r < 0x10000 {
		return 1
	}
	return len(utf16.Encode([]rune{r}))
}

func applyTextEdits(text string, edits []lspTextEdit) (string, error) {
	if len(edits) == 0 {
		return text, nil
	}
	type resolvedEdit struct {
		start   int
		end     int
		newText string
	}
	resolved := make([]resolvedEdit, 0, len(edits))
	for _, edit := range edits {
		start, ok := offsetForLSPPosition(text, edit.Range.Start)
		if !ok {
			return "", fmt.Errorf("bad formatting result")
		}
		end, ok := offsetForLSPPosition(text, edit.Range.End)
		if !ok || end < start {
			return "", fmt.Errorf("bad formatting result")
		}
		resolved = append(resolved, resolvedEdit{
			start:   start,
			end:     end,
			newText: edit.NewText,
		})
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start != resolved[j].start {
			return resolved[i].start < resolved[j].start
		}
		return resolved[i].end < resolved[j].end
	})
	for i := 1; i < len(resolved); i++ {
		if resolved[i].start < resolved[i-1].end {
			return "", fmt.Errorf("bad formatting result")
		}
	}
	runes := []rune(text)
	for i := len(resolved) - 1; i >= 0; i-- {
		edit := resolved[i]
		repl := []rune(edit.newText)
		next := make([]rune, 0, len(runes)-(edit.end-edit.start)+len(repl))
		next = append(next, runes[:edit.start]...)
		next = append(next, repl...)
		next = append(next, runes[edit.end:]...)
		runes = next
	}
	return string(runes), nil
}

func formatHoverResult(raw json.RawMessage) string {
	var payload struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(renderHoverContents(payload.Contents))
}

func formatUsageResult(manager *lspManager, view wire.BufferView, server *lspServer, targets []locationTarget) string {
	if manager == nil || server == nil || len(targets) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(targets))
	var b strings.Builder
	for _, target := range targets {
		key := fmt.Sprintf("%s:%d:%d", target.Path, target.Line, target.Column)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		line := targetLineText(manager, view, server, target)
		if line != "" {
			fmt.Fprintf(&b, "%s:%d:%d: %s\n", target.DisplayPath(manager.root), target.Line, target.Column, line)
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d\n", target.DisplayPath(manager.root), target.Line, target.Column)
	}
	return b.String()
}

func formatWorkspaceSymbolResult(root string, symbols []workspaceSymbolTarget) string {
	if len(symbols) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(symbols))
	var b strings.Builder
	for _, symbol := range symbols {
		name := symbol.Name
		if symbol.ContainerName != "" {
			name = symbol.ContainerName + "." + name
		}
		switch {
		case symbol.HasLocation && symbol.Target.Path != "" && symbol.Target.Line > 0:
			key := fmt.Sprintf("%s:%d:%d:%s", symbol.Target.Path, symbol.Target.Line, symbol.Target.Column, name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			fmt.Fprintf(&b, "%s:%d:%d: %s\n", symbol.Target.DisplayPath(root), symbol.Target.Line, symbol.Target.Column, name)
		case symbol.HasLocation && symbol.Target.Path != "":
			key := fmt.Sprintf("%s:%s", symbol.Target.Path, name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			fmt.Fprintf(&b, "%s: %s\n", symbol.Target.DisplayPath(root), name)
		default:
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			fmt.Fprintf(&b, "%s\n", name)
		}
	}
	return b.String()
}

func targetLineText(manager *lspManager, view wire.BufferView, server *lspServer, target locationTarget) string {
	if manager == nil || server == nil {
		return ""
	}
	text, ok := manager.targetText(view, server, target.Path)
	if !ok {
		return ""
	}
	return lineText(text, target.Line)
}

func lineText(text string, line int) string {
	if line < 1 {
		return ""
	}
	current := 1
	start := 0
	runes := []rune(text)
	for i, r := range runes {
		if r != '\n' {
			continue
		}
		if current == line {
			return strings.TrimSpace(string(runes[start:i]))
		}
		current++
		start = i + 1
	}
	if current == line {
		return strings.TrimSpace(string(runes[start:]))
	}
	return ""
}

func renderHoverContents(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var marked struct {
		Language string `json:"language"`
		Value    string `json:"value"`
		Kind     string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &marked); err == nil && strings.TrimSpace(marked.Value) != "" {
		if strings.TrimSpace(marked.Language) != "" {
			return "```" + marked.Language + "\n" + strings.TrimSpace(marked.Value) + "\n```"
		}
		return strings.TrimSpace(marked.Value)
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			part := strings.TrimSpace(renderHoverContents(item))
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func formatServerNotification(method string, raw json.RawMessage) string {
	switch method {
	case "experimental/serverStatus":
		var payload struct {
			Message   string `json:"message"`
			Quiescent bool   `json:"quiescent"`
		}
		if err := json.Unmarshal(raw, &payload); err == nil {
			if strings.TrimSpace(payload.Message) != "" {
				return payload.Message
			}
			if payload.Quiescent {
				return "ready"
			}
		}
	case "window/logMessage":
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &payload); err == nil {
			return strings.TrimSpace(payload.Message)
		}
	case "$/progress":
		var payload struct {
			Value struct {
				Title   string `json:"title"`
				Message string `json:"message"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &payload); err == nil {
			switch {
			case strings.TrimSpace(payload.Value.Message) != "":
				return payload.Value.Message
			case strings.TrimSpace(payload.Value.Title) != "":
				return payload.Value.Title
			}
		}
	}
	return ""
}

func resolvePath(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	var path string
	if filepath.IsAbs(name) {
		path = filepath.Clean(name)
	} else {
		path = filepath.Clean(filepath.Join(root, name))
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	return path, nil
}

func viewPath(view wire.BufferView) string {
	if strings.TrimSpace(view.Path) != "" {
		return strings.TrimSpace(view.Path)
	}
	return strings.TrimSpace(view.Name)
}

func pathToURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

func pathFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported uri %q", uri)
	}
	path, err := url.PathUnescape(u.Path)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("missing file path")
	}
	return filepath.Clean(path), nil
}
