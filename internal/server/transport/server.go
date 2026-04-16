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
	"unicode"

	"ion/internal/core/cmdlang"
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
	namespaces     map[string]registeredNamespace
	invocations    map[uint64]*invocationState
	menuCommands   []wire.MenuCommand
}

type registeredNamespace struct {
	clientID uint64
	doc      wire.NamespaceProviderDoc
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

	mu       sync.Mutex
	cond     *sync.Cond
	done     bool
	canceled bool
	err      string
	stdout   string
	stderr   string
}

type connState struct {
	clientID  uint64
	auxiliary bool
}

type sessionCommand struct {
	name      string
	sessionID uint64
	arg       string
	arg2      string
	arg3      string
}

type colonCommand struct {
	raw     string
	tail    string
	token   string
	rest    string
	hasRest bool
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
		namespaces:   make(map[string]registeredNamespace),
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
	case *wire.CloseSessionRequest:
		if err := s.closeSession(msg.SessionID, state.clientID); err != nil {
			return writeError(conn, frame.RequestID, msg.SessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.NamespaceRegisterRequest:
		if err := s.registerNamespace(state.clientID, msg.Provider); err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.BufferSnapshotsRequest:
		buffers, err := s.ws.BufferSnapshots()
		if err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.BufferSnapshotsMessage{Buffers: buffers})
	case *wire.SessionStatusRequest:
		if err := s.setSessionStatus(msg.Update.SessionID, msg.Update.Status); err != nil {
			return writeError(conn, frame.RequestID, msg.Update.SessionID, err)
		}
		if s.changeNotify != nil {
			s.changeNotify()
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
	case *wire.InvocationCancelWaitRequest:
		canceled, err := s.waitInvocationCancel(state.clientID, msg.InvocationID)
		if err != nil {
			return writeError(conn, frame.RequestID, 0, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.InvocationCancelWaitResponse{Canceled: canceled})
	case *wire.NamespaceDocsRequest:
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.NamespaceDocsResponse{
			Providers: s.listNamespaceDocs(),
		})
	case *wire.DisconnectRequest:
		s.releaseClient(state.clientID, state.auxiliary)
		state.clientID = 0
		return wire.WriteFrame(conn, frame.RequestID, 0, &wire.OKResponse{})
	case *wire.CommandRequest:
		colon, ok, err := parseColonCommand(msg.Script)
		if ok {
			if err != nil {
				return writeError(conn, frame.RequestID, frame.SessionID, err)
			}
			if isServerQuitCommand(colon.raw) {
				if err := wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: false}); err != nil {
					return err
				}
				return s.Shutdown()
			}
			if cmd, ok, err := parseSessionCommand(colon); ok {
				if err != nil {
					return writeError(conn, frame.RequestID, frame.SessionID, err)
				}
				return s.handleSessionCommand(conn, state.clientID, stdout, stderr, frame, cmd)
			}
			if inv, ok, err := s.beginInvocation(state.clientID, frame.SessionID, colon); ok {
				if err != nil {
					return writeError(conn, frame.RequestID, frame.SessionID, err)
				}
				return s.handleInvocation(conn, frame, inv, stdout, stderr)
			}
			return writeError(conn, frame.RequestID, frame.SessionID, fmt.Errorf("unknown command `%s'", colon.raw))
		}
	}

	managed, err := s.waitForControlledSession(frame.SessionID, state.clientID, msg)
	if err != nil {
		return writeError(conn, frame.RequestID, frame.SessionID, err)
	}
	if _, ok := msg.(*wire.InterruptRequest); ok {
		s.cancelInvocationForSession(managed.id)
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
			return false, wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.MenuFilesMessage{
				Files:    files,
				Commands: s.menuSnapshotCommands(),
			})
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

