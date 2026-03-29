package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// BootstrapRequest carries the initial file list for one client session.
type BootstrapRequest struct {
	Files []string
}

func (m *BootstrapRequest) Kind() Kind { return KindBootstrapRequest }

func (m *BootstrapRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeStringSlice(&b, m.Files); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *BootstrapRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	files, err := readStringSlice(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("bootstrap request has trailing data")
	}
	m.Files = files
	return nil
}

// ConnectRequest binds one transport connection to a logical client id.
// ClientID == 0 asks the server to allocate a new client.
type ConnectRequest struct {
	ClientID uint64
}

func (m *ConnectRequest) Kind() Kind { return KindConnectRequest }

func (m *ConnectRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.ClientID); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *ConnectRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint64(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("connect request has trailing data")
	}
	m.ClientID = id
	return nil
}

// ConnectResponse returns the bound logical client id.
type ConnectResponse struct {
	ClientID uint64
}

func (m *ConnectResponse) Kind() Kind { return KindConnectResponse }

func (m *ConnectResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.ClientID); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *ConnectResponse) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint64(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("connect response has trailing data")
	}
	m.ClientID = id
	return nil
}

// NewSessionRequest creates one new owner-backed session for the calling client.
type NewSessionRequest struct{}

func (m *NewSessionRequest) Kind() Kind { return KindNewSessionRequest }

func (m *NewSessionRequest) MarshalBinary() ([]byte, error) { return nil, nil }

func (m *NewSessionRequest) UnmarshalBinary(data []byte) error {
	if len(data) != 0 {
		return fmt.Errorf("new-session request has trailing data")
	}
	return nil
}

// NewSessionResponse returns the newly allocated session id.
type NewSessionResponse struct {
	SessionID uint64
}

func (m *NewSessionResponse) Kind() Kind { return KindNewSessionResponse }

func (m *NewSessionResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.SessionID); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *NewSessionResponse) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint64(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("new-session response has trailing data")
	}
	m.SessionID = id
	return nil
}

// SessionListRequest asks for visible live sessions.
type SessionListRequest struct{}

func (m *SessionListRequest) Kind() Kind { return KindSessionListRequest }

func (m *SessionListRequest) MarshalBinary() ([]byte, error) { return nil, nil }

func (m *SessionListRequest) UnmarshalBinary(data []byte) error {
	if len(data) != 0 {
		return fmt.Errorf("session-list request has trailing data")
	}
	return nil
}

// SessionListResponse returns visible live sessions.
type SessionListResponse struct {
	Sessions []SessionSummary
}

func (m *SessionListResponse) Kind() Kind { return KindSessionListResponse }

func (m *SessionListResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint32(&b, uint32(len(m.Sessions))); err != nil {
		return nil, err
	}
	for _, sess := range m.Sessions {
		if err := writeUint64(&b, sess.ID); err != nil {
			return nil, err
		}
		if err := writeBool(&b, sess.Owner); err != nil {
			return nil, err
		}
		if err := writeBool(&b, sess.Controlled); err != nil {
			return nil, err
		}
		if err := writeBool(&b, sess.Taken); err != nil {
			return nil, err
		}
		if err := writeString(&b, sess.CurrentFile); err != nil {
			return nil, err
		}
		if err := binary.Write(&b, binary.LittleEndian, sess.LastActiveUnixMs); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func (m *SessionListResponse) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	n, err := readUint32(r)
	if err != nil {
		return err
	}
	sessions := make([]SessionSummary, 0, n)
	for i := uint32(0); i < n; i++ {
		id, err := readUint64(r)
		if err != nil {
			return err
		}
		owner, err := readBool(r)
		if err != nil {
			return err
		}
		controlled, err := readBool(r)
		if err != nil {
			return err
		}
		taken, err := readBool(r)
		if err != nil {
			return err
		}
		currentFile, err := readString(r)
		if err != nil {
			return err
		}
		var lastActive int64
		if err := binary.Read(r, binary.LittleEndian, &lastActive); err != nil {
			return err
		}
		sessions = append(sessions, SessionSummary{
			ID:               id,
			Owner:            owner,
			Controlled:       controlled,
			Taken:            taken,
			CurrentFile:      currentFile,
			LastActiveUnixMs: lastActive,
		})
	}
	if r.Len() != 0 {
		return fmt.Errorf("session-list response has trailing data")
	}
	m.Sessions = sessions
	return nil
}

// TakeSessionRequest temporarily transfers control of one owner-backed session
// to the calling client.
type TakeSessionRequest struct {
	SessionID uint64
}

func (m *TakeSessionRequest) Kind() Kind { return KindTakeSessionRequest }

func (m *TakeSessionRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.SessionID); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *TakeSessionRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint64(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("take-session request has trailing data")
	}
	m.SessionID = id
	return nil
}

