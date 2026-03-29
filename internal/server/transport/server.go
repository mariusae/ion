package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ion/internal/proto/wire"
	serversession "ion/internal/server/session"
	"ion/internal/server/workspace"
)

// Server serves ion wire requests over one listener.
type Server struct {
	ws           *workspace.Workspace
	changeNotify func()

	mu             sync.Mutex
	listener       net.Listener
	conns          map[io.ReadWriteCloser]struct{}
	nextClient     uint64
	nextInvocation uint64
	clients        map[uint64]*serverClient
	sessions       map[uint64]*managedSession
	namespaces     map[string]uint64
	invocations    map[uint64]*invocationState
}

type serverClient struct {
	id           uint64
	primaryRefs  int
	auxRefs      int
	ownedSession []uint64
	namespaces   map[string]struct{}
	pending      []*invocationState
	closed       bool
	cond         *sync.Cond
}

type managedSession struct {
	id               uint64
	ownerClientID    uint64
	controllerClient atomic.Uint64
	term             *serversession.TermSession
	lastActive       time.Time
	closed           atomic.Bool
	delegate         *delegatedIO

	mu   sync.Mutex
	cond *sync.Cond
}

type delegatedIO struct {
	stdout io.Writer
	stderr io.Writer
}

type invocationState struct {
	id               uint64
	providerClientID uint64
	sessionID        uint64
	script           string

	mu     sync.Mutex
	cond   *sync.Cond
	done   bool
	err    string
	stdout string
	stderr string
}

type connState struct {
	clientID  uint64
	auxiliary bool
}

type sessionCommand struct {
	name      string
	sessionID uint64
}

type diagnosticReporter interface {
	Diagnostic() string
}

// New constructs a transport server over one shared workspace.
func New(ws *workspace.Workspace) *Server {
	return NewWithNotifier(ws, nil)
}

// NewWithNotifier constructs a transport server over one shared workspace and
// runs notify after successful state-changing requests.
func NewWithNotifier(ws *workspace.Workspace, notify func()) *Server {
	return &Server{
		ws:           ws,
		changeNotify: notify,
		conns:        make(map[io.ReadWriteCloser]struct{}),
		clients:      make(map[uint64]*serverClient),
		sessions:     make(map[uint64]*managedSession),
		namespaces:   make(map[string]uint64),
		invocations:  make(map[uint64]*invocationState),
	}
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve(listener net.Listener) error {
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.listener == listener {
			s.listener = nil
		}
		s.mu.Unlock()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func() {
			_ = s.ServeConn(conn)
		}()
	}
}

// ServeConn handles requests for one transport connection.
func (s *Server) ServeConn(conn io.ReadWriteCloser) error {
	defer conn.Close()
	s.trackConn(conn)
	defer s.untrackConn(conn)

	state := &connState{}
	stdout := &eventWriter{conn: conn, kind: wire.KindStdoutEvent}
	stderr := &eventWriter{conn: conn, kind: wire.KindStderrEvent}
	defer s.releaseClient(state.clientID, state.auxiliary)

	for {
		frame, err := wire.ReadFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := s.handleFrame(conn, state, stdout, stderr, frame); err != nil {
			return err
		}
	}
}