func (s *Server) registerNamespace(clientID uint64, provider wire.NamespaceProviderDoc) error {
	doc, err := normalizeNamespaceProviderDoc(provider)
	if err != nil {
		return err
	}
	if isBuiltinNamespace(doc.Namespace) {
		return fmt.Errorf("namespace %q is reserved", doc.Namespace)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[clientID]
	if !ok {
		return fmt.Errorf("unknown client %d", clientID)
	}
	if owner, ok := s.namespaces[doc.Namespace]; ok && owner.clientID != clientID {
		return fmt.Errorf("namespace %q already registered", doc.Namespace)
	}
	s.namespaces[doc.Namespace] = registeredNamespace{clientID: clientID, doc: doc}
	client.namespaces[doc.Namespace] = struct{}{}
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

func (s *Server) beginInvocation(clientID, sessionID uint64, cmd colonCommand) (*invocationState, bool, error) {
	namespace := parseNamespaceToken(cmd.token)
	if namespace == "" {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.namespaces[namespace]
	if !ok {
		return nil, false, nil
	}
	if _, ok := s.clients[provider.clientID]; !ok {
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
		providerClientID: provider.clientID,
		sessionID:        sessionID,
		script:           cmd.raw,
	}
	inv.cond = sync.NewCond(&inv.mu)
	s.invocations[inv.id] = inv
	client := s.clients[provider.clientID]
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

func (s *Server) setSessionStatus(sessionID uint64, status string) error {
	managed, err := s.lookupSession(sessionID)
	if err != nil {
		return err
	}
	managed.term.SetSessionStatus(status)
	return nil
}

func (s *Server) waitInvocationCancel(clientID, invocationID uint64) (bool, error) {
	s.mu.Lock()
	inv, ok := s.invocations[invocationID]
	s.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("unknown invocation %d", invocationID)
	}
	if inv.providerClientID != clientID {
		return false, fmt.Errorf("invocation %d not owned by client", invocationID)
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	for !inv.canceled && !inv.done {
		inv.cond.Wait()
	}
	return inv.canceled, nil
}

func (s *Server) cancelInvocationForSession(sessionID uint64) bool {
	s.mu.Lock()
	invocations := make([]*invocationState, 0, len(s.invocations))
	for _, inv := range s.invocations {
		if inv.sessionID != sessionID {
			continue
		}
		invocations = append(invocations, inv)
	}
	s.mu.Unlock()
	canceled := false
	for _, inv := range invocations {
		if inv.cancel() {
			canceled = true
		}
	}
	return canceled
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

func (s *Server) closeSession(sessionID, clientID uint64) error {
	s.mu.Lock()
	client, ok := s.clients[clientID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown client %d", clientID)
	}
	managed, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown session %d", sessionID)
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	defer s.mu.Unlock()
	if managed.closed.Load() {
		return fmt.Errorf("session %d closed", sessionID)
	}
	if managed.ownerClientID != clientID {
		return fmt.Errorf("session %d not owned by client", sessionID)
	}
	if controller := managed.controllerClient.Load(); controller != clientID {
		return fmt.Errorf("session %d busy", sessionID)
	}
	if managed.delegate != nil {
		return fmt.Errorf("session %d busy", sessionID)
	}
	delete(s.sessions, sessionID)
	client.ownedSession = removeOwnedSession(client.ownedSession, sessionID)
	managed.closed.Store(true)
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
	case "help":
		if err := s.writeHelp(stderr, cmd.arg); err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: true})
	case "ns-list":
		if err := s.writeNamespaceList(stderr); err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: true})
	case "ns-show":
		if err := s.writeNamespaceShow(stderr, cmd.arg); err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		return wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: true})
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
	case "menuadd":
		if err := s.addMenuCommand(wire.MenuCommand{Command: cmd.arg, Label: cmd.arg2, Shortcut: cmd.arg3}); err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		if s.changeNotify != nil {
			s.changeNotify()
		}
		return wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: true})
	case "menudel":
		s.removeMenuCommand(cmd.arg)
		if s.changeNotify != nil {
			s.changeNotify()
		}
		return wire.WriteFrame(conn, frame.RequestID, frame.SessionID, &wire.CommandResponse{Continue: true})
	case "terminal-only":
		return writeError(conn, frame.RequestID, frame.SessionID, fmt.Errorf("command %q requires the terminal HUD", cmd.arg))
	case "push":
		managed, err := s.waitForControlledSession(frame.SessionID, clientID, &wire.CommandRequest{})
		if err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		notify, err := s.withManagedSession(managed, stdout, stderr, func(sess *serversession.TermSession) (bool, error) {
			_, err := sess.PushTarget(strings.Fields(cmd.arg))
			return true, err
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
		return wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.CommandResponse{Continue: true})
	case "pop":
		managed, err := s.waitForControlledSession(frame.SessionID, clientID, &wire.CommandRequest{})
		if err != nil {
			return writeError(conn, frame.RequestID, frame.SessionID, err)
		}
		notify, err := s.withManagedSession(managed, stdout, stderr, func(sess *serversession.TermSession) (bool, error) {
			_, err := sess.PopNavigation()
			return true, err
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
		return wire.WriteFrame(conn, frame.RequestID, managed.id, &wire.CommandResponse{Continue: true})
	default:
		return writeError(conn, frame.RequestID, frame.SessionID, fmt.Errorf("unknown session command %q", cmd.name))
	}
}

func parseSessionCommand(cmd colonCommand) (sessionCommand, bool, error) {
	arg := strings.TrimSpace(cmd.rest)
	switch cmd.token {
	case "help":
		if !cmd.hasRest {
			return sessionCommand{name: "help"}, true, nil
		}
		if arg == "" {
			return sessionCommand{}, true, fmt.Errorf("help topic expected")
		}
		return sessionCommand{name: "help", arg: arg}, true, nil
	case "ns:list":
		if cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		return sessionCommand{name: "ns-list"}, true, nil
	case "ns:show":
		if !cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		if arg == "" {
			return sessionCommand{}, true, fmt.Errorf("namespace expected")
		}
		return sessionCommand{name: "ns-show", arg: arg}, true, nil
	case "sess:list":
		if cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		return sessionCommand{name: "list"}, true, nil
	case "sess:return":
		if cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		return sessionCommand{name: "return"}, true, nil
	case "sess:take":
		if !cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		id, err := strconv.ParseUint(arg, 10, 64)
		if err != nil || id == 0 {
			return sessionCommand{}, true, fmt.Errorf("bad session id %q", arg)
		}
		return sessionCommand{name: "take", sessionID: id}, true, nil
	case "ion:menuadd":
		if !cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		command, label, shortcut, err := parseMenuAddArgs(arg)
		if err != nil {
			return sessionCommand{}, true, err
		}
		return sessionCommand{name: "menuadd", arg: command, arg2: label, arg3: shortcut}, true, nil
	case "ion:push":
		if !cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		if arg == "" {
			return sessionCommand{}, true, fmt.Errorf("push target expected")
		}
		return sessionCommand{name: "push", arg: arg}, true, nil
	case "ion:pop":
		if cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		return sessionCommand{name: "pop"}, true, nil
	case "term:write", "term:split", "term:cut", "term:snarf", "term:paste", "term:look", "term:regexp", "term:plumb", "term:plumb2":
		if cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		return sessionCommand{name: "terminal-only", arg: ":" + cmd.token}, true, nil
	case "ion:menudel":
		if !cmd.hasRest {
			return sessionCommand{}, false, nil
		}
		if !validMenuCommand(arg) {
			return sessionCommand{}, true, fmt.Errorf("bad menu command %q", arg)
		}
		return sessionCommand{name: "menudel", arg: arg}, true, nil
	default:
		return sessionCommand{}, false, nil
	}
}

func parseColonCommand(script string) (colonCommand, bool, error) {
	trimmed := strings.TrimLeft(script, " \t")
	if !strings.HasPrefix(trimmed, ":") {
		return colonCommand{}, false, nil
	}
	script = normalizeCommandAlias(script)
	parseScript := script
	if !strings.HasSuffix(parseScript, "\n") {
		parseScript += "\n"
	}
	parser := cmdlang.NewParserRunes([]rune(parseScript))
	cmd, err := parser.ParseWithFinal(true)
	if err != nil {
		return colonCommand{}, false, err
	}
	runes := []rune(parseScript)
	if consumed := parser.Consumed(); consumed != len(runes) {
		return colonCommand{}, false, cmdlang.ErrNeedMoreInput
	}
	if cmd == nil || cmd.Cmdc != ':' {
		return colonCommand{}, false, nil
	}
	parsed := colonCommand{raw: strings.TrimSpace(script)}
	if cmd.Text == nil {
		return parsed, true, nil
	}
	parsed.tail = strings.TrimSpace(strings.TrimRight(cmd.Text.UTF8(), "\x00"))
	parsed.raw = ":" + parsed.tail
	parsed.token, parsed.rest, parsed.hasRest = splitColonTail(parsed.tail)
	return parsed, true, nil
}

func splitColonTail(tail string) (token, rest string, hasRest bool) {
	if tail == "" {
		return "", "", false
	}
	for i, r := range tail {
		if r != ' ' && r != '\t' {
			continue
		}
		return tail[:i], tail[i+1:], true
	}
	return tail, "", false
}

func parseNamespaceToken(token string) string {
	i := strings.IndexByte(token, ':')
	if i <= 0 {
		return ""
	}
	return normalizeNamespace(token[:i])
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

func parseCommandReference(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, ":") {
		return "", "", false
	}
	rest := text[1:]
	i := strings.IndexByte(rest, ':')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	namespace := normalizeNamespace(rest[:i])
	command := strings.TrimSpace(rest[i+1:])
	if namespace == "" || !validCommandToken(command) {
		return "", "", false
	}
	return namespace, command, true
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

func parseMenuAddArgs(text string) (command string, label string, shortcut string, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", "", fmt.Errorf("menu command expected")
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", "", fmt.Errorf("menu command expected")
	}
	command = fields[0]
	if !validMenuCommand(command) {
		return "", "", "", fmt.Errorf("bad menu command %q", command)
	}
	rest := strings.TrimSpace(text[len(command):])
	if rest == "" {
		return command, command, "", nil
	}
	shortcut, rest, err = splitMenuAddShortcut(rest)
	if err != nil {
		return "", "", "", err
	}
	if unquoted, unquoteErr := strconv.Unquote(rest); unquoteErr == nil {
		label = strings.TrimSpace(unquoted)
	} else {
		label = strings.TrimSpace(rest)
	}
	if label == "" {
		label = command
	}
	return command, label, shortcut, nil
}

func validMenuCommand(command string) bool {
	command = strings.TrimSpace(command)
	return command != "" && strings.HasPrefix(command, ":")
}

func normalizeMenuCommand(item wire.MenuCommand) (wire.MenuCommand, error) {
	command := strings.TrimSpace(item.Command)
	if !validMenuCommand(command) {
		return wire.MenuCommand{}, fmt.Errorf("bad menu command %q", item.Command)
	}
	label := strings.TrimSpace(item.Label)
	if label == "" {
		label = command
	}
	shortcut, err := normalizeMenuShortcut(item.Shortcut)
	if err != nil {
		return wire.MenuCommand{}, err
	}
	return wire.MenuCommand{
		Command:  command,
		Label:    label,
		Shortcut: shortcut,
	}, nil
}

func (s *Server) addMenuCommand(item wire.MenuCommand) error {
	normalized, err := normalizeMenuCommand(item)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.menuCommands {
		if normalized.Shortcut != "" && s.menuCommands[i].Command != normalized.Command && strings.EqualFold(s.menuCommands[i].Shortcut, normalized.Shortcut) {
			return fmt.Errorf("menu shortcut %q already used by %s", normalized.Shortcut, s.menuCommands[i].Command)
		}
		if s.menuCommands[i].Command != normalized.Command {
			continue
		}
		s.menuCommands[i] = normalized
		return nil
	}
	s.menuCommands = append(s.menuCommands, normalized)
	return nil
}

func splitMenuAddShortcut(rest string) (shortcut, label string, err error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", nil
	}
	if rest[0] == '"' || rest[0] == '`' {
		quoted, tail, ok := splitQuotedPrefix(rest)
		if !ok {
			return "", "", fmt.Errorf("bad menu label %q", rest)
		}
		tail = strings.TrimSpace(tail)
		if tail == "" {
			return "", quoted, nil
		}
		shortcut, err := normalizeMenuShortcut(tail)
		if err != nil {
			return "", "", err
		}
		return shortcut, quoted, nil
	}
	fields := strings.Fields(rest)
	if len(fields) == 1 {
		if shortcut, err := normalizeMenuShortcut(fields[0]); err == nil {
			return shortcut, "", nil
		}
		return "", rest, nil
	}
	last := fields[len(fields)-1]
	shortcut, err = normalizeMenuShortcut(last)
	if err != nil {
		return "", rest, nil
	}
	idx := strings.LastIndex(rest, last)
	if idx < 0 {
		return "", "", fmt.Errorf("bad menuadd args %q", rest)
	}
	return shortcut, strings.TrimSpace(rest[:idx]), nil
}