// ReturnSessionRequest returns control of one taken session to its owner.
type ReturnSessionRequest struct {
	SessionID uint64
}

func (m *ReturnSessionRequest) Kind() Kind { return KindReturnSessionRequest }

func (m *ReturnSessionRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.SessionID); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *ReturnSessionRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint64(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("return-session request has trailing data")
	}
	m.SessionID = id
	return nil
}

// NamespaceCommandDoc documents one command exposed by a namespace provider.
type NamespaceCommandDoc struct {
	Name    string
	Args    string
	Summary string
	Help    string
}

// NamespaceProviderDoc documents one namespace provider and its commands.
type NamespaceProviderDoc struct {
	Namespace string
	Summary   string
	Help      string
	Commands  []NamespaceCommandDoc
}

// NamespaceRegisterRequest claims one extension namespace for the calling client.
type NamespaceRegisterRequest struct {
	Provider NamespaceProviderDoc
}

func (m *NamespaceRegisterRequest) Kind() Kind { return KindNamespaceRegisterRequest }

func (m *NamespaceRegisterRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeNamespaceProviderDoc(&b, m.Provider); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *NamespaceRegisterRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	provider, err := readNamespaceProviderDoc(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("namespace-register request has trailing data")
	}
	m.Provider = provider
	return nil
}

// InvocationWaitRequest blocks until one registered namespace invocation is ready.
type InvocationWaitRequest struct{}

func (m *InvocationWaitRequest) Kind() Kind { return KindInvocationWaitRequest }

func (m *InvocationWaitRequest) MarshalBinary() ([]byte, error) { return nil, nil }

func (m *InvocationWaitRequest) UnmarshalBinary(data []byte) error {
	if len(data) != 0 {
		return fmt.Errorf("invocation-wait request has trailing data")
	}
	return nil
}

// InvocationWaitResponse returns one delegated extension command invocation.
type InvocationWaitResponse struct {
	Invocation Invocation
}

func (m *InvocationWaitResponse) Kind() Kind { return KindInvocationWaitResponse }

func (m *InvocationWaitResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.Invocation.ID); err != nil {
		return nil, err
	}
	if err := writeUint64(&b, m.Invocation.SessionID); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.Invocation.Script); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *InvocationWaitResponse) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint64(r)
	if err != nil {
		return err
	}
	sessionID, err := readUint64(r)
	if err != nil {
		return err
	}
	script, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("invocation-wait response has trailing data")
	}
	m.Invocation = Invocation{
		ID:        id,
		SessionID: sessionID,
		Script:    script,
	}
	return nil
}

// InvocationFinishRequest completes one delegated extension invocation.
type InvocationFinishRequest struct {
	InvocationID uint64
	Err          string
	Stdout       string
	Stderr       string
}

func (m *InvocationFinishRequest) Kind() Kind { return KindInvocationFinishRequest }

func (m *InvocationFinishRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.InvocationID); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.Err); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.Stdout); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.Stderr); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *InvocationFinishRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	invocationID, err := readUint64(r)
	if err != nil {
		return err
	}
	errText, err := readString(r)
	if err != nil {
		return err
	}
	stdout, err := readString(r)
	if err != nil {
		return err
	}
	stderr, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("invocation-finish request has trailing data")
	}
	m.InvocationID = invocationID
	m.Err = errText
	m.Stdout = stdout
	m.Stderr = stderr
	return nil
}