func (s *Server) handleFrame(conn io.Writer, state *connState, stdout, stderr *eventWriter, frame wire.Frame) error {
	msg, err := wire.DecodeMessage(frame)
	if err != nil {
		return writeError(conn, frame.RequestID, frame.SessionID, err)
	}

	stdout.requestID = frame.RequestID
	stderr.requestID = frame.RequestID

	switch msg := msg.(type) {
	case *wire.ConnectRequest:
		clientID, auxiliary, err := s.connectClient(msg.ClientID)
		if err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		state.clientID = clientID
		state.auxiliary = auxiliary
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.ConnectResponse{ClientID: clientID})
	}

	if state.clientID == 0 {
		return writeError(conn, frame.RequestID, frame.SessionID, fmt.Errorf("client not connected"))
	}

	switch msg := msg.(type) {
	case *wire.NewSessionRequest:
		session, err := s.newSession(state.clientID)
		if err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.id, &wire.NewSessionResponse{SessionID: session.id})
	case *wire.SessionListRequest:
		summaries, err := s.listSessions(state.clientID)
		if err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.SessionListResponse{Sessions: summaries})
	case *wire.TakeSessionRequest:
		if err := s.takeSession(msg.SessionID, state.clientID); err != nil {
			return writeError(conn, frame.RequestID, msg.SessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.ReturnSessionRequest:
		if err := s.returnSession(msg.SessionID, state.clientID); err != nil {
			return writeError(conn, frame.RequestID, msg.SessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.NamespaceRegisterRequest:
		if err := s.registerNamespace(state.clientID, msg.Namespace); err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.InvocationWaitRequest:
		inv, err := s.waitInvocation(state.clientID)
		if err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.InvocationWaitResponse{Invocation: wire.Invocation{
			ID:        inv.id,
			SessionID: inv.sessionID,
			Script:    inv.script,
		}})
	case *wire.InvocationFinishRequest:
		if err := s.finishInvocation(state.clientID, msg); err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.DisconnectRequest:
		s.releaseClient(state.clientID, state.auxiliary)
		state.clientID = 0
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.CommandRequest:
		if cmd, ok, err := parseSessionCommand(msg.Script); ok {
			if err != nil {
				return writeError(conn, frame.RequestID, frame.SessionID, err)
			}
			return s.handleSessionCommand(conn, state.clientID, stdout, stderr, frame, cmd)
		}
		if inv, ok, err := s.beginInvocation(state.clientID, frame.SessionID, msg.Script); ok {
			if err != nil {
				return writeError(conn, frame.RequestID, frame.SessionID, err)
			}
			return s.handleInvocation(conn, frame, inv, stdout, stderr)
		}
	}

	managed, err := s.waitForControlledSession(frame.SessionID, state.clientID, msg)
	if err != nil {
		return writeError(conn, frame.RequestID, frame.SessionID, err)
	}
	if _, ok := msg.(*wire.InterruptRequest); ok {
		if err := managed.term.Interrupt(); err != nil {
			return writeError(conn, frame.RequestID, managed.id, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.OKResponse{})
	}
	stdout.sessionID = managed.id
	stderr.sessionID = managed.id

	notify, err := s.withManagedSession(managed, stdout, stderr, func(sess *serversession.TermSession) (bool, error) {
		switch msg := msg.(type) {
		case *wire.BootstrapRequest:
			if err := sess.Bootstrap(msg.Files); err != nil {
				return false, err
			}
			return len(msg.Files) > 0, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.OKResponse{})
		case *wire.OpenFilesRequest:
			view, err := sess.OpenFiles(msg.Files)
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.OpenTargetRequest:
			view, err := sess.OpenTarget(msg.Path, msg.Address)
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.CommandRequest:
			if isServerQuitCommand(msg.Script) {
				if err := wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.CommandResponse{Continue: false}); err != nil {
					return false, err
				}
				return false, s.Shutdown()
			}
			cont, err := sess.Execute(msg.Script)
			if err != nil {
				return false, err
			}
			return shouldMarkActive(msg), wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.CommandResponse{Continue: cont})
		case *wire.InterruptRequest:
			if err := sess.Interrupt(); err != nil {
				return false, err
			}
			return false, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.OKResponse{})
		case *wire.CurrentViewRequest:
			view, err := sess.CurrentView()
			if err != nil {
				return false, err
			}
			return false, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.MenuFilesRequest:
			files, err := sess.MenuFiles()
			if err != nil {
				return false, err
			}
			return false, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.MenuFilesMessage{Files: files})
		case *wire.NavigationStackRequest:
			stack, err := sess.NavigationStack()
			if err != nil {
				return false, err
			}
			return false, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.NavigationStackMessage{Stack: stack})
		case *wire.FocusRequest:
			view, err := sess.FocusFile(msg.ID)
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.AddressRequest:
			view, err := sess.SetAddress(msg.Expr)
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.SetDotRequest:
			view, err := sess.SetDot(msg.Start, msg.End)
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.ReplaceRequest:
			view, err := sess.Replace(msg.Start, msg.End, msg.Text)
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.UndoRequest:
			view, err := sess.Undo()
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.BufferViewMessage{View: view})
		case *wire.SaveRequest:
			status, err := sess.Save()
			if err != nil {
				return false, err
			}
			return true, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.SaveResponse{Status: status})
		default:
			return false, fmt.Errorf("unsupported request kind %d", frame.Kind)
		}
	})
	if err != nil {
		return writeError(conn, frame.RequestID, managed.id, err)
	}
	if notify {
		s.markSessionActive(managed.id)
		if s.changeNotify != nil {
			s.changeNotify()
		}
	}
	return nil
}

