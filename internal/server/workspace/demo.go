package workspace

import (
	"fmt"
	"strings"
	"unicode"

	"ion/internal/core/text"
	"ion/internal/proto/wire"
)

// DemoSymbol describes the current symbol under dot for the temporary LSP demo commands.
type DemoSymbol struct {
	Name     string
	FileID   int
	FileName string
	Start    int
	End      int
	Line     int
	Column   int
}

// DescribeCurrentSymbol resolves the identifier under the current dot.
func (w *Workspace) DescribeCurrentSymbol(state *SessionState) (DemoSymbol, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()
	return w.currentDemoSymbol()
}

// GotoDemoSymbol jumps to a plausible definition-like occurrence of the current symbol.
func (w *Workspace) GotoDemoSymbol(state *SessionState) (wire.BufferView, DemoSymbol, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	restoreState := w.withSessionState(state)
	defer restoreState()

	current, err := w.currentDemoSymbol()
	if err != nil {
		return wire.BufferView{}, DemoSymbol{}, err
	}

	currentFile := w.session.Current
	targetFile, start, end, err := w.findDemoSymbolTarget(currentFile, current.Name, current.Start, current.End)
	if err != nil {
		return wire.BufferView{}, DemoSymbol{}, err
	}

	r := text.Range{P1: text.Posn(start), P2: text.Posn(end)}
	w.session.Current = targetFile
	targetFile.Dot = r
	targetFile.NDot = r

	view, err := w.currentView()
	if err != nil {
		return wire.BufferView{}, DemoSymbol{}, err
	}
	info, err := w.demoSymbolAtRange(targetFile, start, end)
	if err != nil {
		return wire.BufferView{}, DemoSymbol{}, err
	}
	return view, info, nil
}

func (w *Workspace) currentDemoSymbol() (DemoSymbol, error) {
	if w.session == nil || w.session.Current == nil {
		return DemoSymbol{}, fmt.Errorf("no current file")
	}
	text, err := w.session.CurrentText()
	if err != nil {
		return DemoSymbol{}, err
	}
	dot := w.session.CurrentDot()
	start, end, name, err := resolveDemoSymbol(text, int(dot.P1), int(dot.P2))
	if err != nil {
		return DemoSymbol{}, err
	}
	return demoSymbolForText(w.session.Current, w.session.CurrentFileID(), text, start, end, name), nil
}

func (w *Workspace) findDemoSymbolTarget(currentFile *text.File, name string, currentStart, currentEnd int) (*text.File, int, int, error) {
	for _, wantDefinition := range []bool{true, false} {
		for _, preferCurrent := range []bool{false, true} {
			for _, f := range w.session.Files {
				if f == nil {
					continue
				}
				if preferCurrent != (f == currentFile) {
					continue
				}
				body, err := w.fileText(f)
				if err != nil {
					return nil, 0, 0, err
				}
				skipStart, skipEnd := -1, -1
				if f == currentFile {
					skipStart, skipEnd = currentStart, currentEnd
				}
				start, end, ok := findDemoSymbolOccurrence(body, name, skipStart, skipEnd, wantDefinition)
				if ok {
					return f, start, end, nil
				}
			}
		}
	}
	return nil, 0, 0, fmt.Errorf("no demo target for symbol %q", name)
}

func (w *Workspace) demoSymbolAtRange(f *text.File, start, end int) (DemoSymbol, error) {
	body, err := w.fileText(f)
	if err != nil {
		return DemoSymbol{}, err
	}
	runes := []rune(body)
	if start < 0 || end < start || end > len(runes) {
		return DemoSymbol{}, fmt.Errorf("demo symbol out of range")
	}
	fileID := 0
	if w.session.Current == f {
		fileID = w.session.CurrentFileID()
	}
	return demoSymbolForText(f, fileID, body, start, end, string(runes[start:end])), nil
}