// InvocationCancelWaitRequest blocks until one delegated invocation is canceled
// or otherwise completed.
type InvocationCancelWaitRequest struct {
	InvocationID uint64
}

func (m *InvocationCancelWaitRequest) Kind() Kind { return KindInvocationCancelWaitRequest }

func (m *InvocationCancelWaitRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.InvocationID); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *InvocationCancelWaitRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint64(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("invocation-cancel-wait request has trailing data")
	}
	m.InvocationID = id
	return nil
}

// InvocationCancelWaitResponse reports whether the invocation was canceled.
type InvocationCancelWaitResponse struct {
	Canceled bool
}

func (m *InvocationCancelWaitResponse) Kind() Kind { return KindInvocationCancelWaitResponse }

func (m *InvocationCancelWaitResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeBool(&b, m.Canceled); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *InvocationCancelWaitResponse) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	canceled, err := readBool(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("invocation-cancel-wait response has trailing data")
	}
	m.Canceled = canceled
	return nil
}

// DisconnectRequest tells the server to tear down the current client immediately.
type DisconnectRequest struct{}

func (m *DisconnectRequest) Kind() Kind { return KindDisconnectRequest }

func (m *DisconnectRequest) MarshalBinary() ([]byte, error) { return nil, nil }

func (m *DisconnectRequest) UnmarshalBinary(data []byte) error {
	if len(data) != 0 {
		return fmt.Errorf("disconnect request has trailing data")
	}
	return nil
}

// BufferSnapshotsRequest asks for the current shared buffer snapshots without
// requiring a visible session.
type BufferSnapshotsRequest struct{}

func (m *BufferSnapshotsRequest) Kind() Kind { return KindBufferSnapshotsRequest }

func (m *BufferSnapshotsRequest) MarshalBinary() ([]byte, error) { return nil, nil }

func (m *BufferSnapshotsRequest) UnmarshalBinary(data []byte) error {
	if len(data) != 0 {
		return fmt.Errorf("buffer-snapshots request has trailing data")
	}
	return nil
}

// BufferSnapshotsMessage transports one full shared-buffer snapshot list.
type BufferSnapshotsMessage struct {
	Buffers []BufferView
}

func (m *BufferSnapshotsMessage) Kind() Kind { return KindBufferSnapshotsResponse }

func (m *BufferSnapshotsMessage) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint32(&b, uint32(len(m.Buffers))); err != nil {
		return nil, err
	}
	for _, view := range m.Buffers {
		if err := writeUint32(&b, uint32(view.ID)); err != nil {
			return nil, err
		}
		if err := writeString(&b, view.Text); err != nil {
			return nil, err
		}
		if err := writeString(&b, view.Name); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func (m *BufferSnapshotsMessage) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	n, err := readUint32(r)
	if err != nil {
		return err
	}
	buffers := make([]BufferView, 0, n)
	for i := uint32(0); i < n; i++ {
		id, err := readUint32(r)
		if err != nil {
			return err
		}
		text, err := readString(r)
		if err != nil {
			return err
		}
		name, err := readString(r)
		if err != nil {
			return err
		}
		buffers = append(buffers, BufferView{
			ID:   int(id),
			Text: text,
			Name: name,
		})
	}
	if r.Len() != 0 {
		return fmt.Errorf("buffer-snapshots response has trailing data")
	}
	m.Buffers = buffers
	return nil
}

// SessionStatusRequest publishes one transient session-scoped status message.
type SessionStatusRequest struct {
	Update SessionStatusUpdate
}

func (m *SessionStatusRequest) Kind() Kind { return KindSessionStatusRequest }

func (m *SessionStatusRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint64(&b, m.Update.SessionID); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.Update.Status); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *SessionStatusRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	sessionID, err := readUint64(r)
	if err != nil {
		return err
	}
	status, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("session-status request has trailing data")
	}
	m.Update = SessionStatusUpdate{
		SessionID: sessionID,
		Status:    status,
	}
	return nil
}

