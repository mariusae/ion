package session

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"ion/internal/proto/wire"
)

// Client is a wire-backed client/session adapter shared by all client modes.
type Client struct {
	conn      io.ReadWriteCloser
	stdout    io.Writer
	stderr    io.Writer
	mu        sync.Mutex
	nextReqID uint32
	clientID  uint64
	connected bool
	sessionID uint64
	takeStack []uint64
}

var _ wire.TermService = (*Client)(nil)

// New constructs a client around an existing transport connection.
func New(conn io.ReadWriteCloser, stdout, stderr io.Writer) *Client {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &Client{
		conn:   conn,
		stdout: stdout,
		stderr: stderr,
	}
}

// DialUnix connects one client session to a local ion unix socket.
func DialUnix(path string, stdout, stderr io.Writer) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return New(conn, stdout, stderr), nil
}

// DialUnixAs connects one client transport channel to an existing logical client id.
func DialUnixAs(path string, clientID uint64, stdout, stderr io.Writer) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	client := New(conn, stdout, stderr)
	client.clientID = clientID
	return client, nil
}

// Close tears down the client connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	c.mu.Lock()
	if c.connected {
		c.nextReqID++
		_ = wire.WriteFrame(c.conn, c.nextReqID, 0, &wire.DisconnectRequest{})
		c.connected = false
		c.sessionID = 0
		c.takeStack = nil
	}
	c.mu.Unlock()
	return c.conn.Close()
}

// ID reports the logical client id after the first successful request.
func (c *Client) ID() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clientID
}

// Session is one explicit owner-backed server session handle.
type Session struct {
	client *Client
	id     uint64
}

// ID reports the stable server session id.
func (s *Session) ID() uint64 {
	if s == nil {
		return 0
	}
	return s.id
}

// Session returns one explicit session handle for a known server id.
func (c *Client) Session(id uint64) *Session {
	if c == nil || id == 0 {
		return nil
	}
	return &Session{client: c, id: id}
}

// CurrentSession returns the client's current default session handle, if any.
func (c *Client) CurrentSession() *Session {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionID == 0 {
		return nil
	}
	return &Session{client: c, id: c.sessionID}
}

// NewSession allocates one new owner-backed session for this client.
func (c *Client) NewSession() (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return nil, err
	}
	_, msg, err := c.roundTripLocked(0, &wire.NewSessionRequest{})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*wire.NewSessionResponse)
	if !ok {
		return nil, fmt.Errorf("new-session response type %T, want *wire.NewSessionResponse", msg)
	}
	if c.sessionID == 0 {
		c.sessionID = resp.SessionID
	}
	return &Session{client: c, id: resp.SessionID}, nil
}

// ListSessions returns the currently visible live sessions.
func (c *Client) ListSessions() ([]wire.SessionSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return nil, err
	}
	_, msg, err := c.roundTripLocked(0, &wire.SessionListRequest{})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*wire.SessionListResponse)
	if !ok {
		return nil, fmt.Errorf("session-list response type %T, want *wire.SessionListResponse", msg)
	}
	return append([]wire.SessionSummary(nil), resp.Sessions...), nil
}

// RegisterNamespace claims one delegated command namespace for this client.
func (c *Client) RegisterNamespace(namespace string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return err
	}
	if _, msg, err := c.roundTripLocked(0, &wire.NamespaceRegisterRequest{Namespace: namespace}); err != nil {
		return err
	} else if _, ok := msg.(*wire.OKResponse); !ok {
		return fmt.Errorf("namespace-register response type %T, want *wire.OKResponse", msg)
	}
	return nil
}

// WaitInvocation blocks until one delegated namespace invocation is available.
func (c *Client) WaitInvocation() (wire.Invocation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return wire.Invocation{}, err
	}
	_, msg, err := c.roundTripLocked(0, &wire.InvocationWaitRequest{})
	if err != nil {
		return wire.Invocation{}, err
	}
	resp, ok := msg.(*wire.InvocationWaitResponse)
	if !ok {
		return wire.Invocation{}, fmt.Errorf("invocation-wait response type %T, want *wire.InvocationWaitResponse", msg)
	}
	return resp.Invocation, nil
}