func (s *Server) connectClient(requested uint64) (uint64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if requested != 0 {
		client, ok := s.clients[requested]
		if !ok {
			return 0, false, fmt.Errorf("unknown client %d", requested)
		}
		if client.closed || client.primaryRefs == 0 {
			return 0, false, fmt.Errorf("unknown client %d", requested)
		}
		client.auxRefs++
		return client.id, true, nil
	}

	s.nextClient++
	client := &serverClient{
		id:          s.nextClient,
		primaryRefs: 1,
		namespaces:  make(map[string]struct{}),
	}
	client.cond = sync.NewCond(&s.mu)
	s.clients[client.id] = client
	return client.id, false, nil
}

func (s *Server) releaseClient(clientID uint64, auxiliary bool) {
	if clientID == 0 {
		return
	}
	s.mu.Lock()
	client, ok := s.clients[clientID]
	if !ok {
		s.mu.Unlock()
		return
	}
	if auxiliary {
		if client.auxRefs > 0 {
			client.auxRefs--
		}
		s.mu.Unlock()
		return
	}
	if client.primaryRefs > 0 {
		client.primaryRefs--
	}
	if client.primaryRefs > 0 {
		s.mu.Unlock()
		return
	}
	client.closed = true
	client.cond.Broadcast()
	owned := append([]uint64(nil), client.ownedSession...)
	for namespace := range client.namespaces {
		delete(s.namespaces, namespace)
	}
	delete(s.clients, clientID)
	for _, sessionID := range owned {
		if managed, ok := s.sessions[sessionID]; ok {
			delete(s.sessions, sessionID)
			managed.close()
		}
	}
	for _, inv := range s.invocations {
		if inv.providerClientID != clientID {
			continue
		}
		inv.finish("namespace provider disconnected", "", "")
	}
	s.mu.Unlock()
}

func (s *Server) newSession(clientID uint64) (*managedSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[clientID]
	if !ok {
		return nil, fmt.Errorf("unknown client %d", clientID)
	}
	term := serversession.NewTerm(s.ws, io.Discard, io.Discard)
	managed := &managedSession{
		id:            term.ID(),
		ownerClientID: clientID,
		term:          term,
		lastActive:    time.Now(),
	}
	managed.controllerClient.Store(clientID)
	managed.cond = sync.NewCond(&managed.mu)
	s.sessions[managed.id] = managed
	client.ownedSession = append(client.ownedSession, managed.id)
	return managed, nil
}

func (s *Server) listSessions(clientID uint64) ([]wire.SessionSummary, error) {
	s.mu.Lock()
	sessions := make([]*managedSession, 0, len(s.sessions))
	for _, managed := range s.sessions {
		sessions = append(sessions, managed)
	}
	s.mu.Unlock()

	sort.Slice(sessions, func(i, j int) bool {
		a := sessions[i]
		b := sessions[j]
		if a.lastActive.Equal(b.lastActive) {
			return a.id < b.id
		}
		return a.lastActive.After(b.lastActive)
	})

	out := make([]wire.SessionSummary, 0, len(sessions))
	for _, managed := range sessions {
		out = append(out, managed.summary(clientID))
	}
	return out, nil
}

