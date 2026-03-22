package exec

import (
	"fmt"
	"io"
	"os"
	"strings"

	ionaddr "ion/internal/core/addr"
	ioncmd "ion/internal/core/cmdlang"
	ionregexp "ion/internal/core/regexp"
	"ion/internal/core/text"
)

// Session executes parsed sam commands over a set of files.
type Session struct {
	Files   []*text.File
	Current *text.File
	Seq     uint32
	Out     io.Writer
	Diag    io.Writer
	QuitOK  bool
}

// NewSession constructs an execution session.
func NewSession(out io.Writer) *Session {
	return &Session{Out: out, Diag: io.Discard}
}

// AddFile registers a file with the session and makes it current if needed.
func (s *Session) AddFile(f *text.File) {
	s.Files = append(s.Files, f)
	if s.Current == nil {
		s.Current = f
	}
}

// Execute runs one parsed command. It returns false when execution should stop.
func (s *Session) Execute(cmd *ioncmd.Cmd) (bool, error) {
	if cmd == nil {
		return false, nil
	}

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
			if err := f.LogDelete(a.R.P1, a.R.P2, seq); err != nil {
				return err
			}
			f.NDot = text.Range{P1: a.R.P2, P2: a.R.P2}
			return s.appendLogged(f, cmd.Text, a.R.P2, seq)
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

	case '=':
		if err := s.printAddress(f, a, cmd.Text); err != nil {
			return false, err
		}
		return true, nil

	case 'w':
		if err := s.writeFile(f, cmd.Text); err != nil {
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
	if f == nil && cmd.Cmdc != 'q' {
		return nil, ionaddr.Address{}, fmt.Errorf("no current file")
	}

	eval := &ionaddr.Evaluator{Files: s.Files, Current: s.Current}
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
			return nil, ionaddr.Address{}, nil
		}
		return f, ionaddr.Address{F: f, R: f.Dot}, nil
	}
	base := ionaddr.Address{F: f, R: f.Dot}
	if f == nil {
		base = ionaddr.Address{}
	}
	a, err := eval.Resolve(ap, base, 0)
	if err != nil {
		return nil, ionaddr.Address{}, err
	}
	return a.F, a, nil
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

func (s *Session) mutate(f *text.File, fn func(seq uint32) error) error {
	s.Seq++
	seq := s.Seq
	if err := fn(seq); err != nil {
		return err
	}
	_, _, _, err := f.Update(false)
	if err == nil {
		s.QuitOK = false
	}
	return err
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

	s.Seq++
	seq := s.Seq
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
		return fmt.Errorf("no substitution")
	}
	f.NDot = text.Range{P1: a.R.P1, P2: a.R.P2 + delta}
	_, _, _, err = f.Update(false)
	return err
}

