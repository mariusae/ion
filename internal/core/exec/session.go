package exec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	ionaddr "ion/internal/core/addr"
	ioncmd "ion/internal/core/cmdlang"
	ionregexp "ion/internal/core/regexp"
	"ion/internal/core/text"
)

// Session executes parsed sam commands over a set of files.
type Session struct {
	Files         []*text.File
	Current       *text.File
	Seq           uint32
	Out           io.Writer
	Diag          io.Writer
	QuitOK        bool
	LastShellCmd  string
	ShellInput    ShellInputMode
	closeOK       map[*text.File]bool
	fileIDs       map[*text.File]int
	nextFileID    int
	execDepth     int
	fileLoopDepth int
	frame         *execFrame
}

type ShellInputMode int

const (
	ShellInputEmpty ShellInputMode = iota
	ShellInputSocketEOF
)

type diagnosticError struct {
	msg string
}

func (e diagnosticError) Error() string {
	return e.msg
}

func (e diagnosticError) Diagnostic() string {
	return e.msg
}

// MenuFileInfo is the server-owned file-menu snapshot exposed to clients.
type MenuFileInfo struct {
	ID      int
	Name    string
	Dirty   bool
	Current bool
}

// NewSession constructs an execution session.
func NewSession(out io.Writer) *Session {
	return &Session{
		Out:        out,
		Diag:       io.Discard,
		ShellInput: ShellInputEmpty,
		closeOK:    make(map[*text.File]bool),
		fileIDs:    make(map[*text.File]int),
		nextFileID: 1,
	}
}

type execFrame struct {
	seq     uint32
	touched map[*text.File]struct{}
}

// AddFile registers a file with the session and makes it current if needed.
func (s *Session) AddFile(f *text.File) {
	oldFirst := s.firstFile()
	s.warnDuplicateName(trimToken(f.Name.UTF8()))
	s.ensureFileID(f)
	s.Files = append(s.Files, f)
	s.sortFiles()
	s.syncCloseOK(f)
	if s.Current == nil || s.Current == oldFirst {
		s.Current = s.firstFile()
	}
}

func (s *Session) ensureFileID(f *text.File) int {
	if f == nil {
		return 0
	}
	if id, ok := s.fileIDs[f]; ok {
		return id
	}
	id := s.nextFileID
	s.nextFileID++
	s.fileIDs[f] = id
	return id
}