func (s *Server) registerNamespace(clientID uint64, namespace string) error {
	namespace = normalizeNamespace(namespace)
	if namespace == "" {
		return fmt.Errorf("missing namespace")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[clientID]
	if !ok {
		return fmt.Errorf("unknown client %d", clientID)
	}
	if owner, ok := s.namespaces[namespace]; ok && owner != clientID {
		return fmt.Errorf("namespace %q already registered", namespace)
	}
	s.namespaces[namespace] = clientID
	client.namespaces[namespace] = struct{}{}
	return nil
}

func (s *Server) waitInvocation(clientID uint64) (*invocationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[clientID]
	if !ok {
		return nil, fmt.Errorf("unknown client %d", clientID)
	}
	for len(client.pending) == 0 && !client.closed {
		client.cond.Wait()
	}
	if client.closed {
		return nil, io.EOF
	}
	inv := client.pending[0]
	client.pending = client.pending[1:]
	return inv, nil
}

func (s *Server) beginInvocation(clientID, sessionID uint64, script string) (*invocationState, bool, error) {
	namespace := parseNamespace(script)
	if namespace == "" {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	providerID, ok := s.namespaces[namespace]
	if !ok {
		return nil, false, nil
	}
	if _, ok := s.clients[providerID]; !ok {
		delete(s.namespaces, namespace)
		return nil, false, nil
	}
	managed, ok := s.sessions[sessionID]
	if !ok {
		return nil, true, fmt.Errorf("unknown session %d", sessionID)
	}
	s.nextInvocation++
	inv := &invocationState{
		id:               s.nextInvocation,
		providerClientID: providerID,
		sessionID:        sessionID,
		script:           strings.TrimSpace(script),
	}
	inv.cond = sync.NewCond(&inv.mu)
	s.invocations[inv.id] = inv
	client := s.clients[providerID]
	client.pending = append(client.pending, inv)
	client.cond.Broadcast()
	managed.mu.Lock()
	managed.delegate = &delegatedIO{}
	managed.mu.Unlock()
	return inv, true, nil
}

func (s *Server) handleInvocation(conn io.Writer, frame wire.Frame, inv *invocationState, stdout, stderr *eventWriter) error {
	managed, err := s.lookupSession(inv.sessionID)
	if err != nil {
		return writeError(conn, frame.RequestID, frame.SessionID, err)
	}

	delegateStdout := &eventWriter{
		conn:      conn,
		requestID: frame.RequestID,
		sessionID: managed.id,
		kind:      wire.KindStdoutEvent,
	}
	delegateStderr := &eventWriter{
		conn:      conn,
		requestID: frame.RequestID,
		sessionID: managed.id,
		kind:      wire.KindStderrEvent,
	}
	managed.mu.Lock()
	managed.delegate = &delegatedIO{stdout: delegateStdout, stderr: delegateStderr}
	managed.mu.Unlock()
	defer func() {
		managed.mu.Lock()
		managed.delegate = nil
		managed.mu.Unlock()
		s.mu.Lock()
		delete(s.invocations, inv.id)
		s.mu.Unlock()
	}()

	inv.mu.Lock()
	for !inv.done {
		inv.cond.Wait()
	}
	out := inv.stdout
	diag := inv.stderr
	errText := inv.err
	inv.mu.Unlock()

	if out != "" {
		if _, err := io.WriteString(delegateStdout, out); err != nil {
			return err
		}
	}
	if diag != "" {
		if _, err := io.WriteString(delegateStderr, diag); err != nil {
			return err
		}
	}
	if errText != "" {
		return writeError(conn, frame.RequestID, managed.id, fmt.Errorf("%s", errText))
	}
	return wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.CommandResponse{Continue: true})
}

func (s *Server) finishInvocation(clientID uint64, req *wire.InvocationFinishRequest) error {
	s.mu.Lock()
	inv, ok := s.invocations[req.InvocationID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown invocation %d", req.InvocationID)
	}
	if inv.providerClientID != clientID {
		return fmt.Errorf("invocation %d not owned by client", req.InvocationID)
	}
	inv.finish(req.Err, req.Stdout, req.Stderr)
	return nil
}

func (s *Server) takeSession(sessionID, clientID uint64) error {
	managed, err := s.lookupSession(sessionID)
	if err != nil {
		return err
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.closed.Load() {
		return fmt.Errorf("session %d closed", sessionID)
	}
	controller := managed.controllerClient.Load()
	if controller != managed.ownerClientID && controller != clientID {
		return fmt.Errorf("session %d busy", sessionID)
	}
	managed.controllerClient.Store(clientID)
	managed.cond.Broadcast()
	return nil
}

func (s *Server) returnSession(sessionID, clientID uint64) error {
	managed, err := s.lookupSession(sessionID)
	if err != nil {
		return err
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.closed.Load() {
		return fmt.Errorf("session %d closed", sessionID)
	}
	if managed.controllerClient.Load() != clientID {
		return fmt.Errorf("session %d not controlled by client", sessionID)
	}
	managed.controllerClient.Store(managed.ownerClientID)
	managed.cond.Broadcast()
	return nil
}

func (s *Server) lookupSession(sessionID uint64) (*managedSession, error) {
	if sessionID == 0 {
		return nil, fmt.Errorf("missing session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	managed, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("unknown session %d", sessionID)
	}
	return managed, nil
}

func (s *Server) waitForControlledSession(sessionID, clientID uint64, msg any) (*managedSession, error) {
	managed, err := s.lookupSession(sessionID)
	if err != nil {
		return nil, err
	}
	if _, ok := msg.(*wire.InterruptRequest); ok {
		if !managed.canCancel(clientID) {
			return nil, fmt.Errorf("client %d cannot cancel session %d", clientID, sessionID)
		}
		return managed, nil
	}
	if err := managed.waitForController(clientID); err != nil {
		return nil, err
	}
	return managed, nil
}

func (s *Server) withManagedSession(managed *managedSession, stdout, stderr *eventWriter, fn func(*serversession.TermSession) (bool, error)) (bool, error) {
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.closed.Load() {
		return false, fmt.Errorf("session %d closed", managed.id)
	}
	bindStdout, bindStderr := managed.boundIO(stdout, stderr)
	restore := managed.term.DownloadSession.BindIO(bindStdout, bindStderr)
	defer restore()
	return fn(managed.term)
}

func (s *Server) markSessionActive(sessionID uint64) {
	s.mu.Lock()
	managed := s.sessions[sessionID]
	s.mu.Unlock()
	if managed == nil {
		return
	}
	managed.mu.Lock()
	if !managed.closed.Load() {
		managed.lastActive = time.Now()
	}
	managed.mu.Unlock()
}

func (s *Server) handleSessionCommand(conn io.Writer, clientID uint64, stdout, stderr *eventWriter, frame wire.Frame, cmd sessionCommand) error {
	switch cmd.name {
	case "list":
		summaries, err := s.listSessions(clientID)
		if err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		for _, summary := range summaries {
			flags := "-"
			if summary.Owner {
				flags = "o"
			}
			if summary.Controlled {
				flags += "c"
			}
			if summary.Taken {
				flags += "t"
			}
			if _, err := fmt.Fprintf(stderr, "%d\t%s\t%s\n", summary.ID, flags, summary.CurrentFile); err != nil {
				return err
			}
		}
		return wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: true})
	case "take":
		if err := s.takeSession(cmd.sessionID, clientID); err != nil {
			return writeError(conn, frame.RequestID, cmd.sessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, cmd.sessionID, &wire.CommandResponse{Continue: true})
	case "return":
		if err := s.returnSession(frame.SessionID, clientID); err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: true})
	default:
		return writeError(conn, frame.RequestID, frame.SessionID, fmt.Errorf("unknown session command %q", cmd.name))
	}
}

func parseSessionCommand(script string) (sessionCommand, bool, error) {
	trimmed := strings.TrimSpace(script)
	switch {
	case trimmed == ":sess:list":
		return sessionCommand{name: "list"}, true, nil
	case trimmed == ":sess:return":
		return sessionCommand{name: "return"}, true, nil
	case strings.HasPrefix(trimmed, ":sess:take "):
		text := strings.TrimSpace(strings.TrimPrefix(trimmed, ":sess:take "))
		id, err := strconv.ParseUint(text, 10, 64)
		if err != nil || id == 0 {
			return sessionCommand{}, true, fmt.Errorf("bad session id %q", text)
		}
		return sessionCommand{name: "take", sessionID: id}, true, nil
	default:
		return sessionCommand{}, false, nil
	}
}

func shouldMarkActive(msg any) bool {
	switch msg.(type) {
	case *wire.CommandRequest,
		*wire.OpenFilesRequest,
		*wire.OpenTargetRequest,
		*wire.FocusRequest,
		*wire.AddressRequest,
		*wire.SetDotRequest,
		*wire.ReplaceRequest,
		*wire.UndoRequest,
		*wire.SaveRequest:
		return true
	default:
		return false
	}
}

func normalizeNamespace(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return ""
	}
	for _, r := range namespace {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		return ""
	}
	return strings.ToLower(namespace)
}