func (w *Workspace) fileText(f *text.File) (string, error) {
	if f == nil {
		return "", nil
	}
	previous := w.session.Current
	w.session.Current = f
	err := w.session.LoadCurrentIfUnread()
	w.session.Current = previous
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if _, err := f.WriteTo(&b); err != nil {
		return "", err
	}
	return b.String(), nil
}

func resolveDemoSymbol(body string, dotStart, dotEnd int) (int, int, string, error) {
	runes := []rune(body)
	if len(runes) == 0 {
		return 0, 0, "", fmt.Errorf("no symbol under dot")
	}
	if dotStart < 0 {
		dotStart = 0
	}
	if dotEnd < dotStart {
		dotEnd = dotStart
	}
	if dotEnd > len(runes) {
		dotEnd = len(runes)
	}
	if dotStart > len(runes) {
		dotStart = len(runes)
	}
	if dotStart < dotEnd && demoIdentifierRange(runes, dotStart, dotEnd) {
		return dotStart, dotEnd, string(runes[dotStart:dotEnd]), nil
	}
	for _, pos := range []int{dotStart, dotStart - 1, dotEnd, dotEnd - 1} {
		if pos < 0 || pos >= len(runes) || !demoSymbolRune(runes[pos]) {
			continue
		}
		start := pos
		for start > 0 && demoSymbolRune(runes[start-1]) {
			start--
		}
		end := pos + 1
		for end < len(runes) && demoSymbolRune(runes[end]) {
			end++
		}
		return start, end, string(runes[start:end]), nil
	}
	return 0, 0, "", fmt.Errorf("no symbol under dot")
}

func demoSymbolForText(f *text.File, fileID int, body string, start, end int, name string) DemoSymbol {
	line, col := demoLineColumn(body, start)
	fileName := ""
	if f != nil {
		fileName = strings.TrimRight(strings.TrimSpace(f.Name.UTF8()), "\x00")
	}
	return DemoSymbol{
		Name:     name,
		FileID:   fileID,
		FileName: fileName,
		Start:    start,
		End:      end,
		Line:     line,
		Column:   col,
	}
}

func demoLineColumn(body string, offset int) (int, int) {
	line := 1
	col := 1
	for i, r := range []rune(body) {
		if i >= offset {
			break
		}
		if r == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col
}

func findDemoSymbolOccurrence(body, name string, skipStart, skipEnd int, wantDefinition bool) (int, int, bool) {
	runes := []rune(body)
	symbol := []rune(name)
	if len(symbol) == 0 || len(symbol) > len(runes) {
		return 0, 0, false
	}
	for i := 0; i+len(symbol) <= len(runes); i++ {
		end := i + len(symbol)
		if i == skipStart && end == skipEnd {
			continue
		}
		if !demoIdentifierBoundaries(runes, i, end) {
			continue
		}
		if string(runes[i:end]) != name {
			continue
		}
		if demoSymbolLooksLikeDefinition(runes, i) != wantDefinition {
			continue
		}
		return i, end, true
	}
	return 0, 0, false
}

func demoSymbolLooksLikeDefinition(runes []rune, start int) bool {
	lineStart := start
	for lineStart > 0 && runes[lineStart-1] != '\n' {
		lineStart--
	}
	prefix := strings.TrimSpace(string(runes[lineStart:start]))
	return prefix == "func" ||
		prefix == "type" ||
		prefix == "var" ||
		prefix == "const" ||
		strings.HasPrefix(prefix, "func (")
}

func demoIdentifierRange(runes []rune, start, end int) bool {
	if start < 0 || end > len(runes) || start >= end {
		return false
	}
	for _, r := range runes[start:end] {
		if !demoSymbolRune(r) {
			return false
		}
	}
	return demoIdentifierBoundaries(runes, start, end)
}

func demoIdentifierBoundaries(runes []rune, start, end int) bool {
	if start > 0 && demoSymbolRune(runes[start-1]) {
		return false
	}
	if end < len(runes) && demoSymbolRune(runes[end]) {
		return false
	}
	return true
}

func demoSymbolRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
