package transport

import (
	"errors"
	"fmt"
	"io"
	"net"

	"ion/internal/proto/wire"
	serversession "ion/internal/server/session"
	"ion/internal/server/workspace"
)

// Server serves ion wire requests over one listener.
type Server struct {
	ws *workspace.Workspace
}

type diagnosticReporter interface {
	Diagnostic() string
}

// New constructs a transport server over one shared workspace.
func New(ws *workspace.Workspace) *Server {
	return &Server{ws: ws}
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve(listener net.Listener) error {
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

// ServeConn handles requests for one client connection.
func (s *Server) ServeConn(conn io.ReadWriteCloser) error {
	defer conn.Close()

	stdout := &eventWriter{conn: conn, kind: wire.KindStdoutEvent}
	stderr := &eventWriter{conn: conn, kind: wire.KindStderrEvent}
	session := serversession.NewTerm(s.ws, stdout, stderr)
	stdout.sessionID = session.ID()
	stderr.sessionID = session.ID()

	for {
		frame, err := wire.ReadFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := s.handleFrame(conn, session, stdout, stderr, frame); err != nil {
			return err
		}
	}
}

func (s *Server) handleFrame(conn io.Writer, session *serversession.TermSession, stdout, stderr *eventWriter, frame wire.Frame) error {
	msg, err := wire.DecodeMessage(frame)
	if err != nil {
		return writeError(conn, frame.RequestID, session.ID(), err)
	}

	stdout.requestID = frame.RequestID
	stderr.requestID = frame.RequestID

	switch msg := msg.(type) {
	case *wire.BootstrapRequest:
		if err := session.Bootstrap(msg.Files); err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.OKResponse{})
	case *wire.OpenFilesRequest:
		view, err := session.OpenFiles(msg.Files)
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.BufferViewMessage{View: view})
	case *wire.CommandRequest:
		cont, err := session.Execute(msg.Script)
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.CommandResponse{Continue: cont})
	case *wire.CurrentViewRequest:
		view, err := session.CurrentView()
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.BufferViewMessage{View: view})
	case *wire.MenuFilesRequest:
		files, err := session.MenuFiles()
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.MenuFilesMessage{Files: files})
	case *wire.FocusRequest:
		view, err := session.FocusFile(msg.ID)
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.BufferViewMessage{View: view})
	case *wire.AddressRequest:
		view, err := session.SetAddress(msg.Expr)
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.BufferViewMessage{View: view})
	case *wire.SetDotRequest:
		view, err := session.SetDot(msg.Start, msg.End)
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.BufferViewMessage{View: view})
	case *wire.ReplaceRequest:
		view, err := session.Replace(msg.Start, msg.End, msg.Text)
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.BufferViewMessage{View: view})
	case *wire.UndoRequest:
		view, err := session.Undo()
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.BufferViewMessage{View: view})
	case *wire.SaveRequest:
		status, err := session.Save()
		if err != nil {
			return writeError(conn, frame.RequestID, session.ID(), err)
		}
		return wire.WriteFrame(conn, frame.RequestID, session.ID(), &wire.SaveResponse{Status: status})
	default:
		return writeError(conn, frame.RequestID, session.ID(), fmt.Errorf("unsupported request kind %d", frame.Kind))
	}
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