// OpenFilesRequest carries one explicit file-open list for a live terminal
// client without reparsing shell-style whitespace.
type OpenFilesRequest struct {
	Files []string
}

func (m *OpenFilesRequest) Kind() Kind { return KindOpenFilesRequest }

func (m *OpenFilesRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeStringSlice(&b, m.Files); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *OpenFilesRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	files, err := readStringSlice(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("open-files request has trailing data")
	}
	m.Files = files
	return nil
}

// OpenTargetRequest carries one explicit addressed target open for a live
// terminal client.
type OpenTargetRequest struct {
	Path    string
	Address string
}

func (m *OpenTargetRequest) Kind() Kind { return KindOpenTargetRequest }

func (m *OpenTargetRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeString(&b, m.Path); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.Address); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *OpenTargetRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	path, err := readString(r)
	if err != nil {
		return err
	}
	address, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("open-target request has trailing data")
	}
	m.Path = path
	m.Address = address
	return nil
}

// OKResponse acknowledges one successful request with no typed payload.
type OKResponse struct{}

func (m *OKResponse) Kind() Kind { return KindOKResponse }

func (m *OKResponse) MarshalBinary() ([]byte, error) { return nil, nil }

// CommandRequest carries one sam command string from client to server.
type CommandRequest struct {
	Script string
}

func (m *CommandRequest) Kind() Kind { return KindCommandRequest }

func (m *CommandRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeString(&b, m.Script); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *CommandRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	s, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("command request has trailing data")
	}
	m.Script = s
	return nil
}

// CommandResponse reports whether the client command loop should continue.
type CommandResponse struct {
	Continue bool
}

func (m *CommandResponse) Kind() Kind { return KindCommandResponse }

func (m *CommandResponse) MarshalBinary() ([]byte, error) {
	if m.Continue {
		return []byte{1}, nil
	}
	return []byte{0}, nil
}

func (m *CommandResponse) UnmarshalBinary(data []byte) error {
	if len(data) != 1 {
		return fmt.Errorf("command response size %d, want 1", len(data))
	}
	m.Continue = data[0] != 0
	return nil
}

// InterruptRequest asks the server to interrupt one currently running external command.
type InterruptRequest struct{}

func (m *InterruptRequest) Kind() Kind { return KindInterruptRequest }

func (m *InterruptRequest) MarshalBinary() ([]byte, error) { return nil, nil }

func (m *InterruptRequest) UnmarshalBinary(data []byte) error {
	if len(data) != 0 {
		return fmt.Errorf("interrupt request has trailing data")
	}
	return nil
}

// ErrorResponse returns one protocol-level or command-level error string.
type ErrorResponse struct {
	Message        string
	DiagnosticText string
}

func (m *ErrorResponse) Kind() Kind { return KindErrorResponse }

func (m *ErrorResponse) Error() string {
	if m == nil {
		return ""
	}
	return m.Message
}

func (m *ErrorResponse) Diagnostic() string {
	if m == nil {
		return ""
	}
	if m.DiagnosticText != "" {
		return m.DiagnosticText
	}
	return "?" + m.Message
}

func (m *ErrorResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeString(&b, m.Message); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.DiagnosticText); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *ErrorResponse) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	s, err := readString(r)
	if err != nil {
		return err
	}
	diag, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("error response has trailing data")
	}
	m.Message = s
	m.DiagnosticText = diag
	return nil
}

// OutputEvent carries one stdout/stderr chunk emitted while a request runs.
type OutputEvent struct {
	Data string
}

func (m *OutputEvent) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeString(&b, m.Data); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *OutputEvent) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	s, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("output event has trailing data")
	}
	m.Data = s
	return nil
}

// StdoutEvent carries one stdout chunk emitted while a request runs.
type StdoutEvent struct {
	OutputEvent
}

func (m *StdoutEvent) Kind() Kind { return KindStdoutEvent }

// StderrEvent carries one stderr chunk emitted while a request runs.
type StderrEvent struct {
	OutputEvent
}

