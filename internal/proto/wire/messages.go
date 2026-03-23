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

// ErrorResponse returns one protocol-level or command-level error string.
type ErrorResponse struct {
	Message string
}

func (m *ErrorResponse) Kind() Kind { return KindErrorResponse }

func (m *ErrorResponse) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	if err := writeString(&b, m.Message); err != nil {
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
	if r.Len() != 0 {
		return fmt.Errorf("error response has trailing data")
	}
	m.Message = s
	return nil
}

// BufferViewMessage transports one buffer snapshot or update event.
type BufferViewMessage struct {
	View BufferView
}

func (m *BufferViewMessage) Kind() Kind { return KindCurrentViewResponse }

func (m *BufferViewMessage) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
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
	return b.Bytes(), nil
}

func (m *BufferViewMessage) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)
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
	if r.Len() != 0 {
		return fmt.Errorf("buffer view has trailing data")
	}
	m.View = BufferView{
		Text:     text,
		Name:     name,
		DotStart: int(start),
		DotEnd:   int(end),
	}
	return nil
}

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
		current, err := readBool(r)
		if err != nil {
			return err
		}
		files = append(files, MenuFile{
			ID:      int(id),
			Name:    name,
			Dirty:   dirty,
			Current: current,
		})
	}
	if r.Len() != 0 {
		return fmt.Errorf("menu files has trailing data")
	}
	m.Files = files
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