func parseNamespace(script string) string {
	trimmed := strings.TrimSpace(script)
	if len(trimmed) < 4 || trimmed[0] != ':' {
		return ""
	}
	rest := trimmed[1:]
	i := strings.IndexByte(rest, ':')
	if i <= 0 {
		return ""
	}
	return normalizeNamespace(rest[:i])
}

func (m *managedSession) canCancel(clientID uint64) bool {
	return !m.closed.Load() && (m.ownerClientID == clientID || m.controllerClient.Load() == clientID)
}

func (m *managedSession) boundIO(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if m != nil && m.delegate != nil {
		if m.delegate.stdout != nil {
			stdout = m.delegate.stdout
		}
		if m.delegate.stderr != nil {
			stderr = m.delegate.stderr
		}
	}
	return stdout, stderr
}

func (m *managedSession) waitForController(clientID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for !m.closed.Load() && m.controllerClient.Load() != clientID {
		m.cond.Wait()
	}
	if m.closed.Load() {
		return fmt.Errorf("session %d closed", m.id)
	}
	return nil
}

func (m *managedSession) close() {
	m.mu.Lock()
	m.closed.Store(true)
	m.cond.Broadcast()
	m.mu.Unlock()
}

func (i *invocationState) finish(errText, stdout, stderr string) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.done {
		return
	}
	i.done = true
	i.err = errText
	i.stdout = stdout
	i.stderr = stderr
	i.cond.Broadcast()
}