func (m *StderrEvent) Kind() Kind { return KindStderrEvent }

// CurrentViewRequest asks for the current buffer snapshot.
type CurrentViewRequest struct{}

func (m *CurrentViewRequest) Kind() Kind { return KindCurrentViewRequest }

func (m *CurrentViewRequest) MarshalBinary() ([]byte, error) { return nil, nil }

// BufferViewMessage transports one buffer snapshot or update event.
type BufferViewMessage struct {
	View BufferView
}

func (m *BufferViewMessage) Kind() Kind { return KindCurrentViewResponse }

func (m *BufferViewMessage) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint32(&b, uint32(m.View.ID)); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.View.Text); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.View.Name); err != nil {
		return nil, err
	}
	if err := binary.Write(&b, binary.LittleEndian, int32(m.View.DotStart)); err != nil {
		return nil, err
	}
	if err := binary.Write(&b, binary.LittleEndian, int32(m.View.DotEnd)); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.View.Status); err != nil {
		return nil, err
	}
	if err := writeUint64(&b, m.View.StatusSeq); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *BufferViewMessage) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint32(r)
	if err != nil {
		return err
	}
	text, err := readString(r)
	if err != nil {
		return err
	}
	name, err := readString(r)
	if err != nil {
		return err
	}
	var start int32
	if err := binary.Read(r, binary.LittleEndian, &start); err != nil {
		return err
	}
	var end int32
	if err := binary.Read(r, binary.LittleEndian, &end); err != nil {
		return err
	}
	status, err := readString(r)
	if err != nil {
		return err
	}
	statusSeq, err := readUint64(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("buffer view has trailing data")
	}
	m.View = BufferView{
		ID:        int(id),
		Text:      text,
		Name:      name,
		DotStart:  int(start),
		DotEnd:    int(end),
		Status:    status,
		StatusSeq: statusSeq,
	}
	return nil
}

// MenuFilesRequest asks for the current file-menu snapshot.
type MenuFilesRequest struct{}

func (m *MenuFilesRequest) Kind() Kind { return KindMenuFilesRequest }

func (m *MenuFilesRequest) MarshalBinary() ([]byte, error) { return nil, nil }

// MenuFilesMessage transports one full file-menu snapshot or update event.
type MenuFilesMessage struct {
	Files []MenuFile
}

func (m *MenuFilesMessage) Kind() Kind { return KindMenuFilesResponse }

func (m *MenuFilesMessage) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint32(&b, uint32(len(m.Files))); err != nil {
		return nil, err
	}
	for _, f := range m.Files {
		if err := writeUint32(&b, uint32(f.ID)); err != nil {
			return nil, err
		}
		if err := writeString(&b, f.Name); err != nil {
			return nil, err
		}
		if err := writeBool(&b, f.Dirty); err != nil {
			return nil, err
		}
		if err := writeBool(&b, f.Changed); err != nil {
			return nil, err
		}
		if err := writeBool(&b, f.Current); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func (m *MenuFilesMessage) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	n, err := readUint32(r)
	if err != nil {
		return err
	}
	files := make([]MenuFile, 0, n)
	for i := uint32(0); i < n; i++ {
		id, err := readUint32(r)
		if err != nil {
			return err
		}
		name, err := readString(r)
		if err != nil {
			return err
		}
		dirty, err := readBool(r)
		if err != nil {
			return err
		}
		changed, err := readBool(r)
		if err != nil {
			return err
		}
		current, err := readBool(r)
		if err != nil {
			return err
		}
		files = append(files, MenuFile{
			ID:      int(id),
			Name:    name,
			Dirty:   dirty,
			Changed: changed,
			Current: current,
		})
	}
	if r.Len() != 0 {
		return fmt.Errorf("menu files has trailing data")
	}
	m.Files = files
	return nil
}

// NavigationStackRequest asks for the current per-client navigation stack.
type NavigationStackRequest struct{}

func (m *NavigationStackRequest) Kind() Kind { return KindNavigationStackRequest }

