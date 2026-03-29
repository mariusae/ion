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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	clientsession "ion/internal/client/session"
	"ion/internal/proto/wire"
)

const lspNamespace = "lsp"

var lspMenuCommands = []wire.MenuCommand{
	{Command: ":lsp:goto", Label: "symbol"},
	{Command: ":lsp:show", Label: "hover"},
	{Command: ":lsp:gototype", Label: "type"},
}

func providerDoc() wire.NamespaceProviderDoc {
	return wire.NamespaceProviderDoc{
		Namespace: lspNamespace,
		Summary:   "Language Server Protocol commands",
		Help:      "Routes navigation and hover requests through configured LSP servers. Servers are selected by the configured path match rules.",
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
	cfg := config{
		servers: make(map[string]string),
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
		if _, exists := cfg.servers[name]; exists {
			return fmt.Errorf("duplicate -server %q", name)
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
	if len(cfg.servers) == 0 {
		return config{}, fmt.Errorf("at least one -server is required")
	}
	if len(cfg.matches) == 0 {
		return config{}, fmt.Errorf("at least one -match is required")
	}
	for _, rule := range cfg.matches {
		if _, ok := cfg.servers[rule.server]; !ok {
			return config{}, fmt.Errorf("-match %q references unknown server %q", rule.pattern, rule.server)
		}
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
		if err := handleInvocation(client, aux, manager, inv); err != nil {
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
		view.Name = path
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
	path, err := resolvePath(m.root, view.Name)
	if err != nil || path == "" {
		return "", nil, false
	}
	for _, rule := range m.matches {
		if !rule.re.MatchString(path) {
			continue
		}
		server := m.servers[rule.server]
		if server == nil {
			return "", nil, false
		}
		return path, server, true
	}
	return "", nil, false
}

func (m *lspManager) currentTarget(view wire.BufferView) (*lspServer, lspPosition, string, error) {
	path, server, ok := m.matchView(view)
	if !ok {
		return nil, lspPosition{}, "", fmt.Errorf("no LSP server configured for %q", view.Name)
	}
	if err := server.EnsureView(wire.BufferView{
		ID:   view.ID,
		Name: path,
		Text: view.Text,
	}); err != nil {
		return nil, lspPosition{}, "", err
	}
	return server, positionForOffset(view.Text, view.DotStart), pathToURI(path), nil
}

func handleInvocation(client, aux *clientsession.Client, manager *lspManager, inv wire.Invocation) error {
	script := strings.TrimSpace(inv.Script)
	session := client.Session(inv.SessionID)
	if session == nil {
		return client.FinishInvocation(inv.ID, "missing session", "", "")
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
	switch script {
	case ":lsp:goto":
		return finishGoto(client, session, manager, inv.ID, view, "textDocument/definition", "definition")
	case ":lsp:gototype":
		return finishGoto(client, session, manager, inv.ID, view, "textDocument/typeDefinition", "type definition")
	case ":lsp:show":
		return finishHover(client, manager, inv.ID, view)
	default:
		return client.FinishInvocation(inv.ID, fmt.Sprintf("unknown command `%s'", script), "", "")
	}
}

func finishGoto(client *clientsession.Client, session *clientsession.Session, manager *lspManager, invocationID uint64, view wire.BufferView, method, label string) error {
	server, pos, uri, err := manager.currentTarget(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	result, err := server.Request(method, map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	}, 30*time.Second)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	target, err := decodeLocationTarget(result)
	if err != nil {
		return client.FinishInvocation(invocationID, fmt.Sprintf("no %s found", label), "", "")
	}
	next, err := session.OpenTarget(target.Path, "")
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	if offset, ok := offsetForLineColumn(next.Text, target.Line, target.Column); ok {
		if _, err := session.SetDot(offset, offset); err != nil {
			return client.FinishInvocation(invocationID, err.Error(), "", "")
		}
	}
	return client.FinishInvocation(invocationID, "", fmt.Sprintf("%s %s:%d:%d\n", methodSummary(method), target.DisplayPath(manager.root), target.Line, target.Column), "")
}

func finishHover(client *clientsession.Client, manager *lspManager, invocationID uint64, view wire.BufferView) error {
	server, pos, uri, err := manager.currentTarget(view)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
	}
	result, err := server.Request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	}, 30*time.Second)
	if err != nil {
		return client.FinishInvocation(invocationID, err.Error(), "", "")
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

func methodSummary(method string) string {
	switch method {
	case "textDocument/typeDefinition":
		return "gototype"
	default:
		return "goto"
	}
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
}

type documentState struct {
	version int
	text    string
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
		if view.Name == "" {
			continue
		}
		uri := pathToURI(view.Name)
		seen[uri] = view
		if err := s.syncDocument(uri, view); err != nil {
			return err
		}
	}
	return s.closeMissing(seen)
}

func (s *lspServer) EnsureView(view wire.BufferView) error {
	if s == nil || view.Name == "" {
		return nil
	}
	if err := s.ensureStarted(); err != nil {
		return err
	}
	return s.syncDocument(pathToURI(view.Name), view)
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
				"typeDefinition": map[string]any{"dynamicRegistration": false, "linkSupport": true},
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

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case env, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s server disconnected", s.name)
		}
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", s.name, env.Error.Message)
		}
		return env.Result, nil
	case <-timer.C:
		return nil, fmt.Errorf("%s %s timed out", s.name, method)
	}
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
		s.publishStatus(line)
	}
}

func (s *lspServer) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "experimental/serverStatus", "window/logMessage", "$/progress":
		if message := formatServerNotification(method, params); message != "" {
			s.publishStatus(message)
		}
	}
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

type locationTarget struct {
	Path   string
	Line   int
	Column int
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
	var list []lspLocation
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		return targetFromLocation(list[0])
	}
	var one lspLocation
	if err := json.Unmarshal(raw, &one); err == nil && one.URI != "" {
		return targetFromLocation(one)
	}
	var links []lspLocationLink
	if err := json.Unmarshal(raw, &links); err == nil && len(links) > 0 {
		return targetFromLocationLink(links[0])
	}
	var link lspLocationLink
	if err := json.Unmarshal(raw, &link); err == nil && link.TargetURI != "" {
		return targetFromLocationLink(link)
	}
	return locationTarget{}, fmt.Errorf("no target")
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

func utf16Units(r rune) int {
	if r < 0x10000 {
		return 1
	}
	return len(utf16.Encode([]rune{r}))
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
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	return filepath.Clean(filepath.Join(root, name)), nil
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
