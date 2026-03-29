package wire

import (
	"bytes"
	"reflect"
	"testing"
)

func TestEncodeDecodeFrameRoundTrip(t *testing.T) {
	t.Parallel()

	want := &ReplaceRequest{
		Start: 5,
		End:   9,
		Text:  "delta\n",
	}
	data, err := EncodeFrame(17, 23, want)
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}
	frame, err := ReadFrame(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if got, wantKind := frame.Kind, KindReplaceRequest; got != wantKind {
		t.Fatalf("frame.Kind = %d, want %d", got, wantKind)
	}
	if got, wantReq := frame.RequestID, uint32(17); got != wantReq {
		t.Fatalf("frame.RequestID = %d, want %d", got, wantReq)
	}
	if got, wantSess := frame.SessionID, uint64(23); got != wantSess {
		t.Fatalf("frame.SessionID = %d, want %d", got, wantSess)
	}
	decoded, err := DecodeMessage(frame)
	if err != nil {
		t.Fatalf("DecodeMessage() error = %v", err)
	}
	got, ok := decoded.(*ReplaceRequest)
	if !ok {
		t.Fatalf("DecodeMessage() type = %T, want *ReplaceRequest", decoded)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded message = %#v, want %#v", got, want)
	}
}

func TestBootstrapRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := &BootstrapRequest{Files: []string{"a.txt", "b.txt:2:4"}}
	data, err := want.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	var got BootstrapRequest
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	if !reflect.DeepEqual(got, *want) {
		t.Fatalf("round trip = %#v, want %#v", got, *want)
	}
}

func TestOpenFilesRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := &OpenFilesRequest{Files: []string{"a.txt", "b b.txt", "c.txt:12:4"}}
	data, err := want.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	var got OpenFilesRequest
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	if !reflect.DeepEqual(got, *want) {
		t.Fatalf("round trip = %#v, want %#v", got, *want)
	}
}

func TestAddressRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := &AddressRequest{Expr: "/^func"}
	data, err := want.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	var got AddressRequest
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	if !reflect.DeepEqual(got, *want) {
		t.Fatalf("round trip = %#v, want %#v", got, *want)
	}
}

func TestInterruptRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := &InterruptRequest{}
	data, err := want.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	var got InterruptRequest
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	frameData, err := EncodeFrame(9, 11, want)
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}
	frame, err := ReadFrame(bytes.NewReader(frameData))
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if got, want := frame.Kind, KindInterruptRequest; got != want {
		t.Fatalf("frame.Kind = %d, want %d", got, want)
	}
	msg, err := DecodeMessage(frame)
	if err != nil {
		t.Fatalf("DecodeMessage() error = %v", err)
	}
	if _, ok := msg.(*InterruptRequest); !ok {
		t.Fatalf("DecodeMessage() type = %T, want *InterruptRequest", msg)
	}
}

func TestBufferAndMenuMessagesRoundTrip(t *testing.T) {
	t.Parallel()

	view := &BufferViewMessage{
		View: BufferView{
			ID:       17,
			Text:     "alpha\nbeta\n",
			Name:     "notes.txt",
			DotStart: 2,
			DotEnd:   7,
		},
	}
	viewData, err := view.MarshalBinary()
	if err != nil {
		t.Fatalf("view MarshalBinary() error = %v", err)
	}
	var viewGot BufferViewMessage
	if err := viewGot.UnmarshalBinary(viewData); err != nil {
		t.Fatalf("view UnmarshalBinary() error = %v", err)
	}
	if !reflect.DeepEqual(viewGot, *view) {
		t.Fatalf("view round trip = %#v, want %#v", viewGot, *view)
	}

	menu := &MenuFilesMessage{
		Files: []MenuFile{
			{ID: 0, Name: "one.txt", Dirty: false, Current: true},
			{ID: 1, Name: "", Dirty: true, Current: false},
		},
	}
	menuData, err := menu.MarshalBinary()
	if err != nil {
		t.Fatalf("menu MarshalBinary() error = %v", err)
	}
	var menuGot MenuFilesMessage
	if err := menuGot.UnmarshalBinary(menuData); err != nil {
		t.Fatalf("menu UnmarshalBinary() error = %v", err)
	}
	if !reflect.DeepEqual(menuGot, *menu) {
		t.Fatalf("menu round trip = %#v, want %#v", menuGot, *menu)
	}
}

func TestNamespaceRegisterRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := &NamespaceRegisterRequest{
		Provider: NamespaceProviderDoc{
			Namespace: "demolsp",
			Summary:   "demo LSP commands",
			Help:      "Synthetic LSP-like commands for smoke testing.",
			Commands: []NamespaceCommandDoc{
				{
					Name:    "goto",
					Args:    "",
					Summary: "jump to the demo target",
					Help:    "Opens the README demo target in the caller's session.",
				},
				{
					Name:    "symbol",
					Args:    "<query>",
					Summary: "look up a symbol",
					Help:    "Resolves one synthetic symbol name.",
				},
			},
		},
	}
	data, err := want.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	var got NamespaceRegisterRequest
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	if !reflect.DeepEqual(got, *want) {
		t.Fatalf("round trip = %#v, want %#v", got, *want)
	}
}

func TestReadFrameRejectsBadMagic(t *testing.T) {
	t.Parallel()

	data := make([]byte, frameHeaderSize)
	copy(data[:4], "NOPE")
	if _, err := ReadFrame(bytes.NewReader(data)); err == nil {
		t.Fatal("ReadFrame() error = nil, want failure")
	}
}