// Execute runs one parsed command. It returns false when execution should stop.
func (s *Session) Execute(cmd *ioncmd.Cmd) (ok bool, err error) {
	if cmd == nil {
		return false, nil
	}
	s.execDepth++
	startedFrame := s.beginFrame()
	defer func() {
		if startedFrame {
			ferr := s.finishFrame(err == nil)
			if err == nil && ferr != nil {
				err = ferr
				ok = false
			}
		}
		s.execDepth--
	}()

	f, a, err := s.resolveCommandAddress(cmd)
	if err != nil {
		return false, err
	}
	if f != nil {
		s.Current = f
	}

	switch cmd.Cmdc {
	case '{':
		base := a
		if cmd.Addr == nil && f != nil {
			base = ionaddr.Address{F: f, R: f.Dot}
		}
		for sub := cmd.Cmd; sub != nil; sub = sub.Next {
			base.F.Dot = base.R
			s.Current = base.F
			ok, err := s.Execute(sub)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil

	case 'p':
		return true, s.display(f, a)

	case '\n':
		return true, s.newlineCmd(f, cmd, a)

	case 'a':
		return true, s.appendAt(f, cmd.Text, a.R.P2)

	case 'i':
		return true, s.appendAt(f, cmd.Text, a.R.P1)

	case 'c':
		if err := s.mutate(f, func(seq uint32) error {
			return s.replaceLogged(f, cmd.Text, a.R.P1, a.R.P2, seq)
		}); err != nil {
			return false, err
		}
		return true, nil

	case 'd':
		if err := s.mutate(f, func(seq uint32) error {
			if err := f.LogDelete(a.R.P1, a.R.P2, seq); err != nil {
				return err
			}
			f.NDot = text.Range{P1: a.R.P1, P2: a.R.P1}
			return nil
		}); err != nil {
			return false, err
		}
		return true, nil

	case 'k':
		f.Mark = a.R
		return true, nil

	case 't':
		if err := s.copyRange(a, cmd.AddrArg); err != nil {
			return false, err
		}
		return true, nil

	case 'm':
		if err := s.moveRange(a, cmd.AddrArg); err != nil {
			return false, err
		}
		return true, nil

	case 's':
		if err := s.substitute(f, cmd, a); err != nil {
			return false, err
		}
		return true, nil

	case 'g', 'v':
		if err := s.gCmd(f, cmd, a); err != nil {
			return false, err
		}
		return true, nil

	case 'x':
		if err := s.xCmd(f, cmd, a); err != nil {
			return false, err
		}
		return true, nil

	case 'y':
		if err := s.yCmd(f, cmd, a); err != nil {
			return false, err
		}
		return true, nil

	case 'X':
		if err := s.fileLoop(cmd, true); err != nil {
			return false, err
		}
		return true, nil

	case 'Y':
		if err := s.fileLoop(cmd, false); err != nil {
			return false, err
		}
		return true, nil

	case 'u':
		n := cmd.Num
		if n >= 0 {
			for n > 0 {
				if _, _, err := f.Undo(true, true); err != nil {
					return false, err
				}
				n--
			}
		} else {
			for n < 0 {
				if _, _, err := f.Undo(false, true); err != nil {
					return false, err
				}
				n++
			}
		}
		s.syncCloseOK(f)
		s.QuitOK = false
		return true, nil

	case 'f':
		if err := s.fileCmd(f, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'b':
		if err := s.switchFile(cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'B':
		if err := s.openFiles(cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'D':
		if err := s.closeFiles(cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'n':
		if err := s.listFiles(); err != nil {
			return false, err
		}
		return true, nil

	case 'r':
		if err := s.readFileInto(f, a, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'e':
		if err := s.editFileFromDisk(f, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'c' | 0x100:
		if err := s.changeDirectory(cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case '!':
		if err := s.shellBang(f, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case '<':
		if err := s.shellPipeIn(f, a, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case '>':
		if err := s.shellPipeOut(f, a, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case '|':
		if err := s.shellPipe(f, a, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case '=':
		if err := s.printAddress(f, a, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'w':
		if err := s.writeFile(f, a, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'q':
		if s.hasDirtyFiles() && !s.QuitOK {
			s.QuitOK = true
			if _, err := fmt.Fprintln(s.Diag, "?changed files"); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil

	default:
		return false, fmt.Errorf("command %q not implemented", cmd.Cmdc)
	}
}

func (s *Session) resolveCommandAddress(cmd *ioncmd.Cmd) (*text.File, ionaddr.Address, error) {
	f := s.Current

	eval := &ionaddr.Evaluator{
		Files:   s.Files,
		Current: s.Current,
		EnsureFile: func(f *text.File) error {
			return s.ensureLoadedForCommand(f, f != s.Current)
		},
	}
	ap := cmd.Addr
	if def := defaultAddrFor(cmd.Cmdc); def != 0 {
		if ap == nil && cmd.Cmdc != '\n' {
			ap = &ionaddr.Addr{Type: def}
		} else if ap != nil && ap.Type == '"' && ap.Next == nil && cmd.Cmdc != '\n' {
			ap.Next = &ionaddr.Addr{Type: def}
		}
	}
	if ap == nil {
		if f == nil {
			if commandNeedsCurrent(cmd.Cmdc) {
				return nil, ionaddr.Address{}, fmt.Errorf("no current file")
			}
			return nil, ionaddr.Address{}, nil
		}
		if commandNeedsCurrent(cmd.Cmdc) {
			if err := s.ensureLoadedForCommand(f, f != s.Current); err != nil {
				return nil, ionaddr.Address{}, err
			}
		}
		return f, ionaddr.Address{F: f, R: f.Dot}, nil
	}
	base := ionaddr.Address{}
	if f == nil {
		if commandNeedsCurrent(cmd.Cmdc) && !addressStartsWithFileSelector(ap) {
			return nil, ionaddr.Address{}, fmt.Errorf("no current file")
		}
	} else {
		base = ionaddr.Address{F: f, R: f.Dot}
	}
	a, err := eval.Resolve(ap, base, 0)
	if err != nil {
		return nil, ionaddr.Address{}, err
	}
	if err := s.ensureLoadedForCommand(a.F, a.F != s.Current); err != nil {
		return nil, ionaddr.Address{}, err
	}
	return a.F, a, nil
}

func addressStartsWithFileSelector(ap *ionaddr.Addr) bool {
	if ap == nil {
		return false
	}
	switch ap.Type {
	case '"':
		return true
	case ',', ';':
		return addressStartsWithFileSelector(ap.Left)
	default:
		return false
	}
}

func (s *Session) display(f *text.File, a ionaddr.Address) error {
	if f == nil {
		return fmt.Errorf("no file")
	}
	if a.R.P2 > text.Posn(f.B.Len()) {
		a.R.P2 = text.Posn(f.B.Len())
	}
	for p := a.R.P1; p < a.R.P2; {
		n := a.R.P2 - p
		if n > text.MaxBlock-1 {
			n = text.MaxBlock - 1
		}
		buf := make([]rune, n)
		if err := f.B.Read(p, buf); err != nil {
			return err
		}
		if _, err := io.WriteString(s.Out, string(buf)); err != nil {
			return err
		}
		p += n
	}
	f.Dot = a.R
	f.NDot = a.R
	return nil
}

func (s *Session) newlineCmd(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	if cmd.Addr == nil {
		addr0, err := ionaddr.LineAddr(0, ionaddr.Address{F: f, R: f.Dot}, -1)
		if err != nil {
			return err
		}
		a1, err := ionaddr.LineAddr(0, ionaddr.Address{F: f, R: f.Dot}, 1)
		if err != nil {
			return err
		}
		addr0.R.P2 = a1.R.P2
		if addr0.R == f.Dot {
			addr0, err = ionaddr.LineAddr(1, ionaddr.Address{F: f, R: f.Dot}, 1)
			if err != nil {
				return err
			}
		}
		return s.display(f, addr0)
	}
	return s.display(f, a)
}

func (s *Session) appendAt(f *text.File, txt *text.String, p text.Posn) error {
	return s.mutate(f, func(seq uint32) error {
		f.NDot = text.Range{P1: p, P2: p}
		return s.appendLogged(f, txt, p, seq)
	})
}

func (s *Session) appendLogged(f *text.File, txt *text.String, p text.Posn, seq uint32) error {
	if txt == nil {
		f.NDot = text.Range{P1: p, P2: p}
		return nil
	}
	runes := txt.Runes()
	if len(runes) > 0 && runes[len(runes)-1] == 0 {
		runes = runes[:len(runes)-1]
	}
	if len(runes) > 0 {
		if err := f.LogInsert(p, runes, seq); err != nil {
			return err
		}
	}
	f.NDot = text.Range{P1: p, P2: p + text.Posn(len(runes))}
	return nil
}

func (s *Session) replaceLogged(f *text.File, txt *text.String, start, end text.Posn, seq uint32) error {
	if end > start {
		if err := f.LogDelete(start, end, seq); err != nil {
			return err
		}
	}
	if err := s.appendLogged(f, txt, end, seq); err != nil {
		return err
	}
	f.NDot = text.Range{P1: start, P2: start + textStringLen(txt)}
	return nil
}

func (s *Session) mutate(f *text.File, fn func(seq uint32) error) error {
	seq := s.currentSeq()
	if err := fn(seq); err != nil {
		return err
	}
	s.touchFile(f)
	return nil
}

func (s *Session) beginFrame() bool {
	if s.frame != nil {
		return false
	}
	s.frame = &execFrame{
		seq:     s.Seq + 1,
		touched: make(map[*text.File]struct{}),
	}
	return true
}

func (s *Session) finishFrame(apply bool) error {
	frame := s.frame
	s.frame = nil
	if frame == nil {
		return nil
	}
	if !apply {
		return s.rollbackFrame(frame)
	}
	if err := s.applyFrame(frame); err != nil {
		return err
	}
	if len(frame.touched) != 0 {
		s.Seq = frame.seq
	}
	return nil
}

func (s *Session) currentSeq() uint32 {
	if s.frame != nil {
		return s.frame.seq
	}
	return s.Seq + 1
}

func (s *Session) touchFile(f *text.File) {
	if f == nil || s.frame == nil {
		return
	}
	s.frame.touched[f] = struct{}{}
	s.closeOK[f] = false
	s.QuitOK = false
}

func (s *Session) applyFrame(frame *execFrame) error {
	for _, f := range s.orderedTouchedFiles(frame) {
		if _, _, _, err := f.Update(false); err != nil {
			return err
		}
		s.syncCloseOK(f)
	}
	s.sortFiles()
	return nil
}

func (s *Session) rollbackFrame(frame *execFrame) error {
	for f := range frame.touched {
		if err := f.AbortPendingSequence(frame.seq); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) orderedTouchedFiles(frame *execFrame) []*text.File {
	ordered := make([]*text.File, 0, len(frame.touched))
	seen := make(map[*text.File]struct{}, len(frame.touched))
	for _, f := range s.Files {
		if _, ok := frame.touched[f]; !ok {
			continue
		}
		ordered = append(ordered, f)
		seen[f] = struct{}{}
	}
	for f := range frame.touched {
		if _, ok := seen[f]; ok {
			continue
		}
		ordered = append(ordered, f)
	}
	return ordered
}

func (s *Session) substitute(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	pat, err := ionregexp.Compile(cmd.Re)
	if err != nil {
		return err
	}

	var didSub bool
	var delta text.Posn
	n := cmd.Num
	if n == 0 {
		n = 1
	}
	op := text.Posn(-1)

	seq := s.currentSeq()
	for p1 := a.R.P1; p1 <= a.R.P2; {
		match, ok, err := pat.Execute(f, p1, a.R.P2)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		sel := match.P[0]
		if sel.P1 == sel.P2 {
			if sel.P1 == op {
				p1++
				continue
			}
			p1 = sel.P2 + 1
		} else {
			p1 = sel.P2
		}
		op = sel.P2
		n--
		if n > 0 {
			continue
		}

		repl, err := substituteText(f, cmd.Text, match)
		if err != nil {
			return err
		}
		if sel.P1 != sel.P2 {
			if err := f.LogDelete(sel.P1, sel.P2, seq); err != nil {
				return err
			}
			delta -= sel.P2 - sel.P1
		}
		if len(repl) > 0 {
			if err := f.LogInsert(sel.P2, repl, seq); err != nil {
				return err
			}
			delta += text.Posn(len(repl))
		}
		didSub = true
		if cmd.Flag == 0 {
			break
		}
	}
	if !didSub {
		return fmt.Errorf("substitution")
	}
	f.NDot = text.Range{P1: a.R.P1, P2: a.R.P2 + delta}
	s.touchFile(f)
	return nil
}

func (s *Session) xCmd(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	if cmd.Re == nil {
		return s.lineXCmd(f, cmd, a)
	}
	pat, err := ionregexp.Compile(cmd.Re)
	if err != nil {
		return err
	}
	r := a.R
	op := text.Posn(-1)
	for p := r.P1; p <= r.P2; {
		match, ok, err := pat.Execute(f, p, r.P2)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		sel := match.P[0]
		if sel.P1 == sel.P2 {
			if sel.P1 == op {
				p++
				continue
			}
			p = sel.P2 + 1
		} else {
			p = sel.P2
		}
		op = sel.P2
		f.Dot = sel
		f.NDot = sel
		if cmd.Cmd == nil {
			return fmt.Errorf("x command missing nested command")
		}
		ok, err = s.Execute(cmd.Cmd)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		pat, err = ionregexp.Compile(cmd.Re)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) lineXCmd(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	r := a.R
	a3 := ionaddr.Address{F: f, R: text.Range{P1: r.P1, P2: r.P1}}
	for p := r.P1; p < r.P2; p = a3.R.P2 {
		a3.R.P1 = a3.R.P2
		var linesel text.Range
		if p != r.P1 {
			next, err := ionaddr.LineAddr(1, a3, 1)
			if err != nil {
				return err
			}
			linesel = next.R
		} else {
			next, err := ionaddr.LineAddr(0, a3, 1)
			if err != nil {
				return err
			}
			linesel = next.R
			if linesel.P2 == p {
				next, err = ionaddr.LineAddr(1, a3, 1)
				if err != nil {
					return err
				}
				linesel = next.R
			}
		}
		if linesel.P1 >= r.P2 {
			break
		}
		if linesel.P2 >= r.P2 {
			linesel.P2 = r.P2
		}
		if !(linesel.P2 > linesel.P1 && linesel.P1 >= a3.R.P2 && linesel.P2 > a3.R.P2) {
			break
		}
		f.Dot = linesel
		f.NDot = linesel
		ok, err := s.runLoopBody(cmd)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		a3.R = linesel
	}
	return nil
}

func (s *Session) shellBang(f *text.File, token *text.String) error {
	cmd, err := s.resolveShellCommand(token)
	if err != nil {
		return err
	}
	res, err := s.runShellCommand(f, cmd, nil, false)
	if err != nil {
		return err
	}
	if err := s.writeShellStreams(res, true); err != nil {
		return err
	}
	return s.printShellPrompt()
}

func (s *Session) shellPipeIn(f *text.File, a ionaddr.Address, token *text.String) error {
	cmd, err := s.resolveShellCommand(token)
	if err != nil {
		return err
	}
	res, err := s.runShellCommand(f, cmd, nil, true)
	if err != nil {
		return err
	}
	if err := s.replaceWithShellOutput(f, a, res.Stdout); err != nil {
		return err
	}
	if err := s.writeShellStreams(res, false); err != nil {
		return err
	}
	if err := s.writeShellWarnings(res, true); err != nil {
		return err
	}
	return s.printShellPrompt()
}

func (s *Session) shellPipeOut(f *text.File, a ionaddr.Address, token *text.String) error {
	cmd, err := s.resolveShellCommand(token)
	if err != nil {
		return err
	}
	input, err := readRangeBytes(f, a.R)
	if err != nil {
		return err
	}
	res, err := s.runShellCommand(f, cmd, input, false)
	if err != nil {
		return err
	}
	if err := s.writeShellStreams(res, true); err != nil {
		return err
	}
	return s.printShellPrompt()
}

func (s *Session) shellPipe(f *text.File, a ionaddr.Address, token *text.String) error {
	cmd, err := s.resolveShellCommand(token)
	if err != nil {
		return err
	}
	input, err := readRangeBytes(f, a.R)
	if err != nil {
		return err
	}
	res, err := s.runShellCommand(f, cmd, input, true)
	if err != nil {
		return err
	}
	if err := s.replaceWithShellOutput(f, a, res.Stdout); err != nil {
		return err
	}
	if err := s.writeShellStreams(res, false); err != nil {
		return err
	}
	if err := s.writeShellWarnings(res, true); err != nil {
		return err
	}
	return s.printShellPrompt()
}

func (s *Session) shellFileList(src string) (string, []string, error) {
	token := text.NewStringFromUTF8(src)
	cmd, err := s.resolveShellCommand(&token)
	if err != nil {
		return "", nil, err
	}
	res, err := s.runShellCommand(nil, cmd, nil, true)
	if err != nil {
		return "", nil, err
	}
	if err := s.writeShellStreams(res, false); err != nil {
		return "", nil, err
	}
	if err := s.writeShellWarnings(res, true); err != nil {
		return "", nil, err
	}
	if err := s.printShellPrompt(); err != nil {
		return "", nil, err
	}
	out := strings.ReplaceAll(string(res.Stdout), "\x00", "")
	return out, strings.Fields(out), nil
}

func (s *Session) yCmd(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	if cmd.Re == nil {
		return s.lineXCmd(f, cmd, a)
	}
	pat, err := ionregexp.Compile(cmd.Re)
	if err != nil {
		return err
	}
	r := a.R
	op := r.P1
	prevMatchEnd := text.Posn(-1)
	for p := r.P1; p <= r.P2; {
		match, ok, err := pat.Execute(f, p, r.P2)
		if err != nil {
			return err
		}
		if !ok {
			if op > r.P2 {
				break
			}
			f.Dot = text.Range{P1: op, P2: r.P2}
			f.NDot = f.Dot
			ok, err := s.runLoopBody(cmd)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			break
		}

		sel := match.P[0]
		if sel.P1 == sel.P2 {
			if sel.P1 == prevMatchEnd {
				p++
				continue
			}
			p = sel.P2 + 1
		} else {
			p = sel.P2
		}
		prevMatchEnd = sel.P2

		f.Dot = text.Range{P1: op, P2: sel.P1}
		f.NDot = f.Dot
		op = sel.P2
		ok, err = s.runLoopBody(cmd)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		pat, err = ionregexp.Compile(cmd.Re)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) fileLoop(cmd *ioncmd.Cmd, wantMatch bool) error {
	if s.fileLoopDepth > 0 {
		return fmt.Errorf("can't nest X or Y")
	}
	s.fileLoopDepth++
	defer func() {
		s.fileLoopDepth--
	}()

	orig := s.Current
	files := append([]*text.File(nil), s.Files...)
	for _, f := range files {
		if f == nil {
			continue
		}
		matched, err := fileMatchesRegexp(f, cmd.Re)
		if err != nil {
			return err
		}
		if cmd.Re != nil && matched != wantMatch {
			continue
		}
		if s.Current != f {
			if err := s.printFileStatus(f, false); err != nil {
				return err
			}
		}
		s.Current = f
		ok, err := s.runLoopBody(cmd)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	if s.hasFile(orig) {
		s.Current = orig
	}
	return nil
}

func (s *Session) gCmd(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	if f != a.F {
		return fmt.Errorf("g/v command file mismatch")
	}
	pat, err := ionregexp.Compile(cmd.Re)
	if err != nil {
		return err
	}
	match, ok, err := pat.Execute(f, a.R.P1, a.R.P2)
	if err != nil {
		return err
	}
	shouldRun := ok
	if cmd.Cmdc == 'v' {
		shouldRun = !ok
	}
	if shouldRun {
		f.Dot = a.R
		f.NDot = a.R
		if cmd.Cmd == nil {
			return fmt.Errorf("%c command missing nested command", cmd.Cmdc)
		}
		cont, err := s.Execute(cmd.Cmd)
		if err != nil {
			return err
		}
		if !cont {
			return nil
		}
		_ = match
	}
	return nil
}

func (s *Session) runLoopBody(cmd *ioncmd.Cmd) (bool, error) {
	if cmd.Cmd == nil {
		return false, fmt.Errorf("%c command missing nested command", cmd.Cmdc)
	}
	return s.Execute(cmd.Cmd)
}

func (s *Session) copyRange(src ionaddr.Address, ap *ionaddr.Addr) error {
	dest, err := s.resolveAddrArg(src.F, ap)
	if err != nil {
		return err
	}
	seq := s.currentSeq()
	if err := s.copyRangeLogged(seq, src, dest); err != nil {
		return err
	}
	s.touchFile(dest.F)
	return nil
}

func (s *Session) moveRange(src ionaddr.Address, ap *ionaddr.Addr) error {
	dest, err := s.resolveAddrArg(src.F, ap)
	if err != nil {
		return err
	}
	seq := s.currentSeq()

	switch {
	case src.F == dest.F && src.R.P2 <= dest.R.P2:
		if err := src.F.LogDelete(src.R.P1, src.R.P2, seq); err != nil {
			return err
		}
		if err := s.copyRangeLogged(seq, src, dest); err != nil {
			return err
		}
	case src.F == dest.F && src.R.P1 < dest.R.P2:
		return fmt.Errorf("addresses overlap")
	default:
		if err := s.copyRangeLogged(seq, src, dest); err != nil {
			return err
		}
		if err := src.F.LogDelete(src.R.P1, src.R.P2, seq); err != nil {
			return err
		}
	}

	s.touchFile(dest.F)
	s.touchFile(src.F)
	return nil
}

func (s *Session) resolveAddrArg(current *text.File, ap *ionaddr.Addr) (ionaddr.Address, error) {
	if ap == nil {
		return ionaddr.Address{}, fmt.Errorf("address")
	}
	base := ionaddr.Address{}
	if current != nil {
		base = ionaddr.Address{F: current, R: current.Dot}
	}
	eval := &ionaddr.Evaluator{
		Files:   s.Files,
		Current: s.Current,
		EnsureFile: func(f *text.File) error {
			return s.ensureLoadedForCommand(f, false)
		},
	}
	return eval.Resolve(ap, base, 0)
}

func (s *Session) copyRangeLogged(seq uint32, src, dest ionaddr.Address) error {
	size := src.R.P2 - src.R.P1
	if size < 0 {
		return fmt.Errorf("addresses out of order")
	}
	if size == 0 {
		dest.F.NDot = text.Range{P1: dest.R.P2, P2: dest.R.P2}
		return nil
	}
	for p := src.R.P1; p < src.R.P2; {
		n := src.R.P2 - p
		if n > text.MaxStringRunes {
			n = text.MaxStringRunes
		}
		buf := make([]rune, n)
		if err := src.F.B.Read(p, buf); err != nil {
			return err
		}
		if err := dest.F.LogInsert(dest.R.P2, buf, seq); err != nil {
			return err
		}
		p += n
	}
	dest.F.NDot = text.Range{P1: dest.R.P2, P2: dest.R.P2 + size}
	return nil
}

func (s *Session) writeFile(f *text.File, a ionaddr.Address, nameToken *text.String) error {
	name := fileNameForWrite(f, nameToken)
	if name == "" {
		return fmt.Errorf("no file name")
	}
	if f.Seq == s.currentSeq() {
		return fmt.Errorf("can't write while changing: %q", name)
	}

	currentName := trimToken(f.Name.UTF8())
	explicitName := trimToken(nameTokenUTF8(nameToken))
	newFile := currentName == ""

	if !newFile {
		if meta, ok, err := statFile(currentName); err != nil {
			return err
		} else if !ok {
			newFile = true
		} else if name == currentName && f.StatKnown && (f.Dev != meta.dev || f.Inode != meta.inode || f.Mtime < meta.mtime) {
			f.SetFileInfo(meta.dev, meta.inode, meta.mtime)
			if _, err := fmt.Fprintf(s.Diag, "?warning: write might change good version of `%s'\n", name); err != nil {
				return err
			}
			return nil
		}
	}
	if currentName == "" && explicitName != "" {
		next := text.NewStringFromUTF8(explicitName)
		if err := s.mutate(f, func(seq uint32) error {
			return f.LogSetName(&next, seq)
		}); err != nil {
			return err
		}
	}

	var b strings.Builder
	if _, err := f.WriteRangeTo(&b, a.R.P1, a.R.P2); err != nil {
		return err
	}
	if err := os.WriteFile(name, []byte(b.String()), 0o666); err != nil {
		return createFileError(name, err)
	}

	fullWrite := a.R.P1 == 0 && a.R.P2 == text.Posn(f.B.Len())
	if fullWrite && (currentName == "" || name == currentName) {
		f.MarkClean()
	}
	if currentName == "" || name == currentName {
		if meta, ok, err := statFile(name); err != nil {
			return err
		} else if ok {
			f.SetFileInfo(meta.dev, meta.inode, meta.mtime)
		} else {
			f.ClearFileInfo()
		}
	}
	s.QuitOK = false
	s.syncCloseOK(f)

	if _, err := fmt.Fprintf(s.Diag, "%s: ", name); err != nil {
		return err
	}
	if newFile {
		if _, err := fmt.Fprint(s.Diag, "(new file) "); err != nil {
			return err
		}
	}

	missingFinalNewline := false
	if a.R.P2 > a.R.P1 {
		r, err := f.ReadRune(a.R.P2 - 1)
		if err != nil {
			return err
		}
		missingFinalNewline = r != '\n'
	}
	if missingFinalNewline {
		if _, err := fmt.Fprintln(s.Diag, "?warning: last char not newline"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.Diag, "#%d\n", len(b.String())); err != nil {
		return err
	}
	return nil
}

func (s *Session) fileCmd(f *text.File, nameToken *text.String) error {
	if f == nil {
		return fmt.Errorf("no file")
	}
	name := trimToken(nameTokenUTF8(nameToken))
	warnDup := false
	if name != "" {
		oldName := trimToken(f.Name.UTF8())
		next := text.NewStringFromUTF8(name)
		if err := s.mutate(f, func(seq uint32) error {
			return f.LogSetName(&next, seq)
		}); err != nil {
			return err
		}
		if name != oldName {
			warnDup = s.hasDuplicateNameExcept(name, f)
		}
	}
	if name != "" {
		if err := s.printFileStatusName(f.Mod, true, name); err != nil {
			return err
		}
		if warnDup {
			return s.printDuplicateNameWarning(name)
		}
		return nil
	}
	return s.printFileStatus(f, true)
}

func (s *Session) readFileInto(f *text.File, a ionaddr.Address, nameToken *text.String) error {
	name := fileNameForWrite(f, nameToken)
	if name == "" {
		return fmt.Errorf("no file name")
	}
	currentName := trimToken(f.Name.UTF8())
	explicitName := trimToken(nameTokenUTF8(nameToken))
	wasEmpty := f.B.Len() == 0 && (currentName == "" || currentName == name)
	data, err := os.ReadFile(name)
	if err != nil {
		return openFileError(name, err)
	}
	txt, runeCount, err := textStringFromBytes(data)
	if err != nil {
		return err
	}
	if err := s.mutate(f, func(seq uint32) error {
		if currentName == "" && explicitName != "" {
			next := text.NewStringFromUTF8(explicitName)
			if err := f.LogSetName(&next, seq); err != nil {
				return err
			}
		}
		if err := s.replaceLogged(f, txt, a.R.P1, a.R.P2, seq); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(s.Diag, "#%d\n", len(data)); err != nil {
			return err
		}
		f.NDot = text.Range{P1: a.R.P1, P2: a.R.P1 + runeCount}
		return nil
	}); err != nil {
		return err
	}
	if wasEmpty && !containsNullByte(data) {
		f.MarkClean()
		if meta, ok, err := statFile(name); err != nil {
			return err
		} else if ok {
			f.SetFileInfo(meta.dev, meta.inode, meta.mtime)
		} else {
			f.ClearFileInfo()
		}
		s.QuitOK = true
	}
	return nil
}

func (s *Session) editFileFromDisk(f *text.File, nameToken *text.String) error {
	name := fileNameForWrite(f, nameToken)
	if name == "" {
		return fmt.Errorf("no file name")
	}
	explicitName := trimToken(nameTokenUTF8(nameToken))
	data, err := os.ReadFile(name)
	if err != nil {
		return openFileError(name, err)
	}
	txt, _, err := textStringFromBytes(data)
	if err != nil {
		return err
	}
	if err := s.mutate(f, func(seq uint32) error {
		end := text.Posn(f.B.Len())
		if explicitName != "" {
			sname := text.NewStringFromUTF8(name)
			if err := f.LogSetName(&sname, seq); err != nil {
				return err
			}
		}
		if err := s.replaceLogged(f, txt, 0, end, seq); err != nil {
			return err
		}
		f.NDot = text.Range{}
		f.Mark = text.Range{}
		return nil
	}); err != nil {
		return err
	}
	if meta, ok, err := statFile(name); err != nil {
		return err
	} else if ok {
		f.SetFileInfo(meta.dev, meta.inode, meta.mtime)
	} else {
		f.ClearFileInfo()
	}
	if !containsNullByte(data) {
		f.MarkClean()
	}
	s.Current = f
	s.QuitOK = !containsNullByte(data)
	s.syncCloseOK(f)
	return s.printFileStatusName(f.Mod, true, name)
}

func (s *Session) changeDirectory(nameToken *text.String) error {
	oldwd, err := os.Getwd()
	if err != nil {
		return err
	}
	target := trimToken(nameTokenUTF8(nameToken))
	if target == "" {
		target = strings.TrimSpace(os.Getenv("HOME"))
		if target == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			target = home
		}
	}
	if err := os.Chdir(target); err != nil {
		return diagnosticError{msg: fmt.Sprintf("chdir: ?I/O error: %q", ioErrText(err))}
	}
	newwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(s.Diag, "!"); err != nil {
		return err
	}
	s.rewriteRelativeNames(oldwd, newwd)
	return nil
}

func (s *Session) openFiles(nameToken *text.String) error {
	list, err := s.loadFileList(nameToken)
	if err != nil {
		return err
	}
	if list.noArg {
		return fmt.Errorf("blank expected")
	}
	if list.emptyList {
		if list.fromShell {
			return fmt.Errorf("blank expected")
		}
		return s.openNamelessFile()
	}
	return s.openFileFields(list.fields)
}

// OpenFilesPaths opens one explicit file list without reparsing a command token.
func (s *Session) OpenFilesPaths(files []string) error {
	if len(files) == 0 {
		return nil
	}
	return s.openFileFields(files)
}

type openFilesSnapshot struct {
	files      []*text.File
	current    *text.File
	closeOK    map[*text.File]bool
	fileIDs    map[*text.File]int
	nextFileID int
}

// OpenFilesPathsAtomic opens one explicit file list and rolls back the session
// state if the final file cannot be loaded.
func (s *Session) OpenFilesPathsAtomic(files []string) error {
	snapshot := s.snapshotOpenFiles()
	if err := s.OpenFilesPaths(files); err != nil {
		s.restoreOpenFiles(snapshot)
		return err
	}
	return nil
}

func (s *Session) snapshotOpenFiles() openFilesSnapshot {
	snapshot := openFilesSnapshot{
		files:      append([]*text.File(nil), s.Files...),
		current:    s.Current,
		closeOK:    make(map[*text.File]bool, len(s.closeOK)),
		fileIDs:    make(map[*text.File]int, len(s.fileIDs)),
		nextFileID: s.nextFileID,
	}
	for f, ok := range s.closeOK {
		snapshot.closeOK[f] = ok
	}
	for f, id := range s.fileIDs {
		snapshot.fileIDs[f] = id
	}
	return snapshot
}

func (s *Session) restoreOpenFiles(snapshot openFilesSnapshot) {
	preserved := make(map[*text.File]struct{}, len(snapshot.files))
	for _, f := range snapshot.files {
		preserved[f] = struct{}{}
	}
	for _, f := range s.Files {
		if _, ok := preserved[f]; ok {
			continue
		}
		_ = f.Close()
	}
	s.Files = append([]*text.File(nil), snapshot.files...)
	s.Current = snapshot.current
	s.closeOK = make(map[*text.File]bool, len(snapshot.closeOK))
	for f, ok := range snapshot.closeOK {
		s.closeOK[f] = ok
	}
	s.fileIDs = make(map[*text.File]int, len(snapshot.fileIDs))
	for f, id := range snapshot.fileIDs {
		s.fileIDs[f] = id
	}
	s.nextFileID = snapshot.nextFileID
}

func (s *Session) openFileFields(fields []string) error {
	if len(fields) == 1 {
		if current := s.Current; current != nil && trimToken(current.Name.UTF8()) == fields[0] {
			if shouldOpenNamelessForCurrent(current) {
				return s.openNamelessFile()
			}
		}
	}
	var current *text.File
	currentExisted := false
	currentWasUnread := false
	for _, name := range fields {
		f := s.findFileByName(name)
		existed := f != nil
		wasUnread := existed && f.Unread
		if f == nil {
			d, err := text.NewDisk()
			if err != nil {
				return err
			}
			f = text.NewFile(d)
			next := text.NewStringFromUTF8(name)
			if err := f.Name.DupString(&next); err != nil {
				return err
			}
			s.AddFile(f)
		}
		current = f
		currentExisted = existed
		currentWasUnread = wasUnread
	}
	s.Current = current
	if current == nil {
		return fmt.Errorf("blank expected")
	}
	if current.Unread {
		if err := loadUnreadFile(current); err != nil {
			if statusErr := s.printFileStatus(current, true); statusErr != nil {
				return statusErr
			}
			return err
		}
	}
	if currentExisted && !currentWasUnread {
		return s.printFileStatusName(current.Mod, true, "")
	}
	return s.printFileStatus(current, true)
}

func (s *Session) openNamelessFile() error {
	d, err := text.NewDisk()
	if err != nil {
		return err
	}
	f := text.NewFile(d)
	s.AddFile(f)
	s.Current = f
	return s.printFileStatus(f, true)
}

func shouldOpenNamelessForCurrent(f *text.File) bool {
	if f == nil {
		return false
	}
	return f.StatKnown || f.Mod || f.B.Len() > 0
}

func (s *Session) closeFiles(nameToken *text.String) error {
	list, err := s.loadFileList(nameToken)
	if err != nil {
		return err
	}
	if list.noArg {
		if s.Current == nil {
			return fmt.Errorf("no current file")
		}
		return s.removeFile(s.Current)
	}
	if list.emptyList {
		if list.fromShell {
			return nil
		}
		return fmt.Errorf("newline expected")
	}
	for _, name := range list.fields {
		f := s.findFileByName(name)
		if f == nil {
			if err := s.printNoSuchFileWarning(name); err != nil {
				return err
			}
			continue
		}
		if err := s.removeFile(f); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) switchFile(nameToken *text.String) error {
	list, err := s.loadFileList(nameToken)
	if err != nil {
		return err
	}
	if list.noArg {
		return fmt.Errorf("blank expected")
	}
	name := ""
	displayName := ""
	if len(list.fields) > 0 {
		name = list.fields[0]
		displayName = name
	} else if list.fromShell {
		displayName = list.raw
	}
	if list.fromShell && list.raw != "" {
		displayName = list.raw
	}
	for _, f := range s.Files {
		if trimToken(f.Name.UTF8()) != name {
			continue
		}
		if f.Unread {
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if _, _, err := f.LoadInitial(strings.NewReader(string(data))); err != nil {
				return err
			}
		}
		s.Current = f
		return s.printFileStatus(f, true)
	}
	return diagnosticError{msg: fmt.Sprintf("?not in menu: \"%s\"", displayName)}
}

func (s *Session) findFileByName(name string) *text.File {
	for _, f := range s.Files {
		if trimToken(f.Name.UTF8()) == name {
			return f
		}
	}
	return nil
}

func (s *Session) hasFile(target *text.File) bool {
	for _, f := range s.Files {
		if f == target {
			return true
		}
	}
	return false
}

func (s *Session) rewriteRelativeNames(oldwd, newwd string) {
	for _, f := range s.Files {
		if f == nil {
			continue
		}
		name := trimToken(f.Name.UTF8())
		if name == "" || filepath.IsAbs(name) {
			continue
		}
		s.setFileName(f, normalizeNameForCWD(newwd, filepath.Join(oldwd, name)))
	}
	for _, f := range s.Files {
		if f == nil {
			continue
		}
		name := trimToken(f.Name.UTF8())
		if name == "" || !filepath.IsAbs(name) {
			continue
		}
		if !isUnderDir(newwd, name) {
			continue
		}
		s.setFileName(f, normalizeNameForCWD(newwd, name))
	}
	s.sortFiles()
}

func (s *Session) setFileName(f *text.File, name string) {
	next := text.NewStringFromUTF8(name)
	_ = f.Name.DupString(&next)
}

func (s *Session) firstFile() *text.File {
	if len(s.Files) == 0 {
		return nil
	}
	return s.Files[0]
}

func (s *Session) removeFile(target *text.File) error {
	if target != nil && target.IsDirty() && !s.closeOK[target] {
		s.closeOK[target] = true
		name := trimToken(target.Name.UTF8())
		if name == "" {
			name = "nameless file"
		}
		return fmt.Errorf("changes to %q", name)
	}
	for i, f := range s.Files {
		if f != target {
			continue
		}
		copy(s.Files[i:], s.Files[i+1:])
		s.Files = s.Files[:len(s.Files)-1]
		if s.Current == target {
			s.Current = s.firstFile()
		}
		delete(s.closeOK, target)
		return target.Close()
	}
	return nil
}

func (s *Session) sortFiles() {
	sort.SliceStable(s.Files, func(i, j int) bool {
		return trimToken(s.Files[i].Name.UTF8()) < trimToken(s.Files[j].Name.UTF8())
	})
}

func (s *Session) listFiles() error {
	for _, f := range s.Files {
		if err := s.printFileStatus(f, f == s.Current); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) warnDuplicateName(name string) {
	if s.hasDuplicateNameExcept(name, nil) {
		_ = s.printDuplicateNameWarning(name)
	}
}

func (s *Session) hasDuplicateNameExcept(name string, skip *text.File) bool {
	for _, f := range s.Files {
		if f == skip {
			continue
		}
		if trimToken(f.Name.UTF8()) != name {
			continue
		}
		return true
	}
	return false
}

func (s *Session) printDuplicateNameWarning(name string) error {
	_, err := fmt.Fprintf(s.Diag, "?warning: duplicate file name `%s'\n", name)
	return err
}

func (s *Session) printNoSuchFileWarning(name string) error {
	_, err := fmt.Fprintf(s.Diag, "?warning: no such file `%s'\n", name)
	return err
}

func (s *Session) printFileStatus(f *text.File, current bool) error {
	if f == nil {
		return fmt.Errorf("no file")
	}
	return s.printFileStatusName(f.Mod, current, trimToken(f.Name.UTF8()))
}

func (s *Session) printFileStatusName(modified, current bool, name string) error {
	mod := ' '
	if modified {
		mod = '\''
	}
	cur := ' '
	if current {
		cur = '.'
	}
	_, err := fmt.Fprintf(s.Diag, "%c-%c %s\n", mod, cur, trimToken(name))
	return err
}

// PrintCurrentStatus prints the current file status line, if any.
func (s *Session) PrintCurrentStatus() error {
	if s.Current == nil {
		return nil
	}
	return s.printFileStatus(s.Current, true)
}

// CurrentText returns the current file contents as UTF-8 text.
func (s *Session) CurrentText() (string, error) {
	if s.Current == nil {
		return "", nil
	}
	if err := s.LoadCurrentIfUnread(); err != nil {
		return "", err
	}
	var b strings.Builder
	if _, err := s.Current.WriteTo(&b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// CurrentDot returns the current file's selection range.
func (s *Session) CurrentDot() text.Range {
	if s.Current == nil {
		return text.Range{}
	}
	return s.Current.Dot
}

// SetCurrentDot updates the current file selection without mutating file text.
func (s *Session) SetCurrentDot(start, end text.Posn) error {
	if s.Current == nil {
		return fmt.Errorf("no current file")
	}
	if err := s.LoadCurrentIfUnread(); err != nil {
		return err
	}
	if start < 0 || end < start || int(end) > s.Current.B.Len() {
		return fmt.Errorf("dot out of range")
	}
	r := text.Range{P1: start, P2: end}
	s.Current.Dot = r
	s.Current.NDot = r
	return nil
}

// SetCurrentAddress resolves one sam address against the current file and makes it dot.
func (s *Session) SetCurrentAddress(expr string) error {
	parser := ioncmd.NewParser(expr + "\n")
	cmd, err := parser.Parse()
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Cmdc != '\n' || cmd.Addr == nil {
		return fmt.Errorf("address")
	}
	f, a, err := s.resolveCommandAddress(cmd)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("no current file")
	}
	s.Current = f
	f.Dot = a.R
	f.NDot = a.R
	return nil
}

// ReplaceCurrent replaces the current file range with UTF-8 text.
func (s *Session) ReplaceCurrent(start, end text.Posn, replacement string) (err error) {
	if s.Current == nil {
		return fmt.Errorf("no current file")
	}
	if err := s.LoadCurrentIfUnread(); err != nil {
		return err
	}
	if start < 0 || end < start || int(end) > s.Current.B.Len() {
		return fmt.Errorf("replace range out of bounds")
	}
	if start == end && replacement == "" {
		return s.SetCurrentDot(start, end)
	}

	startedFrame := s.beginFrame()
	defer func() {
		if startedFrame {
			ferr := s.finishFrame(err == nil)
			if err == nil && ferr != nil {
				err = ferr
			}
		}
	}()

	f := s.Current
	return s.mutate(f, func(seq uint32) error {
		if replacement == "" {
			if start != end {
				if err := f.LogDelete(start, end, seq); err != nil {
					return err
				}
			}
			f.NDot = text.Range{P1: start, P2: start}
			return nil
		}
		txt := text.NewStringFromUTF8(replacement)
		return s.replaceLogged(f, &txt, start, end, seq)
	})
}

// UndoCurrent undoes the most recent change in the current file.
func (s *Session) UndoCurrent() error {
	if s.Current == nil {
		return fmt.Errorf("no current file")
	}
	if err := s.LoadCurrentIfUnread(); err != nil {
		return err
	}
	q0, q1, err := s.Current.Undo(true, true)
	if err != nil {
		return err
	}
	r := text.Range{P1: q0, P2: q1}
	s.Current.Dot = r
	s.Current.NDot = r
	s.syncCloseOK(s.Current)
	s.QuitOK = false
	return nil
}

// SaveCurrent writes the current file and returns sam's status line.
func (s *Session) SaveCurrent() (string, error) {
	if s.Current == nil {
		return "", fmt.Errorf("no current file")
	}
	if err := s.LoadCurrentIfUnread(); err != nil {
		return "", err
	}
	oldDiag := s.Diag
	var b bytes.Buffer
	s.Diag = &b
	defer func() {
		s.Diag = oldDiag
	}()
	a := ionaddr.Address{
		F: s.Current,
		R: text.Range{P1: 0, P2: text.Posn(s.Current.B.Len())},
	}
	if err := s.writeFile(s.Current, a, nil); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// LoadCurrentIfUnread materializes the current file contents on demand.
func (s *Session) LoadCurrentIfUnread() error {
	if s.Current == nil {
		return nil
	}
	return s.ensureLoadedForCommand(s.Current, false)
}

// MenuFiles returns the current file-menu ordering and status flags.
func (s *Session) MenuFiles() []MenuFileInfo {
	out := make([]MenuFileInfo, 0, len(s.Files))
	for _, f := range s.Files {
		if f == nil {
			continue
		}
		out = append(out, MenuFileInfo{
			ID:      s.ensureFileID(f),
			Name:    trimToken(f.Name.UTF8()),
			Dirty:   f.Mod,
			Current: f == s.Current,
		})
	}
	return out
}

func (s *Session) CurrentFileID() int {
	return s.ensureFileID(s.Current)
}

// FocusFileID selects one file from the current menu ordering by stable ID.
func (s *Session) FocusFileID(id int) error {
	if id <= 0 {
		return fmt.Errorf("file id out of range")
	}
	var f *text.File
	for _, candidate := range s.Files {
		if candidate == nil {
			continue
		}
		if s.ensureFileID(candidate) == id {
			f = candidate
			break
		}
	}
	if f == nil {
		return fmt.Errorf("file id out of range")
	}
	if f.Unread {
		if err := loadUnreadFile(f); err != nil {
			return err
		}
	}
	s.Current = f
	return nil
}

func (s *Session) hasDirtyFiles() bool {
	for _, f := range s.Files {
		if f != nil && f.IsDirty() {
			return true
		}
	}
	return false
}

func (s *Session) syncCloseOK(f *text.File) {
	if f == nil {
		return
	}
	s.closeOK[f] = !f.Mod
}

func defaultAddrFor(cmdc rune) rune {
	switch cmdc {
	case 'a', 'c', 'd', 'g', 'i', 'p', 's', 'v', 'x', 'y', '\n':
		return '.'
	case 'w':
		return '*'
	default:
		return 0
	}
}

func commandNeedsCurrent(cmdc rune) bool {
	switch cmdc {
	case '!', 'b', 'B', 'D', 'n', 'q', 'X', 'Y', 'c' | 0x100:
		return false
	default:
		return true
	}
}

func fileNameForWrite(f *text.File, token *text.String) string {
	if token != nil {
		name := trimToken(token.UTF8())
		if name != "" {
			return name
		}
	}
	return trimToken(f.Name.UTF8())
}

func trimToken(s string) string {
	return strings.TrimRight(strings.TrimSpace(strings.TrimRight(s, "\x00")), "\x00")
}

func nameTokenUTF8(s *text.String) string {
	if s == nil {
		return ""
	}
	return s.UTF8()
}

func textStringLen(s *text.String) text.Posn {
	if s == nil {
		return 0
	}
	runes := s.Runes()
	if len(runes) > 0 && runes[len(runes)-1] == 0 {
		return text.Posn(len(runes) - 1)
	}
	return text.Posn(len(runes))
}

func textStringFromBytes(data []byte) (*text.String, text.Posn, error) {
	s := text.NewString()
	count := text.Posn(0)
	for _, r := range string(data) {
		if err := s.Add(r); err != nil {
			return nil, 0, err
		}
		count++
	}
	if err := s.Add(0); err != nil {
		return nil, 0, err
	}
	return &s, count, nil
}

func containsNullByte(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func loadUnreadFile(f *text.File) error {
	name := trimToken(f.Name.UTF8())
	if name == "" {
		f.Unread = false
		f.ClearFileInfo()
		return nil
	}
	data, err := os.ReadFile(name)
	if err != nil {
		f.Unread = false
		return openFileError(name, err)
	}
	if err := resetFileContents(f); err != nil {
		return err
	}
	if _, _, err := f.LoadInitial(strings.NewReader(string(data))); err != nil {
		return err
	}
	if meta, ok, err := statFile(name); err != nil {
		return err
	} else if ok {
		f.SetFileInfo(meta.dev, meta.inode, meta.mtime)
	} else {
		f.ClearFileInfo()
	}
	return nil
}

func (s *Session) ensureLoadedForCommand(f *text.File, reportStatus bool) error {
	if f == nil || !f.Unread {
		return nil
	}
	if err := loadUnreadFile(f); err != nil {
		return err
	}
	if reportStatus {
		return s.printFileStatus(f, false)
	}
	return nil
}

func tokenFields(s *text.String) []string {
	if s == nil {
		return nil
	}
	return strings.Fields(trimToken(s.UTF8()))
}

func rawTokenUTF8(s *text.String) string {
	if s == nil {
		return ""
	}
	return strings.TrimRight(s.UTF8(), "\x00")
}

type fileListSpec struct {
	raw       string
	fields    []string
	noArg     bool
	emptyList bool
	fromShell bool
}

func (s *Session) loadFileList(token *text.String) (fileListSpec, error) {
	raw := rawTokenUTF8(token)
	if raw == "" {
		return fileListSpec{noArg: true}, nil
	}
	if raw[0] != ' ' && raw[0] != '\t' {
		return fileListSpec{}, fmt.Errorf("blank expected")
	}
	rest := strings.TrimLeft(raw, " \t")
	if rest == "" {
		return fileListSpec{emptyList: true}, nil
	}
	if rest[0] == '<' {
		shellRaw, fields, err := s.shellFileList(rest[1:])
		if err != nil {
			return fileListSpec{}, err
		}
		return fileListSpec{
			raw:       shellRaw,
			fields:    fields,
			emptyList: len(fields) == 0,
			fromShell: true,
		}, nil
	}
	fields := strings.Fields(rest)
	return fileListSpec{
		raw:       rest,
		fields:    fields,
		emptyList: len(fields) == 0,
	}, nil
}

func normalizeNameForCWD(cwd, name string) string {
	clean := filepath.Clean(name)
	if clean == "." {
		return ""
	}
	if rel, err := filepath.Rel(cwd, clean); err == nil {
		if rel == "." {
			return ""
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return clean
}

func isUnderDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

type shellResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

func (s *Session) resolveShellCommand(token *text.String) (string, error) {
	raw := rawTokenUTF8(token)
	if raw == "" {
		if s.LastShellCmd == "" {
			return "", fmt.Errorf("plan 9 command")
		}
		return s.LastShellCmd, nil
	}
	s.LastShellCmd = raw
	return raw, nil
}

func (s *Session) runShellCommand(f *text.File, cmd string, stdin []byte, captureStdout bool) (shellResult, error) {
	c := osexec.Command("/bin/sh", "-c", cmd)
	c.Args[0] = "sh"
	c.Env = append(os.Environ(), shellEnv(f)...)
	var shellStdin *os.File
	var err error
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	} else {
		shellStdin, err = s.shellStdin()
		if err != nil {
			return shellResult{}, err
		}
		defer shellStdin.Close()
		c.Stdin = shellStdin
	}
	if captureStdout {
		var stderr bytes.Buffer
		c.Stderr = &stderr
		stdout, err := c.Output()
		res := shellResult{Stdout: stdout, Stderr: stderr.Bytes()}
		if err == nil {
			return res, nil
		}
		var exitErr *osexec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return shellResult{}, err
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err = c.Run()
	res := shellResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	if err == nil {
		return res, nil
	}
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return shellResult{}, err
}

func (s *Session) shellStdin() (*os.File, error) {
	switch s.ShellInput {
	case ShellInputSocketEOF:
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}
		_ = syscall.Close(fds[1])
		return os.NewFile(uintptr(fds[0]), "ion-shell-stdin"), nil
	default:
		r, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		return r, nil
	}
}

func shellEnv(f *text.File) []string {
	name := ""
	if f != nil {
		name = trimToken(f.Name.UTF8())
	}
	return []string{
		"samfile=" + name,
		"%=" + name,
	}
}

func (s *Session) writeShellWarnings(res shellResult, warnOnExit bool) error {
	if warnOnExit && res.ExitCode != 0 {
		if _, err := fmt.Fprintln(s.Diag, "?warning: exit status not 0"); err != nil {
			return err
		}
	}
	if containsNullByte(res.Stdout) {
		if _, err := fmt.Fprintln(s.Diag, "?warning: null characters elided"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) writeShellStreams(res shellResult, writeStdout bool) error {
	if writeStdout && len(res.Stdout) > 0 {
		if _, err := s.Out.Write(res.Stdout); err != nil {
			return err
		}
	}
	if len(res.Stderr) > 0 {
		if _, err := s.Diag.Write(res.Stderr); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) printShellPrompt() error {
	if s.execDepth != 1 {
		return nil
	}
	_, err := fmt.Fprintln(s.Diag, "!")
	return err
}

func (s *Session) replaceWithShellOutput(f *text.File, a ionaddr.Address, data []byte) error {
	txt, _, err := textStringFromBytesElidingNulls(data)
	if err != nil {
		return err
	}
	return s.mutate(f, func(seq uint32) error {
		return s.replaceLogged(f, txt, a.R.P1, a.R.P2, seq)
	})
}

func fileMatchesRegexp(f *text.File, re *text.String) (bool, error) {
	if re == nil {
		return true, nil
	}
	pat, err := ionregexp.Compile(re)
	if err != nil {
		return false, err
	}
	tmpDisk, err := text.NewDisk()
	if err != nil {
		return false, err
	}
	menu := text.NewFile(tmpDisk)
	defer func() {
		_ = menu.Close()
		_ = tmpDisk.Close()
	}()
	menu.Unread = false
	line := trimToken(f.Name.UTF8()) + "\n"
	if _, _, err := menu.LoadInitial(strings.NewReader(line)); err != nil {
		return false, err
	}
	got, ok, err := pat.Execute(menu, 0, text.Posn(menu.B.Len()))
	if err != nil {
		return false, err
	}
	return ok && got.P[0].P1 >= 0, nil
}

func readRangeBytes(f *text.File, r text.Range) ([]byte, error) {
	if r.P2 < r.P1 {
		return nil, fmt.Errorf("addresses out of order")
	}
	if r.P1 == r.P2 {
		return nil, nil
	}
	var b strings.Builder
	for p := r.P1; p < r.P2; {
		n := r.P2 - p
		if n > text.MaxBlock-1 {
			n = text.MaxBlock - 1
		}
		buf := make([]rune, n)
		if err := f.B.Read(p, buf); err != nil {
			return nil, err
		}
		if _, err := b.WriteString(string(buf)); err != nil {
			return nil, err
		}
		p += n
	}
	return []byte(b.String()), nil
}

func textStringFromBytesElidingNulls(data []byte) (*text.String, text.Posn, error) {
	s := text.NewString()
	count := text.Posn(0)
	for _, r := range string(data) {
		if r == 0 {
			continue
		}
		if err := s.Add(r); err != nil {
			return nil, 0, err
		}
		count++
	}
	if err := s.Add(0); err != nil {
		return nil, 0, err
	}
	return &s, count, nil
}

func resetFileContents(f *text.File) error {
	if err := f.B.Reset(); err != nil {
		return err
	}
	if err := f.Delta.Reset(); err != nil {
		return err
	}
	if err := f.Epsilon.Reset(); err != nil {
		return err
	}
	f.Unread = false
	f.Mod = false
	f.Seq = 0
	f.CleanSeq = 0
	f.HiPosn = 0
	f.Dot = text.Range{}
	f.NDot = text.Range{}
	f.Mark = text.Range{}
	f.PrevDot = text.Range{}
	f.PrevMark = text.Range{}
	f.PrevSeq = 0
	f.PrevMod = false
	f.ClearFileInfo()
	return nil
}

type fileStat struct {
	dev   uint64
	inode uint64
	mtime int64
}

func statFile(name string) (fileStat, bool, error) {
	info, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return fileStat{}, false, nil
		}
		return fileStat{}, false, err
	}
	meta := fileStat{mtime: info.ModTime().Unix()}
	meta.mtime = info.ModTime().UnixNano()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		meta.dev = uint64(st.Dev)
		meta.inode = uint64(st.Ino)
	}
	return meta, true, nil
}

func openFileError(name string, err error) error {
	return fmt.Errorf("can't open %q: %s", name, ioErrText(err))
}

func createFileError(name string, err error) error {
	return fmt.Errorf("can't create %q: %s", name, ioErrText(err))
}

func ioErrText(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	text := err.Error()
	r, size := utf8.DecodeRuneInString(text)
	if r == utf8.RuneError && size == 0 {
		return text
	}
	return string(unicode.ToUpper(r)) + text[size:]
}

func (s *Session) printAddress(f *text.File, a ionaddr.Address, token *text.String) error {
	if f == nil {
		return fmt.Errorf("no file")
	}
	arg := trimToken(nameTokenUTF8(token))
	charOnly := false
	switch arg {
	case "":
	case "#":
		charOnly = true
	default:
		return fmt.Errorf("newline expected")
	}

	if charOnly {
		if a.R.P1 == a.R.P2 {
			_, err := fmt.Fprintf(s.Diag, "#%d\n", a.R.P1)
			return err
		}
		_, err := fmt.Fprintf(s.Diag, "#%d,#%d\n", a.R.P1, a.R.P2)
		return err
	}

	l1, err := lineNumberAt(f, a.R.P1)
	if err != nil {
		return err
	}
	l2, err := lineNumberEnd(f, a.R)
	if err != nil {
		return err
	}
	if l1 == l2 {
		_, err = fmt.Fprintf(s.Diag, "%d", l1)
	} else {
		_, err = fmt.Fprintf(s.Diag, "%d,%d", l1, l2)
	}
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(s.Diag, "; #%d", a.R.P1); err != nil {
		return err
	}
	if a.R.P2 != a.R.P1 {
		if _, err = fmt.Fprintf(s.Diag, ",#%d", a.R.P2); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(s.Diag)
	return err
}

func lineNumberEnd(f *text.File, r text.Range) (int, error) {
	if r.P1 == r.P2 {
		return lineNumberAt(f, r.P1)
	}
	return lineNumberAt(f, r.P2-1)
}

func lineNumberAt(f *text.File, p text.Posn) (int, error) {
	if p < 0 || p > text.Posn(f.B.Len()) {
		return 0, fmt.Errorf("address range")
	}
	line := 1
	for i := text.Posn(0); i < p; i++ {
		ch, err := f.ReadRune(i)
		if err != nil {
			return 0, err
		}
		if ch == '\n' {
			line++
		}
	}
	return line, nil
}

func substituteText(f *text.File, rhs *text.String, matches ionregexp.RangeSet) ([]rune, error) {
	if rhs == nil {
		return nil, nil
	}
	var out text.String
	out = text.NewString()
	for i := 0; i < rhs.Len(); i++ {
		c := rhs.Runes()[i]
		if c == '\\' && i < rhs.Len()-1 {
			i++
			c = rhs.Runes()[i]
			if '1' <= c && c <= '9' {
				j := int(c - '0')
				n := matches.P[j].P2 - matches.P[j].P1
				buf := make([]rune, n)
				if err := f.B.Read(matches.P[j].P1, buf); err != nil {
					return nil, err
				}
				for _, r := range buf {
					if err := out.Add(r); err != nil {
						return nil, err
					}
				}
				continue
			}
			if err := out.Add(c); err != nil {
				return nil, err
			}
			continue
		}
		if c == '&' {
			n := matches.P[0].P2 - matches.P[0].P1
			buf := make([]rune, n)
			if err := f.B.Read(matches.P[0].P1, buf); err != nil {
				return nil, err
			}
			for _, r := range buf {
				if err := out.Add(r); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := out.Add(c); err != nil {
			return nil, err
		}
	}
	return append([]rune(nil), out.Runes()...), nil
}