// FinishInvocation completes one delegated namespace invocation.
func (c *Client) FinishInvocation(id uint64, errText, stdout, stderr string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return err
	}
	req := &wire.InvocationFinishRequest{
		InvocationID: id,
		Err:          errText,
		Stdout:       stdout,
		Stderr:       stderr,
	}
	if _, msg, err := c.roundTripLocked(0, req); err != nil {
		return err
	} else if _, ok := msg.(*wire.OKResponse); !ok {
		return fmt.Errorf("invocation-finish response type %T, want *wire.OKResponse", msg)
	}
	return nil
}

// Take temporarily transfers control of one live session to this client.
func (c *Client) Take(id uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return err
	}
	if _, msg, err := c.roundTripLocked(0, &wire.TakeSessionRequest{SessionID: id}); err != nil {
		return err
	} else if _, ok := msg.(*wire.OKResponse); !ok {
		return fmt.Errorf("take-session response type %T, want *wire.OKResponse", msg)
	}
	c.takeStack = append(c.takeStack, c.sessionID)
	c.sessionID = id
	return nil
}

// Return returns one taken session to its owner.
func (c *Client) Return(id uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return err
	}
	if _, msg, err := c.roundTripLocked(0, &wire.ReturnSessionRequest{SessionID: id}); err != nil {
		return err
	} else if _, ok := msg.(*wire.OKResponse); !ok {
		return fmt.Errorf("return-session response type %T, want *wire.OKResponse", msg)
	}
	if c.sessionID == id {
		if n := len(c.takeStack); n > 0 {
			c.sessionID = c.takeStack[n-1]
			c.takeStack = c.takeStack[:n-1]
		} else {
			c.sessionID = 0
		}
	}
	return nil
}

// Bootstrap loads the initial file set.
func (c *Client) Bootstrap(files []string) error {
	_, msg, err := c.roundTripDefault(&wire.BootstrapRequest{Files: files})
	if err != nil {
		return err
	}
	if _, ok := msg.(*wire.OKResponse); !ok {
		return fmt.Errorf("bootstrap response type %T, want *wire.OKResponse", msg)
	}
	return nil
}

// Execute runs one command script.
func (c *Client) Execute(script string) (bool, error) {
	script = normalizeIonNamespaceAlias(script)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return false, err
	}
	control, isControl := parseSessionControlScript(script)
	if !isControl {
		if err := c.ensureDefaultSessionLocked(); err != nil {
			return false, err
		}
	}
	_, msg, err := c.roundTripLocked(c.sessionID, &wire.CommandRequest{Script: script})
	if err != nil {
		return false, err
	}
	resp, ok := msg.(*wire.CommandResponse)
	if !ok {
		return false, fmt.Errorf("command response type %T, want *wire.CommandResponse", msg)
	}
	if isControl {
		switch control.name {
		case "take":
			c.takeStack = append(c.takeStack, c.sessionID)
			c.sessionID = control.sessionID
		case "return":
			if n := len(c.takeStack); n > 0 {
				c.sessionID = c.takeStack[n-1]
				c.takeStack = c.takeStack[:n-1]
			} else {
				c.sessionID = 0
			}
		}
	}
	return resp.Continue, nil
}

// Interrupt asks the server to interrupt one currently running external command.
func (c *Client) Interrupt() error {
	_, msg, err := c.roundTripDefault(&wire.InterruptRequest{})
	if err != nil {
		return err
	}
	if _, ok := msg.(*wire.OKResponse); !ok {
		return fmt.Errorf("interrupt response type %T, want *wire.OKResponse", msg)
	}
	return nil
}