func (s *Session) xCmd(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	if cmd.Re == nil {
		return fmt.Errorf("line-based x not implemented")
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

func (s *Session) yCmd(f *text.File, cmd *ioncmd.Cmd, a ionaddr.Address) error {
	if cmd.Re == nil {
		return fmt.Errorf("line-based y not implemented")
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
	s.Seq++
	seq := s.Seq
	if err := s.copyRangeLogged(seq, src, dest); err != nil {
		return err
	}
	_, _, _, err = dest.F.Update(false)
	if err == nil {
		s.QuitOK = false
	}
	return err
}

func (s *Session) moveRange(src ionaddr.Address, ap *ionaddr.Addr) error {
	dest, err := s.resolveAddrArg(src.F, ap)
	if err != nil {
		return err
	}
	s.Seq++
	seq := s.Seq

	switch {
	case src.F == dest.F && src.R.P2 <= dest.R.P2:
		if err := src.F.LogDelete(src.R.P1, src.R.P2, seq); err != nil {
			return err
		}
		if err := s.copyRangeLogged(seq, src, dest); err != nil {
			return err
		}
	case src.F == dest.F && src.R.P1 < dest.R.P2:
		return fmt.Errorf("move overlaps itself")
	default:
		if err := s.copyRangeLogged(seq, src, dest); err != nil {
			return err
		}
		if err := src.F.LogDelete(src.R.P1, src.R.P2, seq); err != nil {
			return err
		}
	}

	if src.F == dest.F {
		_, _, _, err = src.F.Update(false)
	} else {
		if _, _, _, err = dest.F.Update(false); err == nil {
			_, _, _, err = src.F.Update(false)
		}
	}
	if err == nil {
		s.QuitOK = false
	}
	return err
}

func (s *Session) resolveAddrArg(current *text.File, ap *ionaddr.Addr) (ionaddr.Address, error) {
	if ap == nil {
		return ionaddr.Address{}, fmt.Errorf("missing address argument")
	}
	base := ionaddr.Address{}
	if current != nil {
		base = ionaddr.Address{F: current, R: current.Dot}
	}
	eval := &ionaddr.Evaluator{Files: s.Files, Current: s.Current}
	return eval.Resolve(ap, base, 0)
}

func (s *Session) copyRangeLogged(seq uint32, src, dest ionaddr.Address) error {
	size := src.R.P2 - src.R.P1
	if size < 0 {
		return fmt.Errorf("address out of order")
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

func (s *Session) writeFile(f *text.File, nameToken *text.String) error {
	name := fileNameForWrite(f, nameToken)
	if name == "" {
		return fmt.Errorf("no file name")
	}
	var b strings.Builder
	if _, err := f.WriteTo(&b); err != nil {
		return err
	}
	if err := os.WriteFile(name, []byte(b.String()), 0o666); err != nil {
		return err
	}
	f.MarkClean()
	s.QuitOK = false
	if _, err := fmt.Fprintf(s.Diag, "%s: #%d\n", name, len(b.String())); err != nil {
		return err
	}
	return nil
}

func (s *Session) fileCmd(f *text.File, nameToken *text.String) error {
	if f == nil {
		return fmt.Errorf("no file")
	}
	name := trimToken(nameTokenUTF8(nameToken))
	if name != "" {
		next := text.NewStringFromUTF8(name)
		if err := s.mutate(f, func(seq uint32) error {
			return f.LogSetName(&next, seq)
		}); err != nil {
			return err
		}
	}
	return s.printFileStatus(f, true)
}

func (s *Session) readFileInto(f *text.File, a ionaddr.Address, nameToken *text.String) error {
	name := fileNameForWrite(f, nameToken)
	if name == "" {
		return fmt.Errorf("no file name")
	}
	wasEmpty := f.B.Len() == 0 && trimToken(f.Name.UTF8()) == ""
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	txt, runeCount, err := textStringFromBytes(data)
	if err != nil {
		return err
	}
	if err := s.mutate(f, func(seq uint32) error {
		if err := f.LogDelete(a.R.P1, a.R.P2, seq); err != nil {
			return err
		}
		if err := s.appendLogged(f, txt, a.R.P2, seq); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(s.Diag, "#%d\n", len(data)); err != nil {
			return err
		}
		f.NDot = text.Range{P1: a.R.P2, P2: a.R.P2 + runeCount}
		return nil
	}); err != nil {
		return err
	}
	if wasEmpty && !containsNullByte(data) {
		f.MarkClean()
		s.QuitOK = true
	}
	return nil
}

func (s *Session) switchFile(nameToken *text.String) error {
	name := trimToken(nameTokenUTF8(nameToken))
	if name == "" {
		return fmt.Errorf("blank expected")
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
	return fmt.Errorf("not in menu: %q", name)
}

func (s *Session) listFiles() error {
	for _, f := range s.Files {
		if err := s.printFileStatus(f, f == s.Current); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) printFileStatus(f *text.File, current bool) error {
	if f == nil {
		return fmt.Errorf("no file")
	}
	mod := ' '
	if f.Mod {
		mod = '\''
	}
	cur := ' '
	if current {
		cur = '.'
	}
	_, err := fmt.Fprintf(s.Diag, "%c-%c %s\n", mod, cur, trimToken(f.Name.UTF8()))
	return err
}

func (s *Session) hasDirtyFiles() bool {
	for _, f := range s.Files {
		if f != nil && f.Mod {
			return true
		}
	}
	return false
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
		_, err = fmt.Fprintf(s.Diag, "%d; #%d", l1, a.R.P1)
	} else {
		_, err = fmt.Fprintf(s.Diag, "%d,%d; #%d,#%d", l1, l2, a.R.P1, a.R.P2)
	}
	if err != nil {
		return err
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
		return 0, fmt.Errorf("address out of range")
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