func (m *NavigationStackRequest) MarshalBinary() ([]byte, error) { return nil, nil }

// NavigationStackMessage transports the formatted per-client navigation stack.
type NavigationStackMessage struct {
	Stack NavigationStack
}

func (m *NavigationStackMessage) Kind() Kind { return KindNavigationStackResponse }

func (m *NavigationStackMessage) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, int32(m.Stack.Current)); err != nil {
		return nil, err
	}
	if err := writeUint32(&b, uint32(len(m.Stack.Entries))); err != nil {
		return nil, err
	}
	for _, entry := range m.Stack.Entries {
		if err := writeString(&b, entry.Label); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func (m *NavigationStackMessage) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	var current int32
	if err := binary.Read(r, binary.LittleEndian, &current); err != nil {
		return err
	}
	n, err := readUint32(r)
	if err != nil {
		return err
	}
	entries := make([]NavigationEntry, 0, n)
	for i := uint32(0); i < n; i++ {
		label, err := readString(r)
		if err != nil {
			return err
		}
		entries = append(entries, NavigationEntry{Label: label})
	}
	if r.Len() != 0 {
		return fmt.Errorf("navigation stack has trailing data")
	}
	m.Stack = NavigationStack{
		Entries: entries,
		Current: int(current),
	}
	return nil
}

// FocusRequest changes one client's current file by menu index.
type FocusRequest struct {
	ID int
}

func (m *FocusRequest) Kind() Kind { return KindFocusRequest }

func (m *FocusRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint32(&b, uint32(m.ID)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *FocusRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	id, err := readUint32(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("focus request has trailing data")
	}
	m.ID = int(id)
	return nil
}

// AddressRequest resolves one sam address against the current file.
type AddressRequest struct {
	Expr string
}

func (m *AddressRequest) Kind() Kind { return KindAddressRequest }

func (m *AddressRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeString(&b, m.Expr); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *AddressRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	expr, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("address request has trailing data")
	}
	m.Expr = expr
	return nil
}

// SetDotRequest updates one client's current selection range.
type SetDotRequest struct {
	Start int
	End   int
}

func (m *SetDotRequest) Kind() Kind { return KindSetDotRequest }

func (m *SetDotRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint32(&b, uint32(m.Start)); err != nil {
		return nil, err
	}
	if err := writeUint32(&b, uint32(m.End)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *SetDotRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	start, err := readUint32(r)
	if err != nil {
		return err
	}
	end, err := readUint32(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("set-dot request has trailing data")
	}
	m.Start = int(start)
	m.End = int(end)
	return nil
}

// ReplaceRequest applies one replacement edit to the current file.
type ReplaceRequest struct {
	Start int
	End   int
	Text  string
}

func (m *ReplaceRequest) Kind() Kind { return KindReplaceRequest }

func (m *ReplaceRequest) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeUint32(&b, uint32(m.Start)); err != nil {
		return nil, err
	}
	if err := writeUint32(&b, uint32(m.End)); err != nil {
		return nil, err
	}
	if err := writeString(&b, m.Text); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *ReplaceRequest) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	start, err := readUint32(r)
	if err != nil {
		return err
	}
	end, err := readUint32(r)
	if err != nil {
		return err
	}
	text, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("replace request has trailing data")
	}
	m.Start = int(start)
	m.End = int(end)
	m.Text = text
	return nil
}

// UndoRequest requests one undo operation on the current file.
type UndoRequest struct{}

func (m *UndoRequest) Kind() Kind { return KindUndoRequest }

func (m *UndoRequest) MarshalBinary() ([]byte, error) { return nil, nil }

// SaveRequest requests a save of the current file.
type SaveRequest struct{}

func (m *SaveRequest) Kind() Kind { return KindSaveRequest }

func (m *SaveRequest) MarshalBinary() ([]byte, error) { return nil, nil }

// SaveResponse carries the server-produced save status line.
type SaveResponse struct {
	Status string
}

func (m *SaveResponse) Kind() Kind { return KindSaveResponse }