// CurrentView returns the current buffer snapshot.
func (c *Client) CurrentView() (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.CurrentViewRequest{})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("current-view response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// OpenFiles opens one explicit file list in the shared workspace.
func (c *Client) OpenFiles(files []string) (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.OpenFilesRequest{Files: files})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("open-files response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// OpenTarget opens one file target and applies its final address as one logical operation.
func (c *Client) OpenTarget(path, address string) (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.OpenTargetRequest{Path: path, Address: address})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("open-target response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// MenuFiles returns the current file-menu snapshot.
func (c *Client) MenuFiles() ([]wire.MenuFile, error) {
	_, msg, err := c.roundTripDefault(&wire.MenuFilesRequest{})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*wire.MenuFilesMessage)
	if !ok {
		return nil, fmt.Errorf("menu-files response type %T, want *wire.MenuFilesMessage", msg)
	}
	return append([]wire.MenuFile(nil), resp.Files...), nil
}

// NavigationStack returns the current per-client navigation history.
func (c *Client) NavigationStack() (wire.NavigationStack, error) {
	_, msg, err := c.roundTripDefault(&wire.NavigationStackRequest{})
	if err != nil {
		return wire.NavigationStack{}, err
	}
	resp, ok := msg.(*wire.NavigationStackMessage)
	if !ok {
		return wire.NavigationStack{}, fmt.Errorf("navigation-stack response type %T, want *wire.NavigationStackMessage", msg)
	}
	return resp.Stack, nil
}

// FocusFile changes the current file selection.
func (c *Client) FocusFile(id int) (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.FocusRequest{ID: id})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("focus response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// SetAddress resolves one sam address against the current file.
func (c *Client) SetAddress(expr string) (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.AddressRequest{Expr: expr})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("address response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// SetDot updates the current selection range.
func (c *Client) SetDot(start, end int) (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.SetDotRequest{Start: start, End: end})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("set-dot response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// Replace applies one edit to the current file.
func (c *Client) Replace(start, end int, text string) (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.ReplaceRequest{Start: start, End: end, Text: text})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("replace response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// Undo reverts the most recent edit in the current file.
func (c *Client) Undo() (wire.BufferView, error) {
	_, msg, err := c.roundTripDefault(&wire.UndoRequest{})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("undo response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// Save writes the current file to disk.
func (c *Client) Save() (string, error) {
	_, msg, err := c.roundTripDefault(&wire.SaveRequest{})
	if err != nil {
		return "", err
	}
	resp, ok := msg.(*wire.SaveResponse)
	if !ok {
		return "", fmt.Errorf("save response type %T, want *wire.SaveResponse", msg)
	}
	return resp.Status, nil
}

// Execute runs one command script against the explicit session handle.
func (s *Session) Execute(script string) (bool, error) {
	if s == nil || s.client == nil || s.id == 0 {
		return false, fmt.Errorf("nil session")
	}
	script = normalizeIonNamespaceAlias(script)
	_, msg, err := s.client.roundTripForSession(s.id, &wire.CommandRequest{Script: script})
	if err != nil {
		return false, err
	}
	resp, ok := msg.(*wire.CommandResponse)
	if !ok {
		return false, fmt.Errorf("command response type %T, want *wire.CommandResponse", msg)
	}
	return resp.Continue, nil
}

// Cancel interrupts the current operation on the explicit session handle.
func (s *Session) Cancel() error {
	if s == nil || s.client == nil || s.id == 0 {
		return fmt.Errorf("nil session")
	}
	_, msg, err := s.client.roundTripForSession(s.id, &wire.InterruptRequest{})
	if err != nil {
		return err
	}
	if _, ok := msg.(*wire.OKResponse); !ok {
		return fmt.Errorf("interrupt response type %T, want *wire.OKResponse", msg)
	}
	return nil
}

// CurrentView returns the buffer snapshot for the explicit session handle.
func (s *Session) CurrentView() (wire.BufferView, error) {
	if s == nil || s.client == nil || s.id == 0 {
		return wire.BufferView{}, fmt.Errorf("nil session")
	}
	_, msg, err := s.client.roundTripForSession(s.id, &wire.CurrentViewRequest{})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("current-view response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

func (c *Client) roundTripDefault(req wire.Message) (wire.Frame, any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return wire.Frame{}, nil, err
	}
	if err := c.ensureDefaultSessionLocked(); err != nil {
		return wire.Frame{}, nil, err
	}
	frame, msg, err := c.roundTripLocked(c.sessionID, req)
	if err == nil {
		c.sessionID = frame.SessionID
	}
	return frame, msg, err
}

func (c *Client) roundTripForSession(sessionID uint64, req wire.Message) (wire.Frame, any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnectedLocked(); err != nil {
		return wire.Frame{}, nil, err
	}
	return c.roundTripLocked(sessionID, req)
}

func (c *Client) ensureConnectedLocked() error {
	if c.connected {
		return nil
	}
	_, msg, err := c.roundTripLocked(0, &wire.ConnectRequest{ClientID: c.clientID})
	if err != nil {
		return err
	}
	resp, ok := msg.(*wire.ConnectResponse)
	if !ok {
		return fmt.Errorf("connect response type %T, want *wire.ConnectResponse", msg)
	}
	c.clientID = resp.ClientID
	c.connected = true
	return nil
}

func (c *Client) ensureDefaultSessionLocked() error {
	if c.sessionID != 0 {
		return nil
	}
	_, msg, err := c.roundTripLocked(0, &wire.NewSessionRequest{})
	if err != nil {
		return err
	}
	resp, ok := msg.(*wire.NewSessionResponse)
	if !ok {
		return fmt.Errorf("new-session response type %T, want *wire.NewSessionResponse", msg)
	}
	c.sessionID = resp.SessionID
	return nil
}

func (c *Client) roundTripLocked(sessionID uint64, req wire.Message) (wire.Frame, any, error) {

	c.nextReqID++
	reqID := c.nextReqID
	if err := wire.WriteFrame(c.conn, reqID, sessionID, req); err != nil {
		return wire.Frame{}, nil, err
	}

	for {
		frame, err := wire.ReadFrame(c.conn)
		if err != nil {
			return wire.Frame{}, nil, err
		}
		msg, err := wire.DecodeMessage(frame)
		if err != nil {
			return wire.Frame{}, nil, err
		}
		if frame.RequestID != reqID {
			return wire.Frame{}, nil, fmt.Errorf("unexpected response id %d, want %d", frame.RequestID, reqID)
		}

		switch frame.Kind {
		case wire.KindStdoutEvent:
			event, ok := msg.(*wire.OutputEvent)
			if !ok {
				return wire.Frame{}, nil, fmt.Errorf("stdout event type %T, want *wire.OutputEvent", msg)
			}
			if _, err := io.WriteString(c.stdout, event.Data); err != nil {
				return wire.Frame{}, nil, err
			}
			continue
		case wire.KindStderrEvent:
			event, ok := msg.(*wire.OutputEvent)
			if !ok {
				return wire.Frame{}, nil, fmt.Errorf("stderr event type %T, want *wire.OutputEvent", msg)
			}
			if _, err := io.WriteString(c.stderr, event.Data); err != nil {
				return wire.Frame{}, nil, err
			}
			continue
		case wire.KindErrorResponse:
			errResp, ok := msg.(*wire.ErrorResponse)
			if !ok {
				return wire.Frame{}, nil, fmt.Errorf("error response type %T, want *wire.ErrorResponse", msg)
			}
			return frame, nil, errResp
		default:
			return frame, msg, nil
		}
	}
}

func isSessionControlScript(script string) bool {
	trimmed := strings.TrimSpace(script)
	return trimmed == ":sess:list" || strings.HasPrefix(trimmed, ":sess:take ") || trimmed == ":sess:return"
}

type sessionControlScript struct {
	name      string
	sessionID uint64
}

func parseSessionControlScript(script string) (sessionControlScript, bool) {
	trimmed := strings.TrimSpace(script)
	switch {
	case trimmed == ":sess:list":
		return sessionControlScript{name: "list"}, true
	case trimmed == ":sess:return":
		return sessionControlScript{name: "return"}, true
	case strings.HasPrefix(trimmed, ":sess:take "):
		id, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(trimmed, ":sess:take ")), 10, 64)
		if err != nil || id == 0 {
			return sessionControlScript{}, false
		}
		return sessionControlScript{name: "take", sessionID: id}, true
	default:
		return sessionControlScript{}, false
	}
}

func normalizeIonNamespaceAlias(script string) string {
	trimmed := strings.TrimLeft(script, " \t")
	if !strings.HasPrefix(trimmed, "::") {
		return script
	}
	prefixLen := len(script) - len(trimmed)
	return script[:prefixLen] + ":ion:" + trimmed[2:]
}
