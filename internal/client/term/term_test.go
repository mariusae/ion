package term

import (
	"bufio"
	"strings"
	"testing"

	"ion/internal/proto/wire"
)

type fakeTermService struct {
	view      wire.BufferView
	menuFiles []wire.MenuFile
	focusID   int
}

func (f *fakeTermService) Bootstrap(files []string) error {
	_ = files
	return nil
}

func (f *fakeTermService) Execute(script string) (bool, error) {
	_ = script
	return true, nil
}

func (f *fakeTermService) CurrentView() (wire.BufferView, error) {
	return f.view, nil
}

func (f *fakeTermService) MenuFiles() ([]wire.MenuFile, error) {
	return append([]wire.MenuFile(nil), f.menuFiles...), nil
}

func (f *fakeTermService) FocusFile(id int) (wire.BufferView, error) {
	f.focusID = id
	for _, file := range f.menuFiles {
		if file.ID != id {
			continue
		}
		f.view.Name = file.Name
		break
	}
	return f.view, nil
}

func (f *fakeTermService) SetDot(start, end int) (wire.BufferView, error) {
	f.view.DotStart = start
	f.view.DotEnd = end
	return f.view, nil
}

func (f *fakeTermService) Replace(start, end int, text string) (wire.BufferView, error) {
	runes := []rune(f.view.Text)
	next := append([]rune{}, runes[:start]...)
	next = append(next, []rune(text)...)
	next = append(next, runes[end:]...)
	f.view.Text = string(next)
	f.view.DotStart = start
	f.view.DotEnd = start + len([]rune(text))
	return f.view, nil
}

func (f *fakeTermService) Undo() (wire.BufferView, error) {
	return f.view, nil
}

func (f *fakeTermService) Save() (string, error) {
	return "saved", nil
}

func TestNewBufferStateStartsAtDot(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\nbeta\ngamma\n",
		DotStart: 6,
		DotEnd:   10,
	})

	if got, want := state.cursor, 6; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
	if got, want := state.origin, 6; got != want {
		t.Fatalf("origin = %d, want %d", got, want)
	}
}

func TestMovePageDownByLines(t *testing.T) {
	t.Parallel()

	text := []rune("l1\nl2\nl3\nl4\nl5\n")
	if got, want := movePageDown(text, 0, 2), 6; got != want {
		t.Fatalf("movePageDown() = %d, want %d", got, want)
	}
}

func TestMoveLineDownPreservesColumn(t *testing.T) {
	t.Parallel()

	text := []rune("alpha\nxy\nomega\n")
	if got, want := moveLineDown(text, 3), 8; got != want {
		t.Fatalf("moveLineDown() = %d, want %d", got, want)
	}
}

func TestHandleBufferKeyCtrlAAndCtrlE(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\nbeta\n",
		DotStart: 8,
		DotEnd:   8,
	})
	handleBufferKey(state, 1)
	if got, want := state.cursor, 6; got != want {
		t.Fatalf("Ctrl-A cursor = %d, want %d", got, want)
	}
	handleBufferKey(state, 5)
	if got, want := state.cursor, 10; got != want {
		t.Fatalf("Ctrl-E cursor = %d, want %d", got, want)
	}
}

func TestHandleBufferKeyCtrlSpaceExtendsSelection(t *testing.T) {
	t.Parallel()

	state := newBufferState(wire.BufferView{
		Text:     "alpha\n",
		DotStart: 0,
		DotEnd:   0,
	})
	handleBufferKey(state, 0)
	handleBufferKey(state, keyRight)
	handleBufferKey(state, keyRight)

	if got, want := state.dotStart, 0; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := state.dotEnd, 2; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
	if !state.markMode {
		t.Fatalf("markMode = false, want true")
	}
}

func TestApplyBufferKeyPrintableReplacesSelection(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 0,
			DotEnd:   0,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, int('Z'))
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "Zalpha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 1; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
	if got, want := next.dotStart, 1; got != want {
		t.Fatalf("dotStart = %d, want %d", got, want)
	}
	if got, want := next.dotEnd, 1; got != want {
		t.Fatalf("dotEnd = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyBackspaceDeletesPreviousRune(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 2,
			DotEnd:   2,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, 127)
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "apha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 1; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyPrintableAdvancesAcrossRepeatedTyping(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\n",
			DotStart: 0,
			DotEnd:   0,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, int('X'))
	if err != nil {
		t.Fatalf("first applyBufferKey() error = %v", err)
	}
	next, err = applyBufferKey(svc, next, int('Y'))
	if err != nil {
		t.Fatalf("second applyBufferKey() error = %v", err)
	}

	if got, want := string(next.text), "XYalpha\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 2; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyCtrlKDeletesToLineEnd(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha\nbeta\n",
			DotStart: 2,
			DotEnd:   2,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, 11)
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "al\nbeta\n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 2; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestFindSelectionForwardWraps(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta alpha")
	if got, ok := findSelection(text, 11, 16, []rune("alpha"), true); !ok || got != 0 {
		t.Fatalf("findSelection(forward wrap) = (%d,%v), want (0,true)", got, ok)
	}
}

func TestFindSelectionBackwardFindsPrevious(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta alpha")
	if got, ok := findSelection(text, 11, 16, []rune("alpha"), false); !ok || got != 0 {
		t.Fatalf("findSelection(backward) = (%d,%v), want (0,true)", got, ok)
	}
}

func TestPrevAndNextWordStart(t *testing.T) {
	t.Parallel()

	text := []rune("alpha beta_gamma delta")
	if got, want := nextWordStart(text, 0), 6; got != want {
		t.Fatalf("nextWordStart() = %d, want %d", got, want)
	}
	if got, want := prevWordStart(text, len(text)), 17; got != want {
		t.Fatalf("prevWordStart() = %d, want %d", got, want)
	}
}

func TestApplyBufferKeyAltBackspaceDeletesWord(t *testing.T) {
	t.Parallel()

	svc := &fakeTermService{
		view: wire.BufferView{
			Text:     "alpha beta\n",
			DotStart: 10,
			DotEnd:   10,
		},
	}
	state := newBufferState(svc.view)

	next, err := applyBufferKey(svc, state, keyAltBackspace)
	if err != nil {
		t.Fatalf("applyBufferKey() error = %v", err)
	}
	if got, want := string(next.text), "alpha \n"; got != want {
		t.Fatalf("buffer text = %q, want %q", got, want)
	}
	if got, want := next.cursor, 6; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestReadBufferKeyPaste(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("\x1b[200~hello"))
	if _, _, err := reader.ReadRune(); err != nil {
		t.Fatalf("prime reader with ESC: %v", err)
	}
	got, err := readBufferKey(reader)
	if err != nil {
		t.Fatalf("readBufferKey() error = %v", err)
	}
	if got != keyPaste {
		t.Fatalf("readBufferKey() = %d, want keyPaste", got)
	}
}

func TestReadBracketedPaste(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("hello\x1b[201~tail"))
	got, err := readBracketedPaste(reader)
	if err != nil {
		t.Fatalf("readBracketedPaste() error = %v", err)
	}
	if want := "hello"; string(got) != want {
		t.Fatalf("readBracketedPaste() = %q, want %q", string(got), want)
	}
}