func (m *SaveResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeString(&b, m.Status); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (m *SaveResponse) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
	status, err := readString(r)
	if err != nil {
		return err
	}
	if r.Len() != 0 {
		return fmt.Errorf("save response has trailing data")
	}
	m.Status = status
	return nil
}

func writeBool(w io.Writer, v bool) error {
	var b byte
	if v {
		b = 1
	}
	_, err := w.Write([]byte{b})
	return err
}

func readBool(r *bytes.Reader) (bool, error) {
	b, err := r.ReadByte()
	if err != nil {
		return false, err
	}
	return b != 0, nil
}

func writeUint32(w io.Writer, n uint32) error {
	return binary.Write(w, binary.LittleEndian, n)
}

func readUint32(r *bytes.Reader) (uint32, error) {
	var n uint32
	err := binary.Read(r, binary.LittleEndian, &n)
	return n, err
}

func writeUint64(w io.Writer, n uint64) error {
	return binary.Write(w, binary.LittleEndian, n)
}

func readUint64(r *bytes.Reader) (uint64, error) {
	var n uint64
	err := binary.Read(r, binary.LittleEndian, &n)
	return n, err
}

func writeString(w io.Writer, s string) error {
	data := []byte(s)
	if err := writeUint32(w, uint32(len(data))); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readString(r *bytes.Reader) (string, error) {
	n, err := readUint32(r)
	if err != nil {
		return "", err
	}
	if uint64(n) > uint64(r.Len()) {
		return "", fmt.Errorf("short string payload")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func writeStringSlice(w io.Writer, values []string) error {
	if err := writeUint32(w, uint32(len(values))); err != nil {
		return err
	}
	for _, s := range values {
		if err := writeString(w, s); err != nil {
			return err
		}
	}
	return nil
}

func readStringSlice(r *bytes.Reader) ([]string, error) {
	n, err := readUint32(r)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := uint32(0); i < n; i++ {
		s, err := readString(r)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func writeNamespaceProviderDoc(w io.Writer, doc NamespaceProviderDoc) error {
	if err := writeString(w, doc.Namespace); err != nil {
		return err
	}
	if err := writeString(w, doc.Summary); err != nil {
		return err
	}
	if err := writeString(w, doc.Help); err != nil {
		return err
	}
	if err := writeUint32(w, uint32(len(doc.Commands))); err != nil {
		return err
	}
	for _, cmd := range doc.Commands {
		if err := writeString(w, cmd.Name); err != nil {
			return err
		}
		if err := writeString(w, cmd.Args); err != nil {
			return err
		}
		if err := writeString(w, cmd.Summary); err != nil {
			return err
		}
		if err := writeString(w, cmd.Help); err != nil {
			return err
		}
	}
	return nil
}

func readNamespaceProviderDoc(r *bytes.Reader) (NamespaceProviderDoc, error) {
	namespace, err := readString(r)
	if err != nil {
		return NamespaceProviderDoc{}, err
	}
	summary, err := readString(r)
	if err != nil {
		return NamespaceProviderDoc{}, err
	}
	help, err := readString(r)
	if err != nil {
		return NamespaceProviderDoc{}, err
	}
	n, err := readUint32(r)
	if err != nil {
		return NamespaceProviderDoc{}, err
	}
	commands := make([]NamespaceCommandDoc, 0, n)
	for i := uint32(0); i < n; i++ {
		name, err := readString(r)
		if err != nil {
			return NamespaceProviderDoc{}, err
		}
		args, err := readString(r)
		if err != nil {
			return NamespaceProviderDoc{}, err
		}
		summary, err := readString(r)
		if err != nil {
			return NamespaceProviderDoc{}, err
		}
		help, err := readString(r)
		if err != nil {
			return NamespaceProviderDoc{}, err
		}
		commands = append(commands, NamespaceCommandDoc{
			Name:    name,
			Args:    args,
			Summary: summary,
			Help:    help,
		})
	}
	return NamespaceProviderDoc{
		Namespace: namespace,
		Summary:   summary,
		Help:      help,
		Commands:  commands,
	}, nil
}