func splitQuotedPrefix(text string) (quoted string, tail string, ok bool) {
	if text == "" {
		return "", "", false
	}
	for i := 1; i <= len(text); i++ {
		unquoted, err := strconv.Unquote(text[:i])
		if err != nil {
			continue
		}
		return unquoted, text[i:], true
	}
	return "", "", false
}

func normalizeMenuShortcut(shortcut string) (string, error) {
	shortcut = strings.TrimSpace(shortcut)
	if shortcut == "" {
		return "", nil
	}
	runes := []rune(shortcut)
	if len(runes) != 1 || !unicode.IsLetter(runes[0]) {
		return "", fmt.Errorf("bad menu shortcut %q", shortcut)
	}
	return string(unicode.ToLower(runes[0])), nil
}

func (s *Server) removeMenuCommand(command string) {
	command = strings.TrimSpace(command)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.menuCommands {
		if s.menuCommands[i].Command != command {
			continue
		}
		s.menuCommands = append(s.menuCommands[:i], s.menuCommands[i+1:]...)
		return
	}
}

func (s *Server) menuSnapshotCommands() []wire.MenuCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.MenuCommand(nil), s.menuCommands...)
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

type commandHelpDoc struct {
	usage    string
	summary  string
	help     string
	commands []wire.NamespaceCommandDoc
}

