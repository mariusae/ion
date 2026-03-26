package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	frameMagic      = "ION1"
	frameVersion    = 1
	frameHeaderSize = 28
)

// Kind identifies one frame payload type in the ion wire protocol.
type Kind uint16

const (
	KindBootstrapRequest Kind = iota + 1
	KindOpenFilesRequest
	KindOpenTargetRequest
	KindOKResponse
	KindCommandRequest
	KindCommandResponse
	KindErrorResponse
	KindStdoutEvent
	KindStderrEvent
	KindCurrentViewRequest
	KindCurrentViewResponse
	KindMenuFilesRequest
	KindMenuFilesResponse
	KindNavigationStackRequest
	KindNavigationStackResponse
	KindFocusRequest
	KindAddressRequest
	KindSetDotRequest
	KindReplaceRequest
	KindUndoRequest
	KindSaveRequest
	KindSaveResponse
	KindBufferUpdateEvent
	KindMenuUpdateEvent
)

// Frame is the versioned binary envelope shared by all wire messages.
type Frame struct {
	Version   uint16
	Kind      Kind
	Flags     uint16
	RequestID uint32
	SessionID uint64
	Payload   []byte
}

// Message is a typed protocol payload carried inside one frame.
type Message interface {
	Kind() Kind
	MarshalBinary() ([]byte, error)
}

// WriteFrame encodes one framed message to w.
func WriteFrame(w io.Writer, requestID uint32, sessionID uint64, msg Message) error {
	payload, err := msg.MarshalBinary()
	if err != nil {
		return err
	}
	frame := Frame{
		Version:   frameVersion,
		Kind:      msg.Kind(),
		RequestID: requestID,
		SessionID: sessionID,
		Payload:   payload,
	}
	return writeFrame(w, frame)
}

// ReadFrame decodes one framed message from r.
func ReadFrame(r io.Reader) (Frame, error) {
	var header [frameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	if string(header[:4]) != frameMagic {
		return Frame{}, fmt.Errorf("invalid frame magic")
	}
	frame := Frame{
		Version:   binary.LittleEndian.Uint16(header[4:6]),
		Kind:      Kind(binary.LittleEndian.Uint16(header[6:8])),
		Flags:     binary.LittleEndian.Uint16(header[8:10]),
		RequestID: binary.LittleEndian.Uint32(header[12:16]),
		SessionID: binary.LittleEndian.Uint64(header[16:24]),
	}
	if frame.Version != frameVersion {
		return Frame{}, fmt.Errorf("unsupported frame version %d", frame.Version)
	}
	n := binary.LittleEndian.Uint32(header[24:28])
	if n > 1<<26 {
		return Frame{}, fmt.Errorf("frame payload too large: %d", n)
	}
	frame.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, frame.Payload); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func writeFrame(w io.Writer, frame Frame) error {
	if frame.Version == 0 {
		frame.Version = frameVersion
	}
	if frame.Version != frameVersion {
		return fmt.Errorf("unsupported frame version %d", frame.Version)
	}
	var header [frameHeaderSize]byte
	copy(header[:4], frameMagic)
	binary.LittleEndian.PutUint16(header[4:6], frame.Version)
	binary.LittleEndian.PutUint16(header[6:8], uint16(frame.Kind))
	binary.LittleEndian.PutUint16(header[8:10], frame.Flags)
	binary.LittleEndian.PutUint32(header[12:16], frame.RequestID)
	binary.LittleEndian.PutUint64(header[16:24], frame.SessionID)
	binary.LittleEndian.PutUint32(header[24:28], uint32(len(frame.Payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(frame.Payload) == 0 {
		return nil
	}
	_, err := w.Write(frame.Payload)
	return err
}

// EncodeFrame returns the binary encoding of one message frame.
func EncodeFrame(requestID uint32, sessionID uint64, msg Message) ([]byte, error) {
	var b bytes.Buffer
	if err := WriteFrame(&b, requestID, sessionID, msg); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// DecodeMessage decodes a typed message payload from one frame.
func DecodeMessage(frame Frame) (any, error) {
	switch frame.Kind {
	case KindBootstrapRequest:
		var msg BootstrapRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindOpenFilesRequest:
		var msg OpenFilesRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindOpenTargetRequest:
		var msg OpenTargetRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindOKResponse:
		return &OKResponse{}, nil
	case KindCommandRequest:
		var msg CommandRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindCommandResponse:
		var msg CommandResponse
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindErrorResponse:
		var msg ErrorResponse
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindStdoutEvent, KindStderrEvent:
		var msg OutputEvent
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindCurrentViewRequest:
		return &CurrentViewRequest{}, nil
	case KindCurrentViewResponse, KindBufferUpdateEvent:
		var msg BufferViewMessage
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindMenuFilesRequest:
		return &MenuFilesRequest{}, nil
	case KindMenuFilesResponse, KindMenuUpdateEvent:
		var msg MenuFilesMessage
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindNavigationStackRequest:
		return &NavigationStackRequest{}, nil
	case KindNavigationStackResponse:
		var msg NavigationStackMessage
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindFocusRequest:
		var msg FocusRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindAddressRequest:
		var msg AddressRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindSetDotRequest:
		var msg SetDotRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindReplaceRequest:
		var msg ReplaceRequest
		return &msg, msg.UnmarshalBinary(frame.Payload)
	case KindUndoRequest:
		return &UndoRequest{}, nil
	case KindSaveRequest:
		return &SaveRequest{}, nil
	case KindSaveResponse:
		var msg SaveResponse
		return &msg, msg.UnmarshalBinary(frame.Payload)
	default:
		return nil, fmt.Errorf("unknown frame kind %d", frame.Kind)
	}
}