func (m *managedSession) summary(clientID uint64) wire.SessionSummary {
	m.mu.Lock()
	defer m.mu.Unlock()

	currentFile := ""
	if !m.closed.Load() {
		if view, err := m.term.CurrentView(); err == nil {
			currentFile = view.Name
		}
	}
	return wire.SessionSummary{
		ID:               m.id,
		Owner:            m.ownerClientID == clientID,
		Controlled:       m.controllerClient.Load() == clientID,
		Taken:            m.controllerClient.Load() != m.ownerClientID,
		CurrentFile:      currentFile,
		LastActiveUnixMs: m.lastActive.UnixMilli(),
	}
}

func (s *Server) Shutdown() error {
	s.mu.Lock()
	listener := s.listener
	conns := make([]io.ReadWriteCloser, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	var shutdownErr error
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			shutdownErr = err
		}
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) && shutdownErr == nil {
			shutdownErr = err
		}
	}
	return shutdownErr
}

func (s *Server) trackConn(conn io.ReadWriteCloser) {
	if s == nil || conn == nil {
		return
	}
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrackConn(conn io.ReadWriteCloser) {
	if s == nil || conn == nil {
		return
	}
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

type eventWriter struct {
	conn      io.Writer
	requestID uint32
	sessionID uint64
	kind      wire.Kind
}

func (w *eventWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var msg wire.Message
	switch w.kind {
	case wire.KindStdoutEvent:
		msg = &wire.StdoutEvent{OutputEvent: wire.OutputEvent{Data: string(p)}}
	case wire.KindStderrEvent:
		msg = &wire.StderrEvent{OutputEvent: wire.OutputEvent{Data: string(p)}}
	default:
		return 0, fmt.Errorf("unsupported event kind %d", w.kind)
	}
	if err := wire.WriteFrame(w.conn, w.requestID, w.sessionID, msg); err != nil {
		return 0, err
	}
	return len(p), nil
}

func writeError(w io.Writer, requestID uint32, sessionID uint64, err error) error {
	msg := err.Error()
	diag := ""
	var reporter diagnosticReporter
	if errors.As(err, &reporter) {
		diag = reporter.Diagnostic()
	}
	return wire.WriteFrame(w, requestID, sessionID, &wire.ErrorResponse{
		Message:        msg,
		DiagnosticText: diag,
	})
}

func isServerQuitCommand(script string) bool {
	switch strings.TrimSpace(script) {
	case "Q", ":ion:Q":
		return true
	default:
		return false
	}
}