func (s *Server) writeHelp(w io.Writer, target string) error {
	doc, err := s.lookupHelp(strings.TrimSpace(target))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, doc.usage); err != nil {
		return err
	}
	if doc.summary != "" {
		if _, err := fmt.Fprintf(w, "Summary: %s\n", doc.summary); err != nil {
			return err
		}
	}
	if doc.help != "" {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, doc.help); err != nil {
			return err
		}
	}
	if len(doc.commands) > 0 {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "Commands:"); err != nil {
			return err
		}
		for _, cmd := range doc.commands {
			if _, err := fmt.Fprintf(w, ":%s\t%s\n", doc.usage[1:]+":"+cmd.Name, cmd.Summary); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) writeNamespaceList(w io.Writer) error {
	for _, doc := range s.listNamespaceDocs() {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", doc.Namespace, doc.Summary); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) writeNamespaceShow(w io.Writer, target string) error {
	namespace := normalizeNamespaceQuery(target)
	if namespace == "" {
		return fmt.Errorf("bad namespace %q", target)
	}
	doc, ok := s.lookupNamespaceDoc(namespace)
	if !ok {
		return fmt.Errorf("unknown namespace %q", namespace)
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\n", doc.Namespace, doc.Summary); err != nil {
		return err
	}
	for _, cmd := range doc.Commands {
		if _, err := fmt.Fprintf(w, ":%s:%s\t%s\n", doc.Namespace, cmd.Name, cmd.Summary); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) lookupHelp(target string) (commandHelpDoc, error) {
	if target == "" || target == ":help" {
		return builtinHelpRootDoc(), nil
	}
	target = normalizeCommandAlias(target)
	if doc, ok := builtinCommandDoc(target); ok {
		return doc, nil
	}
	namespace, command, ok := parseCommandReference(target)
	if ok {
		provider, ok := s.lookupNamespaceDoc(namespace)
		if !ok {
			return commandHelpDoc{}, fmt.Errorf("unknown help topic %q", target)
		}
		for _, cmd := range provider.Commands {
			if cmd.Name != command {
				continue
			}
			usage := ":" + provider.Namespace + ":" + cmd.Name
			if args := strings.TrimSpace(cmd.Args); args != "" {
				usage += " " + args
			}
			return commandHelpDoc{
				usage:   usage,
				summary: cmd.Summary,
				help:    cmd.Help,
			}, nil
		}
		return commandHelpDoc{}, fmt.Errorf("unknown help topic %q", target)
	}
	if namespace := normalizeNamespaceQuery(target); namespace != "" {
		if provider, ok := s.lookupNamespaceDoc(namespace); ok {
			return commandHelpDoc{
				usage:    ":" + provider.Namespace,
				summary:  provider.Summary,
				help:     provider.Help,
				commands: append([]wire.NamespaceCommandDoc(nil), provider.Commands...),
			}, nil
		}
	}
	return commandHelpDoc{}, fmt.Errorf("unknown help topic %q", target)
}

func (s *Server) lookupNamespaceDoc(namespace string) (wire.NamespaceProviderDoc, bool) {
	for _, doc := range builtinNamespaceDocs() {
		if doc.Namespace == namespace {
			return doc, true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	registered, ok := s.namespaces[namespace]
	if !ok {
		return wire.NamespaceProviderDoc{}, false
	}
	return registered.doc, true
}

func (s *Server) listNamespaceDocs() []wire.NamespaceProviderDoc {
	docs := builtinNamespaceDocs()
	s.mu.Lock()
	for _, registered := range s.namespaces {
		docs = append(docs, registered.doc)
	}
	s.mu.Unlock()
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Namespace < docs[j].Namespace
	})
	return docs
}

func builtinHelpRootDoc() commandHelpDoc {
	return commandHelpDoc{
		usage:   ":help [command]",
		summary: "show detailed help for a command",
		help:    "Displays the long-form help text for one built-in or provider command. Use :ns:list to see available namespaces, then :ns:show <namespace> to list commands.",
	}
}

func builtinCommandDoc(target string) (commandHelpDoc, bool) {
	switch target {
	case ":help":
		return builtinHelpRootDoc(), true
	case ":ns:list":
		return commandHelpDoc{
			usage:   ":ns:list",
			summary: "list registered namespaces",
			help:    "Prints each built-in or provider namespace together with its short description.",
		}, true
	case ":ns:show":
		return commandHelpDoc{
			usage:   ":ns:show <namespace>",
			summary: "list commands in one namespace",
			help:    "Prints the namespace summary followed by each documented command in that namespace and its short description. The namespace argument may be plain like demolsp or prefixed like :demolsp.",
		}, true
	case ":sess:list":
		return commandHelpDoc{
			usage:   ":sess:list",
			summary: "list live sessions",
			help:    "Prints the visible live sessions, including ownership/control flags and the current file for each session.",
		}, true
	case ":sess:take":
		return commandHelpDoc{
			usage:   ":sess:take <session-id>",
			summary: "take temporary control of a session",
			help:    "Transfers control of one live session to the current client until :sess:return is used.",
		}, true
	case ":sess:return":
		return commandHelpDoc{
			usage:   ":sess:return",
			summary: "return a taken session to its owner",
			help:    "Returns control of the current taken session to its owner and restores the previous session selection on the client.",
		}, true
	case ":ion:Q":
		return commandHelpDoc{
			usage:   ":ion:Q",
			summary: "shut down the current ion server",
			help:    "Stops the current ion server and disconnects all attached clients.",
		}, true
	case ":ion:menuadd":
		return commandHelpDoc{
			usage:   `:ion:menuadd <command> ["label"] [letter]`,
			summary: "add a shared custom context-menu item",
			help:    "Registers one server-global menu item for all attached clients. The first argument is the command to run when the item is selected. The optional label defaults to the command text. The optional final single-letter shortcut binds the item to M-<letter> in the keyboard menu.",
		}, true
	case ":ion:push":
		return commandHelpDoc{
			usage:   ":ion:push <b syntax>",
			summary: "push the current location, then navigate",
			help:    "Records the current session location in the navigation stack, then opens the destination described using B-style target syntax such as path, path:line, or path:/regexp/.",
		}, true
	case ":ion:pop":
		return commandHelpDoc{
			usage:   ":ion:pop",
			summary: "pop the navigation stack",
			help:    "Navigates to the previous recorded navigation entry and removes the popped entry. If the current stack position is not at the top, forward entries are discarded.",
		}, true
	case ":ion:menudel":
		return commandHelpDoc{
			usage:   ":ion:menudel <command>",
			summary: "remove a shared custom context-menu item",
			help:    "Removes one previously registered server-global menu item identified by its command text.",
		}, true
	case ":term:write":
		return commandHelpDoc{
			usage:   ":term:write",
			summary: "save the current buffer",
			help:    "Terminal HUD command that saves the current buffer using the same path as the context-menu write action.",
		}, true
	case ":term:cut":
		return commandHelpDoc{
			usage:   ":term:cut",
			summary: "cut the current selection",
			help:    "Terminal HUD command that cuts the current selection into the snarf buffer and clipboard.",
		}, true
	case ":term:snarf":
		return commandHelpDoc{
			usage:   ":term:snarf",
			summary: "copy the current selection",
			help:    "Terminal HUD command that copies the current selection into the snarf buffer and clipboard.",
		}, true
	case ":term:paste":
		return commandHelpDoc{
			usage:   ":term:paste",
			summary: "paste the current snarf buffer",
			help:    "Terminal HUD command that pastes the current snarf buffer at the current selection or cursor.",
		}, true
	case ":term:tmux":
		return commandHelpDoc{
			usage:   ":term:tmux",
			summary: "exchange the snarf buffer with tmux",
			help:    "Terminal HUD command that exchanges the terminal snarf buffer with the current tmux paste buffer. If tmux has no paste buffer yet, it is treated as empty.",
		}, true
	case ":term:send":
		return commandHelpDoc{
			usage:   ":term:send",
			summary: "send dot or snarf to the command window",
			help:    "Terminal HUD command that sends the current selection to the command window as if typed there, or uses the snarf buffer if the selection is empty. The sent text becomes the new snarf buffer.",
		}, true
	case ":term:pick":
		return commandHelpDoc{
			usage:   ":term:pick <unix command>",
			summary: "run a shell command and pick one plumb target per line",
			help:    "Terminal HUD command that runs one shell command as with !, then treats each stdout or stderr line as one B-style plumb target and opens a picker over the results.",
		}, true
	case ":term:look":
		return commandHelpDoc{
			usage:   ":term:look",
			summary: "find the current selection or token",
			help:    "Terminal HUD command that searches forward for the current selection, or the token under the cursor if there is no selection.",
		}, true
	case ":term:regexp":
		return commandHelpDoc{
			usage:   ":term:regexp",
			summary: "repeat the previous regexp search",
			help:    "Terminal HUD command that re-runs the most recently used sam regexp search pattern.",
		}, true
	case ":term:plumb":
		return commandHelpDoc{
			usage:   ":term:plumb",
			summary: "open the current token as a target",
			help:    "Terminal HUD command that opens the current selection or token under the cursor using B-style target plumbing and pushes the destination onto the navigation stack.",
		}, true
	case ":term:plumb2":
		return commandHelpDoc{
			usage:   ":term:plumb2",
			summary: "open the current token in another session",
			help:    "Terminal HUD command that opens the current selection or token under the cursor in the next-most-recent resident session. If no other session is available, it opens a new attached pane as in ion -N.",
		}, true
	case ":term:split":
		return commandHelpDoc{
			usage:   ":term:split",
			summary: "open a new attached pane for the current file",
			help:    "Terminal HUD command that opens a new attached pane as in ion -N. If the current buffer names a file, the new pane opens that file.",
		}, true
	default:
		return commandHelpDoc{}, false
	}
}

func builtinNamespaceDocs() []wire.NamespaceProviderDoc {
	return []wire.NamespaceProviderDoc{
		{
			Namespace: "ion",
			Summary:   "core ion server commands",
			Help:      "Built-in commands implemented directly by the ion server.",
			Commands: []wire.NamespaceCommandDoc{
				{
					Name:    "Q",
					Summary: "shut down the current ion server",
					Help:    "Stops the current ion server and disconnects all attached clients.",
				},
				{
					Name:    "menuadd",
					Args:    `<command> ["label"] [letter]`,
					Summary: "add a shared custom context-menu item",
					Help:    "Registers one server-global menu item for all attached clients. The optional label defaults to the command text. The optional final single-letter shortcut binds the item to M-<letter> in the keyboard menu.",
				},
				{
					Name:    "push",
					Args:    "<b syntax>",
					Summary: "push the current location, then navigate",
					Help:    "Records the current session location in the navigation stack, then opens the destination described using B-style target syntax such as path, path:line, or path:/regexp/.",
				},
				{
					Name:    "pop",
					Summary: "pop the navigation stack",
					Help:    "Navigates to the previous recorded navigation entry and removes the popped entry. If the current stack position is not at the top, forward entries are discarded.",
				},
				{
					Name:    "menudel",
					Args:    "<command>",
					Summary: "remove a shared custom context-menu item",
					Help:    "Removes one previously registered server-global menu item identified by its command text.",
				},
			},
		},
		{
			Namespace: "term",
			Summary:   "terminal HUD commands",
			Help:      "Commands implemented locally by the interactive terminal HUD. These commands depend on terminal state such as the current selection, token under the cursor, snarf buffer, or tmux pane context.",
			Commands: []wire.NamespaceCommandDoc{
				{
					Name:    "write",
					Summary: "save the current buffer",
					Help:    "Terminal HUD command that saves the current buffer using the same path as the context-menu write action.",
				},
				{
					Name:    "cut",
					Summary: "cut the current selection",
					Help:    "Terminal HUD command that cuts the current selection into the snarf buffer and clipboard.",
				},
				{
					Name:    "snarf",
					Summary: "copy the current selection",
					Help:    "Terminal HUD command that copies the current selection into the snarf buffer and clipboard.",
				},
				{
					Name:    "paste",
					Summary: "paste the current snarf buffer",
					Help:    "Terminal HUD command that pastes the current snarf buffer at the current selection or cursor.",
				},
				{
					Name:    "tmux",
					Summary: "exchange the snarf buffer with tmux",
					Help:    "Terminal HUD command that exchanges the terminal snarf buffer with the current tmux paste buffer. If tmux has no paste buffer yet, it is treated as empty.",
				},
				{
					Name:    "send",
					Summary: "send dot or snarf to the command window",
					Help:    "Terminal HUD command that sends the current selection to the command window as if typed there, or uses the snarf buffer if the selection is empty. The sent text becomes the new snarf buffer.",
				},
				{
					Name:    "look",
					Summary: "find the current selection or token",
					Help:    "Terminal HUD command that searches forward for the current selection, or the token under the cursor if there is no selection.",
				},
				{
					Name:    "regexp",
					Summary: "repeat the previous regexp search",
					Help:    "Terminal HUD command that re-runs the most recently used sam regexp search pattern.",
				},
				{
					Name:    "plumb",
					Summary: "open the current token as a target",
					Help:    "Terminal HUD command that opens the current selection or token under the cursor using B-style target plumbing and pushes the destination onto the navigation stack.",
				},
				{
					Name:    "plumb2",
					Summary: "open the current token in another session",
					Help:    "Terminal HUD command that opens the current selection or token under the cursor in the next-most-recent resident session. If no other session is available, it opens a new attached pane as in ion -N.",
				},
				{
					Name:    "split",
					Summary: "open a new attached pane for the current file",
					Help:    "Terminal HUD command that opens a new attached pane as in ion -N. If the current buffer names a file, the new pane opens that file.",
				},
			},
		},
		{
			Namespace: "ns",
			Summary:   "namespace discovery commands",
			Help:      "Built-in commands for discovering registered namespaces and their documented commands.",
			Commands: []wire.NamespaceCommandDoc{
				{
					Name:    "list",
					Summary: "list registered namespaces",
					Help:    "Prints each built-in or provider namespace together with its short description.",
				},
				{
					Name:    "show",
					Args:    "<namespace>",
					Summary: "list commands in one namespace",
					Help:    "Prints the namespace summary followed by each documented command in that namespace and its short description.",
				},
			},
		},
		{
			Namespace: "sess",
			Summary:   "session inspection and control",
			Help:      "Built-in commands for listing sessions and taking or returning session control.",
			Commands: []wire.NamespaceCommandDoc{
				{
					Name:    "list",
					Summary: "list live sessions",
					Help:    "Prints the visible live sessions, including ownership/control flags and the current file for each session.",
				},
				{
					Name:    "take",
					Args:    "<session-id>",
					Summary: "take temporary control of a session",
					Help:    "Transfers control of one live session to the current client until :sess:return is used.",
				},
				{
					Name:    "return",
					Summary: "return a taken session to its owner",
					Help:    "Returns control of the current taken session to its owner and restores the previous session selection on the client.",
				},
			},
		},
	}
}

func isBuiltinNamespace(namespace string) bool {
	for _, doc := range builtinNamespaceDocs() {
		if doc.Namespace == namespace {
			return true
		}
	}
	return false
}

func normalizeNamespaceProviderDoc(doc wire.NamespaceProviderDoc) (wire.NamespaceProviderDoc, error) {
	doc.Namespace = normalizeNamespace(doc.Namespace)
	if doc.Namespace == "" {
		return wire.NamespaceProviderDoc{}, fmt.Errorf("missing namespace")
	}
	seen := make(map[string]struct{}, len(doc.Commands))
	for i := range doc.Commands {
		name := strings.TrimSpace(doc.Commands[i].Name)
		if !validCommandToken(name) {
			return wire.NamespaceProviderDoc{}, fmt.Errorf("bad command name %q", doc.Commands[i].Name)
		}
		if _, ok := seen[name]; ok {
			return wire.NamespaceProviderDoc{}, fmt.Errorf("duplicate command name %q", name)
		}
		seen[name] = struct{}{}
		doc.Commands[i].Name = name
		doc.Commands[i].Args = strings.TrimSpace(doc.Commands[i].Args)
		doc.Commands[i].Summary = strings.TrimSpace(doc.Commands[i].Summary)
		doc.Commands[i].Help = strings.TrimSpace(doc.Commands[i].Help)
	}
	doc.Summary = strings.TrimSpace(doc.Summary)
	doc.Help = strings.TrimSpace(doc.Help)
	return doc, nil
}

func validCommandToken(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func normalizeNamespaceQuery(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, ":")
	if i := strings.IndexByte(text, ':'); i >= 0 {
		text = text[:i]
	}
	return normalizeNamespace(text)
}

func normalizeCommandAlias(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "::") {
		return text
	}
	return ":ion:" + text[2:]
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

func removeOwnedSession(ids []uint64, sessionID uint64) []uint64 {
	if len(ids) == 0 {
		return nil
	}
	out := ids[:0]
	for _, id := range ids {
		if id != sessionID {
			out = append(out, id)
		}
	}
	return out
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

func (i *invocationState) cancel() bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.done || i.canceled {
		return false
	}
	i.canceled = true
	i.cond.Broadcast()
	return true
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
