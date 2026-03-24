package session

import (
	"fmt"
	"io"
	"net"
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
	sessionID uint64
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

// Close tears down the client connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Bootstrap loads the initial file set.
func (c *Client) Bootstrap(files []string) error {
	_, msg, err := c.roundTrip(&wire.BootstrapRequest{Files: files})
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
	_, msg, err := c.roundTrip(&wire.CommandRequest{Script: script})
	if err != nil {
		return false, err
	}
	resp, ok := msg.(*wire.CommandResponse)
	if !ok {
		return false, fmt.Errorf("command response type %T, want *wire.CommandResponse", msg)
	}
	return resp.Continue, nil
}

// CurrentView returns the current buffer snapshot.
func (c *Client) CurrentView() (wire.BufferView, error) {
	_, msg, err := c.roundTrip(&wire.CurrentViewRequest{})
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
	_, msg, err := c.roundTrip(&wire.OpenFilesRequest{Files: files})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("open-files response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// MenuFiles returns the current file-menu snapshot.
func (c *Client) MenuFiles() ([]wire.MenuFile, error) {
	_, msg, err := c.roundTrip(&wire.MenuFilesRequest{})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*wire.MenuFilesMessage)
	if !ok {
		return nil, fmt.Errorf("menu-files response type %T, want *wire.MenuFilesMessage", msg)
	}
	return append([]wire.MenuFile(nil), resp.Files...), nil
}

// FocusFile changes the current file selection.
func (c *Client) FocusFile(id int) (wire.BufferView, error) {
	_, msg, err := c.roundTrip(&wire.FocusRequest{ID: id})
	if err != nil {
		return wire.BufferView{}, err
	}
	resp, ok := msg.(*wire.BufferViewMessage)
	if !ok {
		return wire.BufferView{}, fmt.Errorf("focus response type %T, want *wire.BufferViewMessage", msg)
	}
	return resp.View, nil
}

// SetDot updates the current selection range.
func (c *Client) SetDot(start, end int) (wire.BufferView, error) {
	_, msg, err := c.roundTrip(&wire.SetDotRequest{Start: start, End: end})
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
	_, msg, err := c.roundTrip(&wire.ReplaceRequest{Start: start, End: end, Text: text})
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
	_, msg, err := c.roundTrip(&wire.UndoRequest{})
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
	_, msg, err := c.roundTrip(&wire.SaveRequest{})
	if err != nil {
		return "", err
	}
	resp, ok := msg.(*wire.SaveResponse)
	if !ok {
		return "", fmt.Errorf("save response type %T, want *wire.SaveResponse", msg)
	}
	return resp.Status, nil
}

func (c *Client) roundTrip(req wire.Message) (wire.Frame, any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextReqID++
	reqID := c.nextReqID
	if err := wire.WriteFrame(c.conn, reqID, c.sessionID, req); err != nil {
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
		if frame.SessionID != 0 {
			c.sessionID = frame.SessionID
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
